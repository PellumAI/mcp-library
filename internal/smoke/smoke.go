package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/pellumai/mcp-library/internal/build"
	"github.com/pellumai/mcp-library/internal/manifest"
)

// Modes a run is in, from the recipe's smoke.mode. The recipe spells full
// mode as the empty string; the report spells it out.
const (
	ModeFull           = "full"
	ModeInitializeOnly = "initialize-only"
)

// Verdicts.
const (
	VerdictPass = "pass"
	VerdictFail = "fail"
)

// SnapshotName is the committed tool surface under servers/<name>/.
const SnapshotName = "tools.snapshot.json"

// defaultInitializeTimeout applies when the manifest's
// health.initialize_timeout is absent: the schema requires the field, so a
// zero means a template that predates it, and 30s is generous rather than an
// immediate failure.
const defaultInitializeTimeout = 30 * time.Second

// The shutdown budget. A stdio server is told to exit by EOF on stdin, as
// the executor tells it, and gets stdinGrace to do so; then SIGTERM, and
// termGrace before docker kills it.
const (
	stdinGrace = 5 * time.Second
	termGrace  = 10 * time.Second
	// exitWait bounds how long smoke waits for a container docker has
	// already been told to kill, or that closed its session, to be reaped.
	exitWait = 30 * time.Second
)

// exitSIGTERM is docker run's status for a package that died of the
// SIGTERM smoke sent: 128 plus the signal, which --init's tini passes on.
const exitSIGTERM = 128 + int(syscall.SIGTERM)

// clientVersion is what smoke reports as its clientInfo.version.
const clientVersion = "1"

// Input is one smoke run.
type Input struct {
	// Tar is the package .tar.gz mcplib build wrote. It is unpacked, never
	// modified.
	Tar string
	// Name, when set, must equal the name in the tar's manifest, so a
	// run cannot compare one server's tools against another's snapshot.
	Name string
	// Mode is the recipe's smoke.mode: "" or ModeFull, or
	// ModeInitializeOnly.
	Mode string
	// Snapshot is the path of the committed tools.snapshot.json.
	Snapshot string
	// WriteSnapshot writes Snapshot from this run instead of comparing
	// against it. It writes only when every other check passes.
	WriteSnapshot bool
	// Image runs the package and the proxy: the pinned build image.
	Image string
	// MCPLib is a static linux mcplib binary for the proxy sidecar.
	MCPLib string
	Docker Docker
	// Log receives progress lines; nil discards them.
	Log func(format string, a ...any)
}

// Report is the smoke verdict, printed as JSON by mcplib smoke --json.
type Report struct {
	Verdict    string    `json:"verdict"` // pass | fail
	Failures   []string  `json:"failures"`
	Protocol   string    `json:"protocol_version"`
	InitMillis int64     `json:"initialize_ms"`
	ToolCount  int       `json:"tool_count"`
	Mode       string    `json:"mode"`
	Egress     []Attempt `json:"egress"`
	// Stderr is the tail of the package's stderr, for a human reading a
	// failure. It is not in the JSON: it can carry anything the package
	// printed, and the report is posted on issues.
	Stderr []byte `json:"-"`
}

func (r *Report) fail(format string, a ...any) {
	r.Failures = append(r.Failures, fmt.Sprintf(format, a...))
}

