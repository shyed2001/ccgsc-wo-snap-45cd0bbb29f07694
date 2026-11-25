// Path: internal/store/redislease/client.go
// MVPW slice D (hermes): minimal RESP2 client for lease coordination (SET NX PX, GET, PEXPIRE, DEL).
// No Lua/EVAL. Fencing via token value. Lease issue/renew/expire via redis when lease_store=redis.
// Settle remains in PG store. Stdlib only.
// Round 8 (owner decision R42.7, shared VPS Redis safe use): optional ACL AUTH
// (Redis 6 form `AUTH <user> <pass>`, legacy one-argument form for the default
// user), optional `SELECT <db>` on every (re)connect, and a wire key prefix
// (default "pools:") applied to every lease key. Credentials arrive only via
// the POOLS_REDIS_USER / POOLS_REDIS_PASSWORD environment variables and are
// never logged, never echoed into errors and never reported by /capabilities.
package redislease

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ccgsc/poolscore/internal/redact"
)

const defaultCommandTimeout = 2 * time.Second

// DefaultPrefix is the wire key prefix applied to every lease key when no
// explicit prefix is configured (R42.7: on the shared VPS Redis the pools
// lease keys must never mix with other tenants' keys).
const DefaultPrefix = "pools:"

// Options carries the R42.7 safe-use settings for a shared Redis. A zero
// Options keeps the documented defaults: no AUTH, DB 0, prefix "pools:".
type Options struct {
	// Username of the Redis 6 ACL user. Empty uses the default user with the
	// legacy one-argument AUTH <password> form.
	Username string
	// Password; empty disables AUTH entirely.
	Password string
	// DB is the logical Redis DB index. 0 (the default DB) sends no SELECT.
	DB int
	// Prefix is applied to every lease key on the wire. Empty falls back to
	// DefaultPrefix ("pools:"); there is no unprefixed mode from configuration.
	Prefix string
}

// String implements fmt.Stringer with the password redacted (T101): %v of
// Options can reach a log through error wrapping and must stay credential-free.
func (o Options) String() string {
	return fmt.Sprintf("redislease.Options{Username:%q, Password:%s, DB:%d, Prefix:%q}",
		o.Username, redact.Password(o.Password), o.DB, o.Prefix)
}

// GoString is the %#v counterpart of String, so no debug format leaks the
// password either.
func (o Options) GoString() string { return o.String() }

type Client struct {
	addr           string
	commandTimeout time.Duration
	username       string
	password       string
	db             int
	prefix         string
	conn           net.Conn
	reader         *bufio.Reader
	mu             sync.Mutex
}

// String implements fmt.Stringer with the password redacted (T101): the
// password field never leaves the client, no matter which format verb prints
// the client into a log. The connection state is read under the lock.
func (c *Client) String() string {
	if c == nil {
		return "redislease.Client(nil)"
	}
	c.mu.Lock()
	connected := c.conn != nil
	c.mu.Unlock()
	return fmt.Sprintf("redislease.Client{addr:%q, commandTimeout:%s, username:%q, password:%s, db:%d, prefix:%q, connected:%t}",
		c.addr, c.commandTimeout, c.username, redact.Password(c.password), c.db, c.prefix, connected)
}

// GoString is the %#v counterpart of String.
func (c *Client) GoString() string { return c.String() }

// normalizeAddr validates a Redis address before it is dialed or printed
// (T101). A plain host:port passes through unchanged. A URL form
// (redis://host:port) keeps only the host and port. Any address that carries
// userinfo credentials is rejected with an error that quotes neither the
// address nor the credential: net.Dial errors would otherwise print the
// address, and with it the password, into logs.
func normalizeAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("redislease: address is empty")
	}
	if !strings.Contains(addr, "://") {
		if strings.Contains(addr, "@") {
			return "", errors.New("redislease: address must not carry userinfo credentials; put them in POOLS_REDIS_USER / POOLS_REDIS_PASSWORD")
		}
		return addr, nil
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" {
		return "", errors.New("redislease: address is not a usable redis URL (unparseable or missing host)")
	}
	if u.User != nil {
		return "", errors.New("redislease: address must not carry userinfo credentials; put them in POOLS_REDIS_USER / POOLS_REDIS_PASSWORD")
	}
	switch u.Scheme {
	case "redis", "tcp":
		return u.Host, nil
	default:
		return "", fmt.Errorf("redislease: unsupported address scheme %q (use redis://host:port or plain host:port)", u.Scheme)
	}
}

// Dial connects to a RESP2 server (redis or fake).
func Dial(addr string) (*Client, error) {
	return DialWithTimeout(addr, defaultCommandTimeout)
}

