// Path: internal/redisadmin/resp.go | Author: Codex | 2026-11-20
// T91: independent bounded RESP2 sessions; never expose peer errors or credentials.
// T93 (2026-11-20, hermes MiniMax-M3): one reply may never allocate more than
// maxReplyBytes in total, so a hostile or misconfigured endpoint cannot force
// an unbounded allocation in pools-core.
package redisadmin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

var errRedis = errors.New("Redis admin operation failed")

// maxReplyBytes caps the total payload of one reply: the sum of every bulk
// string length and every array element count in the reply. The previous
// per-element limit (1 MiB for a bulk, 32 elements for an array) still allowed
// 32 MiB per reply.
const maxReplyBytes = 1 << 20

type dialFunc func(context.Context, string, string) (net.Conn, error)
type session struct {
	conn   net.Conn
	reader *bufio.Reader
}

func withSession(ctx context.Context, dial dialFunc, addr, user, password string, fn func(*session) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return errRedis
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return errRedis
	}
	s := &session{conn, bufio.NewReader(conn)}
	if password != "" {
		args := Command{"AUTH", password}
		if user != "" {
			args = Command{"AUTH", user, password}
		}
		if err := s.ok(args); err != nil {
			return errRedis
		}
	}
	return fn(s)
}

func (s *session) command(args Command) (any, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(arg), arg)
	}
	if _, err := io.WriteString(s.conn, b.String()); err != nil {
		return nil, errRedis
	}
	return readRESP(s.reader, 0)
}

func (s *session) ok(args Command) error {
	v, err := s.command(args)
	if err != nil || v != "OK" {
		return errRedis
	}
	return nil
}

// replyBudget tracks the total size of one reply. The size of a reply is the
// sum of its bulk string lengths and its array element counts, which is what
// the parser actually allocates.
type replyBudget struct{ used int }

// take reserves n bytes of the reply budget. It reports false when the reply
// would exceed maxReplyBytes, in which case nothing is reserved.
func (b *replyBudget) take(n int) bool {
	if n < 0 || b.used > maxReplyBytes-n {
		return false
	}
	b.used += n
	return true
}

// readRESP reads one reply with a fresh per-reply budget of maxReplyBytes.
func readRESP(r *bufio.Reader, depth int) (any, error) {
	return readRESPBudget(r, depth, &replyBudget{})
}

// readRESPBudget reads one reply and refuses to allocate more than the shared
// budget allows. The budget is shared by every element of the reply, so an
// array of many large bulk strings cannot exceed the per-reply cap.
func readRESPBudget(r *bufio.Reader, depth int, budget *replyBudget) (any, error) {
	if depth > 2 {
		return nil, errRedis
	}
	line, err := r.ReadSlice('\n')
	if err != nil || len(line) < 3 || line[len(line)-2] != '\r' {
		return nil, errRedis
	}
	tag, body := line[0], string(line[1:len(line)-2])
	switch tag {
	case '+':
		// T93 (agy review): a simple string allocates len(body) bytes just
		// like a bulk string, so it must be charged to the reply budget too.
		// Without this, an array of '+' simple strings bypassed the cap:
		// a depth-2 array of 1024 four-KiB simple strings allocated ~4 MiB
		// while the budget saw only the array element counts.
		if !budget.take(len(body)) {
			return nil, errRedis
		}
		return body, nil
	case '$', '*':
		n, err := strconv.Atoi(body)
		if err != nil || n < 0 || n > maxReplyBytes {
			return nil, errRedis
		}
		if tag == '$' {
			if !budget.take(n) {
				return nil, errRedis
			}
			buf := make([]byte, n+2)
			if _, err = io.ReadFull(r, buf); err != nil || buf[n] != '\r' || buf[n+1] != '\n' {
				return nil, errRedis
			}
			return string(buf[:n]), nil
		}
		if n > 32 {
			return nil, errRedis
		}
		if !budget.take(n) {
			return nil, errRedis
		}
		items := make([]any, n)
		for i := range items {
			items[i], err = readRESPBudget(r, depth+1, budget)
			if err != nil {
				return nil, errRedis
			}
		}
		return items, nil
	default:
		return nil, errRedis
	}
}