// Run smoke-tests one package tar and returns the verdict. An error means
// smoke itself could not run (no docker, an unreadable tar); a package that
// ran and misbehaved is a Report with verdict fail.
func Run(ctx context.Context, in Input) (Report, error) {
	logf := in.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	rep := Report{Failures: []string{}, Egress: []Attempt{}, Mode: ModeFull}
	switch in.Mode {
	case "", ModeFull:
	case ModeInitializeOnly:
		rep.Mode = ModeInitializeOnly
	default:
		return Report{}, fmt.Errorf("smoke: mode %q is neither full nor initialize-only", in.Mode)
	}

	// A short root: an http package's socket path must fit the ~108-byte
	// sun_path limit, which a deep $TMPDIR would exceed.
	work, err := os.MkdirTemp("", "mcplib-smoke-")
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	srv, logDir, sockDir := filepath.Join(work, "srv"), filepath.Join(work, "log"), filepath.Join(work, "sock")
	for _, d := range []string{srv, logDir, sockDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return Report{}, err
		}
	}
	// The package runs as uid 65534: it must read the tree and create its
	// socket, and os.Mkdir is subject to the umask.
	if err := os.Chmod(srv, 0o755); err != nil {
		return Report{}, err
	}
	if err := os.Chmod(sockDir, 0o777); err != nil {
		return Report{}, err
	}

	b, err := os.ReadFile(in.Tar)
	if err != nil {
		return Report{}, fmt.Errorf("smoke: %w", err)
	}
	if err := build.Unpack(b, srv); err != nil {
		return Report{}, fmt.Errorf("smoke: unpack %s: %w", in.Tar, err)
	}
	pkg, err := readPackage(srv)
	if err != nil {
		return Report{}, err
	}
	if in.Name != "" && pkg.doc.Name != in.Name {
		return Report{}, fmt.Errorf("smoke: %s holds package %q, not %q", in.Tar, pkg.doc.Name, in.Name)
	}
	allow, paramValues, err := egressHosts(pkg.rt)
	if err != nil {
		return Report{}, err
	}
	initTimeout := pkg.rt.InitializeTimeout
	if initTimeout <= 0 {
		initTimeout = defaultInitializeTimeout
	}

	st := &Stack{Docker: in.Docker, Image: in.Image, MCPLib: in.MCPLib, LogDir: logDir, Allow: allow}
	defer func() {
		if err := st.Down(ctx); err != nil {
			logf("%v", err)
		}
	}()
	logf("starting the egress proxy, allowing %v", allow)
	if err := st.Up(ctx); err != nil {
		return Report{}, err
	}
	spec := Spec{
		Image:       in.Image,
		SrvDir:      srv,
		Runtime:     pkg.doc.Runtime,
		Entrypoint:  pkg.doc.Entrypoint,
		Manifest:    pkg.rt,
		Env:         pkg.doc.Env,
		ParamValues: paramValues,
		Transport:   pkg.doc.Transport,
		SocketDir:   sockDir,
	}
	logf("running %s %s (%s, %s)", pkg.doc.Name, pkg.doc.Version, pkg.doc.Runtime, pkg.doc.Transport)
	started := time.Now()
	c, err := st.Start(ctx, spec)
	if err != nil {
		return Report{}, err
	}
	exited := make(chan int, 1)
	go func() { exited <- exitCode(c.Wait()) }()

	var snap []byte
	sess, sessionClosed := probe(ctx, c, pkg, filepath.Join(sockDir, filepath.Base(ListenSocket)), initTimeout, started, &rep, &snap)

	// The process must be alive until shutdown. A session that closed on
	// its own means it is exiting; give docker a moment to reap it.
	code, early := 0, false
	if sessionClosed {
		select {
		case code = <-exited:
			early = true
		case <-time.After(exitWait):
		}
	} else {
		select {
		case code = <-exited:
			early = true
		default:
		}
	}
	if early {
		rep.fail("the package exited before shutdown, with exit code %d", code)
	} else {
		shutdown(ctx, c, sess, pkg.doc.Transport, exited, &rep)
	}
	rep.Stderr = c.Stderr()

	attempts, err := readProxyLog(st.ProxyLog())
	if err != nil {
		return Report{}, err
	}
	rep.Egress = attempts
	for _, a := range attempts {
		if !a.Allowed {
			rep.fail("egress: denied %s:%d, which the manifest does not declare", a.Host, a.Port)
		}
	}

	if rep.Mode == ModeFull && snap != nil {
		checkSnapshot(in, snap, &rep, logf)
	}
	rep.Verdict = VerdictPass
	if len(rep.Failures) > 0 {
		rep.Verdict = VerdictFail
	}
	return rep, nil
}

// pkgInfo is the tar's rendered mcpgw-package.json. Every field smoke runs
// the package with comes from here and never from the template in
// servers/<name>/, because build renders into it what the template lacks,
// PYTHONPATH among it.
type pkgInfo struct {
	doc      manifest.Doc
	rt       manifest.Runtime
	endpoint string
}