// DialWithTimeout connects to a RESP2 server and bounds each command's I/O.
func DialWithTimeout(addr string, commandTimeout time.Duration) (*Client, error) {
	return DialWithOptions(addr, commandTimeout, Options{})
}

// DialWithOptions connects to a RESP2 server with the R42.7 safe-use options
// and completes the AUTH/SELECT handshake before the client is returned, so a
// wrong credential or an unavailable DB fails at startup with a clear error.
// T101: the address is normalized first, so an address with userinfo
// credentials is rejected before any dial, and the dial address can never
// carry a password into an error message.
func DialWithOptions(addr string, commandTimeout time.Duration, opt Options) (*Client, error) {
	if commandTimeout <= 0 {
		return nil, fmt.Errorf("redislease command timeout must be positive")
	}
	addr, err := normalizeAddr(addr)
	if err != nil {
		return nil, err
	}
	prefix := opt.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	c := &Client{
		addr:           addr,
		commandTimeout: commandTimeout,
		username:       opt.Username,
		password:       opt.Password,
		db:             opt.DB,
		prefix:         prefix,
	}
	conn, err := net.DialTimeout("tcp", addr, commandTimeout)
	if err != nil {
		return nil, fmt.Errorf("redislease dial %s: %w", addr, err)
	}
	c.conn = conn
	c.reader = bufio.NewReader(conn)
	// No other goroutine can hold this fresh client yet: run the handshake
	// here so AUTH and SELECT precede every lease command on the connection.
	if err := c.handshake(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		c.reader = nil
		return err
	}
	return nil
}

func (c *Client) reconnect() error {
	conn, err := net.DialTimeout("tcp", c.addr, c.commandTimeout)
	if err != nil {
		return fmt.Errorf("redislease reconnect %s: %w", c.addr, err)
	}
	c.conn = conn
	c.reader = bufio.NewReader(conn)
	// R42.7: every (re)connect repeats AUTH and SELECT before any lease
	// command may run on the fresh connection.
	if err := c.handshake(); err != nil {
		return err
	}
	return nil
}

// handshake authenticates and selects the configured logical DB on the current
// connection. Callers hold c.mu (reconnect) or exclusively own the fresh client
// (dial). A failed handshake discards the connection.
func (c *Client) handshake() error {
	if c.password == "" && c.db == 0 {
		return nil
	}
	if err := c.conn.SetDeadline(time.Now().Add(c.commandTimeout)); err != nil {
		c.discardConn()
		return fmt.Errorf("redislease handshake deadline: %w", err)
	}
	if c.password != "" {
		args := []string{"AUTH", c.password}
		if c.username != "" {
			args = []string{"AUTH", c.username, c.password}
		}
		if err := c.handshakeCommand(args); err != nil {
			c.discardConn()
			// The error names the command and the address only; credentials
			// are never echoed into error text, logs or /capabilities.
			return fmt.Errorf("redislease: AUTH to %s failed (check POOLS_REDIS_USER / POOLS_REDIS_PASSWORD): %v", c.addr, err)
		}
	}
	if c.db != 0 {
		if err := c.handshakeCommand([]string{"SELECT", strconv.Itoa(c.db)}); err != nil {
			c.discardConn()
			return fmt.Errorf("redislease: SELECT %d to %s failed: %v", c.db, c.addr, err)
		}
	}
	return nil
}

// handshakeCommand sends one handshake command and requires the +OK reply.
func (c *Client) handshakeCommand(args []string) error {
	if err := c.writeArray(args); err != nil {
		return err
	}
	resp, err := c.readResp()
	if err != nil {
		return err
	}
	if resp != "OK" {
		return fmt.Errorf("reply %q, want OK", resp)
	}
	return nil
}

func (c *Client) ensureConn() error {
	if c.conn != nil {
		return nil
	}
	return c.reconnect()
}

func (c *Client) discardConn() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn = nil
	c.reader = nil
}

func (c *Client) beginCommand() error {
	if err := c.ensureConn(); err != nil {
		return err
	}
	if err := c.conn.SetDeadline(time.Now().Add(c.commandTimeout)); err != nil {
		c.discardConn()
		return fmt.Errorf("redislease deadline: %w", err)
	}
	return nil
}

func (c *Client) writeArray(args []string) error {
	var buf bytes.Buffer
	buf.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		buf.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n")
	}
	_, err := c.conn.Write(buf.Bytes())
	if err != nil {
		c.discardConn()
		return fmt.Errorf("redislease write: %w", err)
	}
	return nil
}

