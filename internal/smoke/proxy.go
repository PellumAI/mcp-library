// Package smoke implements the test deployment mcplib smoke runs: a package
// tar executed in the pinned build image under a locked-down container, with
// this package's CONNECT allow-list egress proxy standing between it and the
// network so smoke can prove the server dials only the hosts its manifest
// declares.
package smoke

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Attempt is one logged connection attempt the proxy decided on, written as
// one JSON object per line to the proxy's log. smoke reads this log back to
// fail the run on any denied attempt.
type Attempt struct {
	Host    string    `json:"host"`
	Port    int       `json:"port"`
	Allowed bool      `json:"allowed"`
	Time    time.Time `json:"time"`
}

// dialTimeout bounds how long a CONNECT tunnel waits to reach an allowed
// upstream, so a hung dial can't wedge a smoke run.
const dialTimeout = 10 * time.Second

// hopByHopHeaders are stripped before forwarding a plain-HTTP request or its
// response, per RFC 7230 6.1 -- they describe this hop's connection, not the
// one downstream.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

// proxy is the CONNECT allow-list egress proxy. It admits CONNECT tunnels
// and plain absolute-URI HTTP requests only to host:port pairs in allow,
// and logs every attempt -- admitted or denied -- to its log writer.
type proxy struct {
	// allow maps a normalized host to the ports it may be reached on; a
	// set holding anyPort admits every port.
	allow     map[string]map[int]struct{}
	log       io.Writer
	logMu     sync.Mutex
	transport http.RoundTripper
	// connectDefaultPort is the port a portless CONNECT authority resolves
	// to. It's "443" in production; tests override it (via a type assertion
	// back to *proxy, since NewProxy returns http.Handler) to point a
	// portless CONNECT at an httptest backend's ephemeral port, proving the
	// dial target matches the logged one instead of just asserting it.
	connectDefaultPort string
}

// anyPort is the port an allow-list entry with no ":port" admits: all of
// them, as an egress rule with no port means.
const anyPort = 0

// NewProxy returns an http.Handler that proxies CONNECT tunnels and plain
// absolute-URI HTTP requests only to what allow names, logging every
// attempt as one JSON line to log. An entry is a bare host, which admits
// any port, or host:port, which admits that port alone; hosts compare
// case-insensitively with a trailing dot stripped. An entry ParseAllow
// would refuse admits nothing, so a malformed list fails closed.
func NewProxy(allow []string, log io.Writer) http.Handler {
	p := &proxy{
		allow:              make(map[string]map[int]struct{}, len(allow)),
		log:                log,
		transport:          http.DefaultTransport,
		connectDefaultPort: "443",
	}
	for _, entry := range allow {
		host, port, err := parseAllowEntry(entry)
		if err != nil {
			continue
		}
		host = normalizeHost(host)
		if p.allow[host] == nil {
			p.allow[host] = map[int]struct{}{}
		}
		p.allow[host][port] = struct{}{}
	}
	return p
}

