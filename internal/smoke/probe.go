package smoke

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BridgeProtocolVersion is what smoke asks a package for in initialize: the
// value MCPGW's executor bridge asks a child for, ProtocolVersion in MCPGW
// internal/mcpexecutor/bridge/bridge.go:45. Asking for anything else would
// test a negotiation the executor never performs.
const BridgeProtocolVersion = "2025-06-18"

// SupportedProtocolVersions is every revision MCPGW accepts, newest first:
// what protocolVersionsFor in MCPGW internal/transport/clientside/protocol.go
// returns outside legacy mode, which is the MCP go-sdk's
// mcp.SupportedProtocolVersions() at the go-sdk version MCPGW pins (v1.8.0).
// A package that negotiates a version outside it fails smoke. It moves with
// EXECUTOR_TARGET.yaml: bump it in the same pull request that moves the
// target.
var SupportedProtocolVersions = []string{
	"2026-07-28",
	"2025-11-25",
	"2025-06-18",
	"2025-03-26",
	"2024-11-05",
}

// maxFrameBytes bounds one JSON-RPC message from the package, the executor
// bridge's DefaultMaxFrameBytes.
const maxFrameBytes = 8 << 20

// sessionHeader and protocolHeader are the streamable HTTP headers.
const (
	sessionHeader  = "Mcp-Session-Id"
	protocolHeader = "Mcp-Protocol-Version"
)

// DefaultEndpoint is the streamable HTTP path when the manifest names none.
const DefaultEndpoint = "/mcp"

// ErrSessionClosed is a Call's answer when the package's stream ended, or
// the session was closed, before the response arrived.
var ErrSessionClosed = errors.New("smoke: the MCP session closed")

// Session is a minimal MCP client: enough to run the handshake and the list
// calls smoke's probe makes, and nothing a real client would need beyond
// that.
type Session interface {
	// Call sends a request and returns its result, or an *RPCError when
	// the package answered with a JSON-RPC error.
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	// Notify sends a notification, which has no answer.
	Notify(ctx context.Context, method string, params any) error
	// Close ends the session. For stdio it closes the package's stdin,
	// which is how a stdio server is told to exit.
	Close() error
}

// RPCError is a JSON-RPC error response.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// request is an outgoing message: a request with an ID, or a notification.
type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// incoming is any message from the package: a response has an ID and no
// method; a notification a method and no ID; a server-initiated request
// both.
type incoming struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

func (m incoming) isResponse() bool { return m.Method == "" && len(m.ID) > 0 }

// idKey normalises an ID so the response's echo matches the request's.
func idKey(raw json.RawMessage) string { return string(bytes.TrimSpace(raw)) }

func (m incoming) outcome() (json.RawMessage, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	return m.Result, nil
}

// stdioSession frames newline-delimited JSON-RPC over the package's stdin
// and stdout, as the executor bridge does.
type stdioSession struct {
	w   io.Writer
	wmu sync.Mutex

	mu      sync.Mutex
	next    int64
	pending map[string]chan incoming
	closed  bool
	cause   error
	done    chan struct{}
	// eof is closed when the reader stops, which only the package can
	// cause: it closed its stdout, which it does by exiting. Close does not
	// close it, so smoke can tell a package that left on its own from one
	// it shut down.
	eof chan struct{}
}

// DialStdio starts a session over a package's stdout (r) and stdin (w). One
// goroutine reads r until EOF. A line that is not JSON is skipped, not
// fatal, the executor's rule: a server printing a banner before speaking
// protocol is common. Notifications are skipped, and a server-initiated
// request is answered method-not-found, so a server that waits on, say,
// roots/list does not hang the probe.
func DialStdio(r io.Reader, w io.Writer) Session {
	s := &stdioSession{w: w, pending: map[string]chan incoming{}, done: make(chan struct{}), eof: make(chan struct{})}
	go s.readLoop(r)
	return s
}