func (c *Client) readResp() (string, error) {
	line, err := c.reader.ReadString('\n')
	if err != nil {
		c.discardConn()
		return "", fmt.Errorf("redislease read line: %w", err)
	}
	if !strings.HasSuffix(line, "\r\n") {
		c.discardConn()
		return "", fmt.Errorf("redislease invalid line terminator")
	}
	line = strings.TrimSuffix(line, "\r\n")
	switch {
	case strings.HasPrefix(line, "+"):
		return strings.TrimPrefix(line, "+"), nil
	case strings.HasPrefix(line, "-"):
		return "", fmt.Errorf("redislease err: %s", strings.TrimPrefix(line, "-"))
	case strings.HasPrefix(line, ":"):
		return strings.TrimPrefix(line, ":"), nil
	case strings.HasPrefix(line, "$"):
		nstr := strings.TrimPrefix(line, "$")
		n, err := strconv.Atoi(nstr)
		if err != nil {
			c.discardConn()
			return "", fmt.Errorf("redislease bad bulk len: %w", err)
		}
		if n < 0 {
			return "", nil // null bulk
		}
		data := make([]byte, n+2)
		if _, err := io.ReadFull(c.reader, data); err != nil {
			c.discardConn()
			return "", fmt.Errorf("redislease read bulk: %w", err)
		}
		if data[n] != '\r' || data[n+1] != '\n' {
			c.discardConn()
			return "", fmt.Errorf("redislease invalid bulk terminator")
		}
		return string(data[:n]), nil
	default:
		c.discardConn()
		return "", fmt.Errorf("redislease unexpected: %q", line)
	}
}

// leaseKey applies the configured wire prefix to a logical lease key, so the
// engine's "lease:<jobId>:<taskId>" keys travel as "<prefix>lease:<jobId>:<taskId>"
// (R42.7). The prefix is applied exactly once, here in the client, so no call
// site can forget it and none can apply it twice.
func (c *Client) leaseKey(key string) string {
	return c.prefix + key
}

// SetNX issues lease: SET key token NX PX ms . Returns true if acquired (was absent).
func (c *Client) SetNX(key, token string, pxMs int) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.beginCommand(); err != nil {
		return false, err
	}
	if err := c.writeArray([]string{"SET", c.leaseKey(key), token, "NX", "PX", strconv.Itoa(pxMs)}); err != nil {
		return false, err
	}
	resp, err := c.readResp()
	if err != nil {
		return false, err
	}
	return resp == "OK", nil
}

// Get returns current token or "" if absent/expired (client side).
func (c *Client) Get(key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.beginCommand(); err != nil {
		return "", err
	}
	if err := c.writeArray([]string{"GET", c.leaseKey(key)}); err != nil {
		return "", err
	}
	return c.readResp()
}

// PExpire extends the lease ttl for the current token holder (caller must have verified via Get).
func (c *Client) PExpire(key string, ms int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.beginCommand(); err != nil {
		return err
	}
	if err := c.writeArray([]string{"PEXPIRE", c.leaseKey(key), strconv.Itoa(ms)}); err != nil {
		return err
	}
	_, err := c.readResp()
	return err
}

// Del removes the lease entry (used on explicit expire or settle).
func (c *Client) Del(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.beginCommand(); err != nil {
		return err
	}
	if err := c.writeArray([]string{"DEL", c.leaseKey(key)}); err != nil {
		return err
	}

	_, err := c.readResp()
	return err
}

// CompareAndDelete is a best-effort fencing operation without Lua/EVAL. The
// caller accepts the small GET/DEL race; a newer token can only be deleted if
// it replaces ours between these commands.
func (c *Client) CompareAndDelete(key, token string) error {
	if current, err := c.Get(key); err != nil {
		return err
	} else if current == token {
		return c.Del(key)
	}
	return nil
}

// Connect is the startup switch for lease_store with the R42.7 safe-use
// options (ACL AUTH, logical DB index, key prefix).
// pg (default) does not dial; redis dials addr (POOLS_REDIS_ADDR) and completes
// the AUTH/SELECT handshake before returning, so wrong credentials fail with a
// clear startup error. No Lua.
func Connect(mode, addr string, opt Options) (*Client, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == "pg" {
		return nil, nil
	}
	if mode != "redis" {
		return nil, fmt.Errorf("leaseStore must be pg or redis, got %q", mode)
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("leaseStore=redis requires POOLS_REDIS_ADDR")
	}
	return DialWithOptions(addr, defaultCommandTimeout, opt)
}

// ConnectIfRedis is the startup switch for lease_store.
// pg (default) does not dial; redis dials addr (POOLS_REDIS_ADDR). No Lua.
// Compatibility form of Connect: it applies the default safe-use options
// (no AUTH, DB 0, prefix "pools:").
func ConnectIfRedis(mode, addr string) (*Client, error) {
	return Connect(mode, addr, Options{})
}