// ParseAllow checks every allow-list entry NewProxy will read, so the
// egress-proxy command can refuse a malformed list at start rather than
// run a proxy that silently admits less than it was asked to.
func ParseAllow(allow []string) error {
	for _, entry := range allow {
		if _, _, err := parseAllowEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

// parseAllowEntry splits host or host:port. A bare IPv6 literal, which
// net.SplitHostPort cannot split, is a host with any port; bracket it to
// name a port.
func parseAllowEntry(entry string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(entry)
	if err != nil {
		if strings.Count(entry, ":") > 1 || !strings.Contains(entry, ":") {
			if entry == "" {
				return "", 0, fmt.Errorf("egress allow-list: empty entry")
			}
			return entry, anyPort, nil
		}
		return "", 0, fmt.Errorf("egress allow-list: %q: %w", entry, err)
	}
	port, err := strconv.Atoi(portStr)
	if host == "" || err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("egress allow-list: %q is not host or host:port with a port from 1 to 65535", entry)
	}
	return host, port, nil
}

// normalizeHost applies the proxy's host-comparison rule: lowercase, and a
// trailing dot (a fully-qualified name) stripped.
func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func (p *proxy) isAllowed(host string, port int) bool {
	ports, ok := p.allow[normalizeHost(host)]
	if !ok {
		return false
	}
	_, anyOK := ports[anyPort]
	_, portOK := ports[port]
	return anyOK || portOK
}

// logAttempt writes one JSON line for the attempt. Writes are serialized so
// concurrent CONNECT and plain-HTTP handlers never interleave partial lines.
func (p *proxy) logAttempt(host string, port int, allowed bool) {
	line, err := json.Marshal(Attempt{
		Host:    host,
		Port:    port,
		Allowed: allowed,
		Time:    time.Now().UTC(),
	})
	if err != nil {
		return
	}
	line = append(line, '\n')
	p.logMu.Lock()
	defer p.logMu.Unlock()
	_, _ = p.log.Write(line)
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.serveConnect(w, r)
		return
	}
	p.servePlain(w, r)
}

// serveConnect handles `CONNECT host:port HTTP/1.1`: on an allowed host:port it
// dials the upstream, answers 200, then copies bytes both ways until either
// side closes. On a denied host it answers 403 and never dials.
func (p *proxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	host, port, err := splitHostPort(r.Host, p.connectDefaultPort)
	p.logAttempt(host, port, err == nil && p.isAllowed(host, port))
	if err != nil {
		http.Error(w, "malformed CONNECT target", http.StatusBadRequest)
		return
	}
	if !p.isAllowed(host, port) {
		http.Error(w, "host not allowed", http.StatusForbidden)
		return
	}

	// Dial the same normalized host:port the allow decision and log line
	// used, not the raw r.Host: a portless authority (bare "CONNECT
	// example.com") has no ":port" for net.Dialer to split, so dialing
	// r.Host directly fails even though the decision above defaulted the
	// port and logged allowed:true -- the log would then claim a connection
	// that never happened.
	dialCtx, cancel := context.WithTimeout(r.Context(), dialTimeout)
	defer cancel()
	var d net.Dialer
	upstream, err := d.DialContext(dialCtx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = upstream.Close() }()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	tunnel(client, upstream)
}

// tunnel copies bytes both ways between client and upstream until either
// side closes, half-closing writes so a client that stops writing doesn't
// wedge open the upstream's read half.
func tunnel(client, upstream net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, client)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		closeWrite(client)
	}()
	wg.Wait()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// servePlain handles a plain-HTTP absolute-URI request: forwarded through
// the proxy's transport with hop-by-hop headers stripped, on an allowed
// host. A non-absolute-URI request (one that didn't come from a client
// configured to use this proxy) gets 400. A denied host gets 403 and is
// never forwarded.
func (p *proxy) servePlain(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "absolute-URI required", http.StatusBadRequest)
		return
	}

	host := r.URL.Hostname()
	portStr := r.URL.Port()
	if portStr == "" {
		portStr = "80"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		http.Error(w, "malformed port", http.StatusBadRequest)
		return
	}

	allowed := p.isAllowed(host, port)
	p.logAttempt(host, port, allowed)
	if !allowed {
		http.Error(w, "host not allowed", http.StatusForbidden)
		return
	}

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	stripHopByHop(outReq.Header)

	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	stripHopByHop(resp.Header)
	dst := w.Header()
	for k, vv := range resp.Header {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func stripHopByHop(h http.Header) {
	for _, k := range hopByHopHeaders {
		h.Del(k)
	}
}

// splitHostPort splits an authority into host and numeric port, defaulting
// the port to defaultPort when authority carries none.
func splitHostPort(authority, defaultPort string) (host string, port int, err error) {
	h, p, err := net.SplitHostPort(authority)
	if err != nil {
		h = authority
		p = defaultPort
	}
	port, err = strconv.Atoi(p)
	if err != nil {
		return "", 0, err
	}
	return h, port, nil
}