func (s *stdioSession) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	var err error
	for {
		var line []byte
		var tooLong bool
		line, tooLong, err = readFrame(br, maxFrameBytes)
		if !tooLong && len(bytes.TrimSpace(line)) > 0 {
			s.handle(line)
		}
		if err != nil {
			break
		}
	}
	if errors.Is(err, io.EOF) {
		err = nil
	}
	close(s.eof)
	s.shut(err)
}

// streamEnded is closed when a stdio session's package closed its stdout.
// It is nil, which never fires, for any other session.
func streamEnded(sess Session) <-chan struct{} {
	if s, ok := sess.(*stdioSession); ok {
		return s.eof
	}
	return nil
}

func (s *stdioSession) handle(line []byte) {
	var m incoming
	if json.Unmarshal(line, &m) != nil {
		return
	}
	switch {
	case m.isResponse():
		s.mu.Lock()
		ch, ok := s.pending[idKey(m.ID)]
		delete(s.pending, idKey(m.ID))
		s.mu.Unlock()
		if ok {
			ch <- m
		}
	case m.Method != "" && len(m.ID) > 0:
		reply, _ := json.Marshal(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Error   RPCError        `json:"error"`
		}{"2.0", m.ID, RPCError{Code: -32601, Message: "mcplib smoke does not serve " + m.Method}})
		// Written from its own goroutine: the read loop must never block
		// on stdin, or a server busy writing to stdout would deadlock with
		// it.
		go func() { _ = s.write(reply) }()
	}
}

// readFrame reads one newline-terminated line of at most max bytes. A longer
// line is consumed to its newline and reported as tooLong. It is the
// executor bridge's framing.
func readFrame(br *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, rerr := br.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > max {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(rerr, bufio.ErrBufferFull) {
			continue
		}
		return line, tooLong, rerr
	}
}

// shut marks the session closed and fails every pending call.
func (s *stdioSession) shut(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed, s.cause = true, cause
	s.pending = map[string]chan incoming{}
	close(s.done)
}

func (s *stdioSession) closedErr() error {
	if s.cause != nil {
		return fmt.Errorf("%w: %v", ErrSessionClosed, s.cause)
	}
	return ErrSessionClosed
}

func (s *stdioSession) write(b []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("smoke: write to package: %w", err)
	}
	return nil
}

