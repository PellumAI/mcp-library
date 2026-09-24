package smoke

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// fakeStdioServer answers initialize and tools/list over newline-delimited
// JSON-RPC, and before each answer emits the noise a real server does: a
// banner line, a notification, and a server-initiated request.
func fakeStdioServer(t *testing.T, in io.Reader, out io.WriteCloser, seen chan<- rpcMsg) {
	t.Helper()
	defer out.Close()
	fmt.Fprintln(out, "context7 server starting")
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		var m rpcMsg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Errorf("client sent non-JSON %q", sc.Text())
			return
		}
		seen <- m
		if m.ID == nil || m.Method == "" {
			continue // a notification, or the answer to our request
		}
		fmt.Fprintln(out, `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}`)
		fmt.Fprintln(out, `{"jsonrpc":"2.0","id":"srv-1","method":"roots/list"}`)
		var result string
		switch m.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}`
		case "tools/list":
			result = `{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}`
		default:
			fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"no such method"}}`+"\n", m.ID)
			continue
		}
		fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", m.ID, result)
	}
}

func TestDialStdio_RoundTrip(t *testing.T) {
	cr, cw := io.Pipe() // client -> server
	sr, sw := io.Pipe() // server -> client
	seen := make(chan rpcMsg, 16)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		fakeStdioServer(t, cr, sw, seen)
	}()

	s := DialStdio(sr, cw)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := s.Call(ctx, "initialize", map[string]any{
		"protocolVersion": BridgeProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mcplib-smoke", "version": "0"},
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(res, &init); err != nil || init.ProtocolVersion != "2025-06-18" {
		t.Fatalf("initialize result %s: %v", res, err)
	}
	if err := s.Notify(ctx, "notifications/initialized", nil); err != nil {
		t.Fatalf("notify: %v", err)
	}
	res, err = s.Call(ctx, "tools/list", nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var tools struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(res, &tools); err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Fatalf("tools/list result %s: %v", res, err)
	}

	_, err = s.Call(ctx, "bogus", nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32601 {
		t.Fatalf("bogus: want RPCError -32601, got %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	<-serverDone

	// The server saw initialize, the notification, and a method-not-found
	// answer to each of its roots/list requests.
	var methods []string
	var answered int
	for len(seen) > 0 {
		m := <-seen
		if m.Method != "" {
			methods = append(methods, m.Method)
		} else if string(m.ID) == `"srv-1"` {
			answered++
		}
	}
	if want := "initialize,notifications/initialized,tools/list,bogus"; strings.Join(methods, ",") != want {
		t.Errorf("methods = %v, want %s", methods, want)
	}
	if answered == 0 {
		t.Error("server-initiated request was never answered")
	}
}

func TestDialStdio_EOFFailsPending(t *testing.T) {
	cr, cw := io.Pipe()
	sr, sw := io.Pipe()
	go func() {
		_, _ = bufio.NewReader(cr).ReadBytes('\n')
		_ = sw.Close() // the server dies mid-call
	}()
	s := DialStdio(sr, cw)
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Call(ctx, "initialize", nil); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("want ErrSessionClosed, got %v", err)
	}
}

func TestDialStdio_ContextTimeout(t *testing.T) {
	cr, cw := io.Pipe()
	sr, _ := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, cr) }()
	s := DialStdio(sr, cw)
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.Call(ctx, "initialize", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
}

// fakeHTTPServer is a streamable HTTP MCP endpoint at /mcp: initialize
// mints a session and answers in JSON, tools/list answers over SSE after a
// notification, and a notification is 202.
type fakeHTTPServer struct {
	mu      sync.Mutex
	headers []http.Header
	deleted bool
}

func (f *fakeHTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.headers = append(f.headers, r.Header.Clone())
	f.mu.Unlock()
	if r.URL.Path != "/mcp" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		f.mu.Lock()
		f.deleted = r.Header.Get("Mcp-Session-Id") == "sess-1"
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var m rpcMsg
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch m.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{}}}`, m.ID)
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		if r.Header.Get("Mcp-Session-Id") != "sess-1" {
			http.Error(w, "no session", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\n", m.ID)
		fmt.Fprint(w, "data: \"result\":{\"tools\":[{\"name\":\"echo\"}]}}\n\n")
	default:
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"nope"}}`, m.ID)
	}
}

func TestDialSocket_RoundTrip(t *testing.T) {
	// A short path: unix socket paths are capped near 108 bytes and
	// t.TempDir can exceed it.
	dir, err := os.MkdirTemp("", "smk")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sock := filepath.Join(dir, "mcp.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeHTTPServer{}
	srv := httptest.NewUnstartedServer(fake)
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitSocket(ctx, sock); err != nil {
		t.Fatalf("WaitSocket: %v", err)
	}
	s, err := DialSocket(sock)
	if err != nil {
		t.Fatalf("DialSocket: %v", err)
	}
	if _, err := s.Call(ctx, "initialize", map[string]any{"protocolVersion": BridgeProtocolVersion}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := s.Notify(ctx, "notifications/initialized", nil); err != nil {
		t.Fatalf("notify: %v", err)
	}
	res, err := s.Call(ctx, "tools/list", nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if !strings.Contains(string(res), `"echo"`) {
		t.Fatalf("tools/list result %s", res)
	}
	var rpcErr *RPCError
	if _, err := s.Call(ctx, "bogus", nil); !errors.As(err, &rpcErr) {
		t.Fatalf("bogus: want RPCError, got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.deleted {
		t.Error("Close did not DELETE the session")
	}
	first := fake.headers[0]
	if got := first.Get("Accept"); !strings.Contains(got, "application/json") || !strings.Contains(got, "text/event-stream") {
		t.Errorf("Accept = %q", got)
	}
	if first.Get("Mcp-Session-Id") != "" {
		t.Error("initialize must not carry a session id")
	}
	last := fake.headers[len(fake.headers)-2] // the bogus call, before DELETE
	if last.Get("Mcp-Session-Id") != "sess-1" || last.Get("Mcp-Protocol-Version") != "2025-06-18" {
		t.Errorf("follow-up headers = %v", last)
	}
}

func TestWaitSocket_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := WaitSocket(ctx, filepath.Join(t.TempDir(), "absent.sock")); err == nil {
		t.Fatal("WaitSocket returned for a socket that never appears")
	}
}

func TestSupportedProtocolVersions(t *testing.T) {
	found := false
	for _, v := range SupportedProtocolVersions {
		if v == BridgeProtocolVersion {
			found = true
		}
	}
	if !found {
		t.Errorf("BridgeProtocolVersion %s not in %v", BridgeProtocolVersion, SupportedProtocolVersions)
	}
}