func readPackage(srv string) (pkgInfo, error) {
	raw, err := os.ReadFile(filepath.Join(srv, manifest.PackageManifestName))
	if err != nil {
		return pkgInfo{}, fmt.Errorf("smoke: the tar has no %s: %w", manifest.PackageManifestName, err)
	}
	doc, err := manifest.Parse(raw)
	if err != nil {
		return pkgInfo{}, fmt.Errorf("smoke: %w", err)
	}
	rt, err := manifest.ParseRuntime(raw)
	if err != nil {
		return pkgInfo{}, fmt.Errorf("smoke: %w", err)
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return pkgInfo{}, fmt.Errorf("smoke: %w", err)
	}
	return pkgInfo{doc: doc, rt: rt, endpoint: p.Path}, nil
}

// hostTemplateRE is an egress host templated from a param's URL.
var hostTemplateRE = regexp.MustCompile(`^\$\{([A-Za-z0-9_]+)\.host\}$`)

// smokeDomain is the reserved TLD smoke points templated hosts at: the
// proxy admits the name, and the dial fails harmlessly, since .invalid
// never resolves.
const smokeDomain = ".smoke.invalid"

// egressHosts is the proxy's allow-list and the param values it implies. A
// host templated as ${<param>.host} becomes <param>.smoke.invalid, and the
// param is set to https://<param>.smoke.invalid so the package dials that
// host and nothing else.
func egressHosts(rt manifest.Runtime) ([]string, map[string]string, error) {
	allow := []string{}
	values := map[string]string{}
	for _, e := range rt.Egress {
		if !strings.Contains(e.Host, "${") {
			allow = append(allow, e.Host)
			continue
		}
		m := hostTemplateRE.FindStringSubmatch(e.Host)
		if m == nil {
			return nil, nil, fmt.Errorf("smoke: egress host %q is neither a host nor ${<param>.host}", e.Host)
		}
		if !slices.ContainsFunc(rt.Params, func(p manifest.Param) bool { return p.Name == m[1] }) {
			return nil, nil, fmt.Errorf("smoke: egress host %q names no param %q", e.Host, m[1])
		}
		host := m[1] + smokeDomain
		allow = append(allow, host)
		values[m[1]] = "https://" + host
	}
	return allow, values, nil
}