func (s *stdioSession) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, s.closedErr()
	}
	s.next++
	id := s.next
	key := strconv.FormatInt(id, 10)
	ch := make(chan incoming, 1)
	s.pending[key] = ch
	s.mu.Unlock()

	b, err := json.Marshal(request{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err == nil {
		err = s.write(b)
	}
	if err != nil {
		s.mu.Lock()
		delete(s.pending, key)
		s.mu.Unlock()
		return nil, err
	}
	select {
	case m := <-ch:
		return m.outcome()
	case <-s.done:
		// A response delivered just before shut still wins.
		select {
		case m := <-ch:
			return m.outcome()
		default:
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return nil, s.closedErr()
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, key)
		s.mu.Unlock()
		return nil, fmt.Errorf("smoke: %s: %w", method, ctx.Err())
	}
}

func (s *stdioSession) Notify(_ context.Context, method string, params any) error {
	b, err := json.Marshal(request{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	return s.write(b)
}

// Close closes the package's stdin if the writer can be closed. It does not
// close stdout: the caller reads that to EOF to see the package exit.
func (s *stdioSession) Close() error {
	s.shut(nil)
	if c, ok := s.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// socketSession speaks streamable HTTP to a package's unix socket, the way
// the executor's passthrough proxy reaches an http-transport child.
type socketSession struct {
	client   *http.Client
	url      string
	next     int64
	mu       sync.Mutex
	session  string
	protocol string
}

// WaitSocket polls until the package accepts a connection on path, with the
// executor's short doubling backoff, or ctx ends.
func WaitSocket(ctx context.Context, path string) error {
	backoff := 20 * time.Millisecond
	for {
		var d net.Dialer
		c, err := d.DialContext(ctx, "unix", path)
		if err == nil {
			return c.Close()
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("smoke: the package did not open its socket %s: %w", path, errors.Join(ctx.Err(), err))
		case <-time.After(backoff):
		}
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
}

// DialSocket starts a streamable HTTP session over the unix socket at path,
// at DefaultEndpoint. Use WaitSocket first: DialSocket does not wait.
func DialSocket(path string) (Session, error) { return DialSocketAt(path, "") }

// DialSocketAt is DialSocket at the manifest's path; empty means
// DefaultEndpoint.
func DialSocketAt(path, endpoint string) (Session, error) {
	if path == "" {
		return nil, errors.New("smoke: no socket path")
	}
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if !strings.HasPrefix(endpoint, "/") {
		return nil, fmt.Errorf("smoke: endpoint %q is not an absolute path", endpoint)
	}
	transport := &http.Transport{
		// The address is ignored: the one destination is the socket.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
	return &socketSession{client: &http.Client{Transport: transport}, url: "http://mcp-package" + endpoint}, nil
}

func (s *socketSession) post(ctx context.Context, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	s.mu.Lock()
	if s.session != "" {
		req.Header.Set(sessionHeader, s.session)
	}
	if s.protocol != "" {
		req.Header.Set(protocolHeader, s.protocol)
	}
	s.mu.Unlock()
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("smoke: post: %w", err)
	}
	if sid := resp.Header.Get(sessionHeader); sid != "" {
		s.mu.Lock()
		s.session = sid
		s.mu.Unlock()
	}
	return resp, nil
}

func (s *socketSession) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.next++
	id := s.next
	s.mu.Unlock()
	resp, err := s.post(ctx, request{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("smoke: %s: HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	m, err := readResponse(resp, strconv.FormatInt(id, 10))
	if err != nil {
		return nil, fmt.Errorf("smoke: %s: %w", method, err)
	}
	if method == "initialize" && m.Error == nil {
		// Every later request carries the negotiated revision, as the
		// 2025-06-18 transport requires of a client.
		var r struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(m.Result, &r) == nil {
			s.mu.Lock()
			s.protocol = r.ProtocolVersion
			s.mu.Unlock()
		}
	}
	return m.outcome()
}

// readResponse finds the response to id in a JSON or SSE body, skipping any
// notifications and requests an SSE stream carries before it.
func readResponse(resp *http.Response, id string) (incoming, error) {
	body := io.LimitReader(resp.Body, maxFrameBytes)
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch ct {
	case "application/json":
		var m incoming
		if err := json.NewDecoder(body).Decode(&m); err != nil {
			return incoming{}, fmt.Errorf("decode response: %w", err)
		}
		if !m.isResponse() || idKey(m.ID) != id {
			return incoming{}, fmt.Errorf("the body is not the response to request %s", id)
		}
		return m, nil
	case "text/event-stream":
		br := bufio.NewReaderSize(body, 64<<10)
		var data []byte
		for {
			line, tooLong, err := readFrame(br, maxFrameBytes)
			if tooLong {
				return incoming{}, errors.New("an event exceeds the frame bound")
			}
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0 && len(data) > 0:
				var m incoming
				if json.Unmarshal(data, &m) == nil && m.isResponse() && idKey(m.ID) == id {
					return m, nil
				}
				data = data[:0]
			case bytes.HasPrefix(line, []byte("data:")):
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))...)
			}
			if err != nil {
				return incoming{}, fmt.Errorf("the stream ended before the response to request %s: %w", id, ErrSessionClosed)
			}
		}
	default:
		return incoming{}, fmt.Errorf("unexpected content type %q", resp.Header.Get("Content-Type"))
	}
}

func (s *socketSession) Notify(ctx context.Context, method string, params any) error {
	resp, err := s.post(ctx, request{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("smoke: %s: HTTP %d", method, resp.StatusCode)
	}
	return nil
}

// Close ends the session with a DELETE when the package minted one. The
// DELETE is best effort: a server may answer 405, and smoke judges the
// package on its exit, not on this.
func (s *socketSession) Close() error {
	defer s.client.CloseIdleConnections()
	s.mu.Lock()
	sid := s.session
	s.mu.Unlock()
	if sid == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set(sessionHeader, sid)
	if resp, err := s.client.Do(req); err == nil {
		_ = resp.Body.Close()
	}
	return nil
}
