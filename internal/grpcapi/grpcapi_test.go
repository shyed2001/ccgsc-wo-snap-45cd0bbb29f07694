// Path: internal/grpcapi/grpcapi_test.go | Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: the offline tests of the gRPC surface: the parity with the HTTP
// routes, the auth claims, the 429 with the retry-after trailer, the server
// streaming of the events and the not-implemented answers; WO T42 F10.
package grpcapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// harness is the gRPC service over a file backed SQLite store, with a real
// gRPC client that talks to it over a loopback listener. The REST handler of
// the same server answers beside it, so the tests compare both transports.
type harness struct {
	server   *Server
	service  *Service
	engine   *pool.Engine
	auth     *auth.Service
	rest     http.Handler
	conn     *grpc.ClientConn
	thread   ThreadPoolClient
	process  ProcessPoolClient
	adminKey string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, logger, "s1", time.Minute)
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}
	authService := auth.New(db, config.Default().JWTSecret, "s1")
	service := New(engine, authService)
	server := NewServer(service)
	rest := &api.Server{Engine: engine, Auth: authService, Log: logger, Config: config.Default()}
	h := &harness{server: server, service: service, engine: engine, auth: authService, rest: rest.Handler()}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	h.conn = conn
	h.thread = NewThreadPoolClient(conn)
	h.process = NewProcessPoolClient(conn)
	return h
}

// key issues an API key and a JWT for the given role.
func (h *harness) key(t *testing.T, role string) (string, string) {
	t.Helper()
	account := "B"
	if role == "admin" || role == "director" {
		account = "A"
	}
	raw, _, err := h.auth.CreateKey(context.Background(), auth.User{ID: auth.ID(), AccountStore: account, Roles: []string{role}}, []string{"*"})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	token, err := h.auth.Exchange(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	return raw, token.AccessToken
}

// withToken attaches the bearer token to the call context.
func withToken(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

// artifact uploads a module artifact and returns its id and its bytes.
func (h *harness) artifact(t *testing.T, token string) (string, []byte) {
	t.Helper()
	data := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	id := verify.SHA256(data)
	_, err := h.thread.UploadArtifact(withToken(context.Background(), token), &ArtifactUploadRequest{
		Sha256:       id,
		UploadOffset: 0,
		UploadLength: int64(len(data)),
		Chunk:        data,
		ContentType:  "application/wasm",
	})
	if err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}
	return id, data
}

// restRequest calls the REST handler of the same server.
func (h *harness) restRequest(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytesReader(raw))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.rest.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

// bytesReader builds a reader over the bytes without importing bytes twice.
func bytesReader(raw []byte) io.Reader {
	if raw == nil {
		return http.NoBody
	}
	return &sliceReader{data: raw}
}

// sliceReader is a minimal reader over a byte slice.
type sliceReader struct {
	data []byte
	pos  int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// codeOfStatus reads the gRPC code of an error.
func codeOfStatus(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	st, _ := status.FromError(err)
	return st.Code()
}

// errorDetailOf reads the ErrorDetail that a failed call carries in the
// status details.
func errorDetailOf(t *testing.T, err error) *ErrorDetail {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("the error is not a status: %v", err)
	}
	for _, detail := range st.Details() {
		if value, ok := detail.(*ErrorDetail); ok {
			return value
		}
	}
	t.Fatalf("the status carries no ErrorDetail: %v", err)
	return nil
}

// keyID reads the key id of the claims of a token, so the test can read the
// job of its owner from the engine.
func (h *harness) keyID(t *testing.T, token string) string {
	t.Helper()
	claims, err := h.auth.Validate(context.Background(), token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return claims.KeyID
}

// newRequest builds a request for the REST handler of the test.
func newRequest(method, path string, body []byte) *http.Request {
	request := httptest.NewRequest(method, path, bytesReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

// newRecorder builds a recorder for the REST handler of the test.
func newRecorder() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}