// probe runs the MCP conversation: initialize within the manifest's budget,
// then in full mode the listings. It records failures on rep and the tool
// snapshot in snap, and reports whether it saw the package end the session
// on its own, which means it is exiting.
func probe(ctx context.Context, c *Container, pkg pkgInfo, sock string, budget time.Duration, started time.Time, rep *Report, snap *[]byte) (Session, bool) {
	initCtx, cancel := context.WithDeadline(ctx, started.Add(budget))
	defer cancel()
	var sess Session
	if pkg.doc.Transport == TransportHTTP {
		if err := WaitSocket(initCtx, sock); err != nil {
			rep.fail("initialize: no socket within %v: %v", budget, err)
			return nil, false
		}
		s, err := DialSocketAt(sock, pkg.endpoint)
		if err != nil {
			rep.fail("initialize: %v", err)
			return nil, false
		}
		sess = s
	} else {
		sess = DialStdio(c.Stdout, c.Stdin)
	}

	res, err := sess.Call(initCtx, "initialize", map[string]any{
		"protocolVersion": BridgeProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mcplib-smoke", "version": clientVersion},
	})
	if err != nil {
		return sess, callFailed(initCtx, rep, "initialize", budget, err)
	}
	rep.InitMillis = time.Since(started).Milliseconds()
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Resources json.RawMessage `json:"resources"`
			Prompts   json.RawMessage `json:"prompts"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(res, &init); err != nil {
		rep.fail("initialize: the result does not decode: %v", err)
		return sess, false
	}
	rep.Protocol = init.ProtocolVersion
	if !slices.Contains(SupportedProtocolVersions, init.ProtocolVersion) {
		rep.fail("initialize: protocol version %q is outside the supported set %v", init.ProtocolVersion, SupportedProtocolVersions)
	}
	if err := sess.Notify(ctx, "notifications/initialized", nil); err != nil {
		rep.fail("notifications/initialized: %v", err)
	}
	if rep.Mode != ModeFull {
		return sess, false
	}

	tools, closed, ok := listTools(ctx, sess, budget, rep)
	if closed || !ok {
		return sess, closed
	}
	rep.ToolCount = len(tools)
	if len(tools) == 0 {
		rep.fail("tools/list: the package lists no tools")
	}
	schemaOK := true
	for _, t := range tools {
		if err := checkInputSchema(t.InputSchema); err != nil {
			schemaOK = false
			rep.fail("tools/list: tool %q: %v", t.Name, err)
		}
	}
	if schemaOK && len(tools) > 0 {
		b, err := Snapshot(tools)
		if err != nil {
			rep.fail("tools/list: snapshot: %v", err)
		} else {
			*snap = b
		}
	}
	for _, l := range []struct {
		method string
		cap    json.RawMessage
	}{{"resources/list", init.Capabilities.Resources}, {"prompts/list", init.Capabilities.Prompts}} {
		if len(l.cap) == 0 || string(l.cap) == "null" {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, budget)
		_, err := sess.Call(callCtx, l.method, map[string]any{})
		if err != nil && callFailed(callCtx, rep, l.method, budget, err) {
			cancel()
			return sess, true
		}
		cancel()
	}
	return sess, false
}

// callFailed records a failed call and reports whether the session closed
// under it.
func callFailed(ctx context.Context, rep *Report, method string, budget time.Duration, err error) bool {
	switch {
	case errors.Is(err, ErrSessionClosed):
		rep.fail("%s: the package closed the session before answering: %v", method, err)
		return true
	case ctx.Err() != nil:
		rep.fail("%s: no answer within %v", method, budget)
	default:
		rep.fail("%s: %v", method, err)
	}
	return false
}

// Tool is one tools/list entry as the snapshot records it.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// maxPages bounds tools/list pagination against a server whose cursor never
// ends.
const maxPages = 100

// listTools pages through tools/list. It reports whether the session closed
// and whether the listing completed.
func listTools(ctx context.Context, sess Session, budget time.Duration, rep *Report) ([]Tool, bool, bool) {
	var tools []Tool
	cursor := ""
	for range maxPages {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		callCtx, cancel := context.WithTimeout(ctx, budget)
		res, err := sess.Call(callCtx, "tools/list", params)
		if err != nil {
			closed := callFailed(callCtx, rep, "tools/list", budget, err)
			cancel()
			return nil, closed, false
		}
		cancel()
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &page); err != nil {
			rep.fail("tools/list: the result does not decode: %v", err)
			return nil, false, false
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" {
			return tools, false, true
		}
		cursor = page.NextCursor
	}
	rep.fail("tools/list: still paging after %d pages", maxPages)
	return nil, false, false
}

// refuseLoader loads no schema resource at all. A tool's input schema is
// judged on its own bytes: a $ref to a URL would have smoke fetch from the
// network, or read the host's files, on the package's say-so.
type refuseLoader struct{}

func (refuseLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("smoke loads no external schema, and the input schema refers to %s", url)
}

// checkInputSchema requires a tool's inputSchema to be a JSON Schema that
// compiles, with type "object", as the MCP Tool type requires.
func checkInputSchema(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return errors.New("no inputSchema")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("inputSchema is not JSON: %v", err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return errors.New("inputSchema is not a JSON object")
	}
	if obj["type"] != "object" {
		return fmt.Errorf("inputSchema type is %v, not \"object\"", obj["type"])
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(refuseLoader{})
	const url = "mcplib-smoke://tool/inputSchema.json"
	if err := c.AddResource(url, doc); err != nil {
		return fmt.Errorf("inputSchema is not a valid JSON Schema: %v", err)
	}
	if _, err := c.Compile(url); err != nil {
		return fmt.Errorf("inputSchema is not a valid JSON Schema: %v", err)
	}
	return nil
}

// Snapshot renders tools as tools.snapshot.json: sorted by name, each with
// name, description and inputSchema, indented two spaces, with a trailing
// newline. The schema is re-encoded through a generic value, so object keys
// come out sorted and a server that emits the same schema in a different
// key order does not read as drift. Numbers keep their exact text.
func Snapshot(tools []Tool) ([]byte, error) {
	type entry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema any    `json:"inputSchema"`
	}
	out := make([]entry, 0, len(tools))
	for _, t := range tools {
		dec := json.NewDecoder(bytes.NewReader(t.InputSchema))
		dec.UseNumber()
		var schema any
		if err := dec.Decode(&schema); err != nil {
			return nil, fmt.Errorf("tool %q: %w", t.Name, err)
		}
		out = append(out, entry{t.Name, t.Description, schema})
	}
	slices.SortStableFunc(out, func(a, b entry) int { return strings.Compare(a.Name, b.Name) })
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// checkSnapshot compares this run's snapshot with the committed one, or
// writes it when asked and every other check passed.
func checkSnapshot(in Input, snap []byte, rep *Report, logf func(string, ...any)) {
	if in.WriteSnapshot {
		if len(rep.Failures) > 0 {
			logf("not writing %s: the run failed", in.Snapshot)
			return
		}
		if err := os.WriteFile(in.Snapshot, snap, 0o644); err != nil {
			rep.fail("snapshot: %v", err)
			return
		}
		logf("wrote %s", in.Snapshot)
		return
	}
	committed, err := os.ReadFile(in.Snapshot)
	switch {
	case errors.Is(err, os.ErrNotExist):
		rep.fail("snapshot missing: %s does not exist; run mcplib smoke --write-snapshot and commit it", in.Snapshot)
	case err != nil:
		rep.fail("snapshot: %v", err)
	case !bytes.Equal(committed, snap):
		rep.fail("snapshot drift: this run's tool surface differs from %s; review the change, rerun with --write-snapshot and commit it", in.Snapshot)
	}
}

// shutdown ends a package that is still running and records whether it
// exited cleanly: exit 0, or death by the SIGTERM smoke sent. A stdio
// package first gets EOF on stdin and stdinGrace to act on it, as the
// executor stops one; an http package goes straight to SIGTERM.
func shutdown(ctx context.Context, c *Container, sess Session, transport string, exited <-chan int, rep *Report) {
	// For stdio, closing the session closes the package's stdin.
	if sess != nil {
		_ = sess.Close()
	}
	if transport == TransportStdio {
		select {
		case code := <-exited:
			if code != 0 {
				rep.fail("the package exited with code %d when its stdin closed", code)
			}
			return
		case <-time.After(stdinGrace):
		}
	}
	// A package that exits between the grace running out and the stop is
	// already gone, which is not a shutdown failure; its exit code below
	// still is judged.
	if err := c.Stop(ctx, termGrace); err != nil && !strings.Contains(err.Error(), "No such container") {
		rep.fail("shutdown: %v", err)
	}
	select {
	case code := <-exited:
		switch code {
		case 0, exitSIGTERM:
		case exitSIGKILL:
			rep.fail("the package did not exit within %v of SIGTERM", termGrace)
		default:
			rep.fail("the package exited with code %d on SIGTERM", code)
		}
	case <-time.After(exitWait):
		rep.fail("the package was still running %v after docker stop", exitWait)
	}
}

// exitSIGKILL is docker run's status for a package docker stop had to kill.
const exitSIGKILL = 128 + int(syscall.SIGKILL)

// exitCode is docker run's exit status from Wait's error: 0 for nil, the
// status for an exit error, and -1 when docker run itself failed to report
// one.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// readProxyLog reads the proxy's log, one Attempt per line. A missing log
// is no attempts: the proxy creates it on the first one.
func readProxyLog(path string) ([]Attempt, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Attempt{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("smoke: proxy log: %w", err)
	}
	out := []Attempt{}
	for line := range bytes.SplitSeq(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var a Attempt
		if err := json.Unmarshal(line, &a); err != nil {
			return nil, fmt.Errorf("smoke: proxy log line %q: %w", line, err)
		}
		out = append(out, a)
	}
	return out, nil
}
