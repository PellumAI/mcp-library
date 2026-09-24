package smoke

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// dialProxy opens a raw TCP connection to the proxy under test, so CONNECT
// tunnels can be driven byte-for-byte without relying on an http.Client's
// own (TLS-assuming) CONNECT support.
func dialProxy(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readAttempts decodes every JSON line the proxy has written to buf so far.
func readAttempts(t *testing.T, buf *syncBuffer) []Attempt {
	t.Helper()
	var attempts []Attempt
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var a Attempt
		if err := json.Unmarshal(line, &a); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		attempts = append(attempts, a)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan log: %v", err)
	}
	return attempts
}

func TestProxy_AllowsDeclared(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from backend")
	}))
	defer backend.Close()
	backendHost, backendPort := splitTestAddr(t, backend.Listener.Addr().String())

	var logBuf syncBuffer
	proxy := httptest.NewServer(NewProxy([]string{backendHost}, &logBuf))
	defer proxy.Close()

	conn := dialProxy(t, proxy.Listener.Addr().String())
	target := net.JoinHostPort(backendHost, backendPort)
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if !strings.Contains(statusLine, "200") {
		t.Fatalf("CONNECT status = %q, want 200", statusLine)
	}
	// consume the rest of the CONNECT response headers up to the blank line.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read CONNECT headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	// The tunnel is open: issue a plain HTTP request through it to the backend.
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target)
	body, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read tunneled response: %v", err)
	}
	if !strings.Contains(string(body), "hello from backend") {
		t.Fatalf("tunneled response = %q, want it to contain backend body", body)
	}

	attempts := readAttempts(t, &logBuf)
	if len(attempts) != 1 {
		t.Fatalf("logged %d attempts, want 1: %+v", len(attempts), attempts)
	}
	got := attempts[0]
	if got.Host != backendHost || !got.Allowed {
		t.Fatalf("attempt = %+v, want host=%s allowed=true", got, backendHost)
	}
	if got.Time.IsZero() {
		t.Fatalf("attempt.Time is zero")
	}
}

func TestProxy_DeniesUndeclared(t *testing.T) {
	var logBuf syncBuffer
	proxy := httptest.NewServer(NewProxy([]string{"context7.com"}, &logBuf))
	defer proxy.Close()

	conn := dialProxy(t, proxy.Listener.Addr().String())
	target := "evil.example.com:443"
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if !strings.Contains(statusLine, "403") {
		t.Fatalf("CONNECT status = %q, want 403", statusLine)
	}

	attempts := readAttempts(t, &logBuf)
	if len(attempts) != 1 {
		t.Fatalf("logged %d attempts, want 1: %+v", len(attempts), attempts)
	}
	got := attempts[0]
	if got.Host != "evil.example.com" || got.Port != 443 || got.Allowed {
		t.Fatalf("attempt = %+v, want host=evil.example.com port=443 allowed=false", got)
	}
}

func TestProxy_PlainHTTP(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "plain backend body")
	}))
	defer backend.Close()
	backendHost, _ := splitTestAddr(t, backend.Listener.Addr().String())

	var logBuf syncBuffer
	proxy := httptest.NewServer(NewProxy([]string{backendHost}, &logBuf))
	defer proxy.Close()

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL(t, proxy.URL)),
		},
	}
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "plain backend body") {
		t.Fatalf("body = %q, want it to contain backend body", body)
	}

	attempts := readAttempts(t, &logBuf)
	if len(attempts) != 1 {
		t.Fatalf("logged %d attempts, want 1: %+v", len(attempts), attempts)
	}
	got := attempts[0]
	if got.Host != backendHost || !got.Allowed {
		t.Fatalf("attempt = %+v, want host=%s allowed=true", got, backendHost)
	}
}

// TestProxy_ConnectPortlessAuthorityDialsLoggedTarget covers a fix-round
// regression: a portless CONNECT authority ("CONNECT example.com", no
// ":port") must be dialed at the same host:port the allow decision and log
// line computed (host defaulted to connectDefaultPort), not at the raw,
// portless r.Host -- which net.Dialer rejects outright, leaving the log
// claiming an allowed connection that was never attempted. The test points
// connectDefaultPort at the backend's real ephemeral port instead of
// mocking the dial, so a full round trip through the tunnel is the proof
// that the dialed target and the logged target are the same address.
func TestProxy_ConnectPortlessAuthorityDialsLoggedTarget(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from portless backend")
	}))
	defer backend.Close()
	backendHost, backendPort := splitTestAddr(t, backend.Listener.Addr().String())

	var logBuf syncBuffer
	handler := NewProxy([]string{backendHost}, &logBuf)
	p, ok := handler.(*proxy)
	if !ok {
		t.Fatalf("NewProxy returned %T, want *proxy", handler)
	}
	p.connectDefaultPort = backendPort // test hook: see doc comment above.

	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()

	conn := dialProxy(t, proxySrv.Listener.Addr().String())
	// Portless authority: no ":port" after the host, so the proxy must
	// supply connectDefaultPort itself for both the decision and the dial.
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", backendHost, backendHost)

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if !strings.Contains(statusLine, "200") {
		t.Fatalf("CONNECT status = %q, want 200", statusLine)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read CONNECT headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	// The tunnel only reaches the backend if the proxy dialed
	// backendHost:backendPort -- proving the dial target matches what was
	// logged below, not the portless raw authority.
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", backendHost)
	body, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read tunneled response: %v", err)
	}
	if !strings.Contains(string(body), "hello from portless backend") {
		t.Fatalf("tunneled response = %q, want it to contain the backend body", body)
	}

	attempts := readAttempts(t, &logBuf)
	if len(attempts) != 1 {
		t.Fatalf("logged %d attempts, want 1: %+v", len(attempts), attempts)
	}
	wantPort, err := strconv.Atoi(backendPort)
	if err != nil {
		t.Fatalf("parse backend port %q: %v", backendPort, err)
	}
	got := attempts[0]
	if got.Host != backendHost || got.Port != wantPort || !got.Allowed {
		t.Fatalf("attempt = %+v, want host=%s port=%d allowed=true", got, backendHost, wantPort)
	}
}

func splitTestAddr(t *testing.T, addr string) (host, port string) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr %q: %v", addr, err)
	}
	return host, port
}
