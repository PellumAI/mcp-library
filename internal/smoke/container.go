package smoke

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pellumai/mcp-library/internal/manifest"
)

// Paths and identities inside the package container. They mirror MCPGW's
// internal/mcpexecutor/sandbox/runner.go: the package tree read-only at /srv
// and the working directory, per-instance state at /state, which is also
// HOME, and /tmp for TMPDIR.
const (
	srvDir      = "/srv"
	stateDir    = "/state"
	tmpDir      = "/tmp"
	runtimeRoot = "/opt/mcpgw/runtimes"
	// socketDir is where an http package's socket directory is bound. The
	// executor puts the socket at /state/mcp.sock, but /state is a tmpfs
	// here, which the host cannot reach; a separate bind is the one way the
	// probe on the host can dial it. The package learns the path from
	// ListenSocketEnv either way, so it cannot tell the difference.
	socketDir = "/run/mcpgw"
	// ListenSocket is where an http-transport package listens.
	ListenSocket = socketDir + "/mcp.sock"
	// ListenSocketEnv names it for the package, as MCPGW's executor does.
	ListenSocketEnv = "MCPGW_LISTEN_SOCKET"
	// nobody is the non-root uid:gid the package runs as.
	nobody = "65534:65534"
	// proxyPort is where the egress proxy sidecar listens.
	proxyPort = "3128"
	// proxyLogName is the proxy's attempt log inside Stack.LogDir.
	proxyLogName = "proxy.jsonl"
)

// Transports a manifest may declare.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// controlEnv are the variables a package's manifest env may not set, the
// same list the executor keeps: they are how smoke steers the package, and
// a manifest setting HTTPS_PROXY must not route its traffic around the
// allow-list.
var controlEnv = []string{
	"PATH", "HOME", "TMPDIR", ListenSocketEnv,
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "ALL_PROXY", "all_proxy",
}

// Spec is one package container smoke runs.
type Spec struct {
	// Name is the container name, so Down can remove it by name.
	Name string
	// Image is the pinned build image, which carries the runtime window.
	Image string
	// SrvDir is the host directory holding the unpacked tar, bound
	// read-only at /srv. It must be absolute.
	SrvDir string
	// Runtime is the manifest's runtime line, such as node@22 or native.
	Runtime string
	// Entrypoint is the manifest's entrypoint argv, run from /srv.
	Entrypoint []string
	// Manifest is the tar's rendered mcpgw-package.json, parsed by
	// manifest.ParseRuntime: params, egress and resource limits.
	Manifest manifest.Runtime
	// Env is the manifest's env map.
	Env map[string]string
	// ParamValues overrides DummyValue for the params it names, by param
	// name. smoke uses it for a param an egress host is templated from,
	// which needs a URL whose host the proxy admits rather than "smoke".
	ParamValues map[string]string
	// Transport is stdio or http.
	Transport string
	// SocketDir is the host directory an http package's socket appears
	// in, bound at /run/mcpgw. The caller creates it mode 0777 so the
	// package's uid can create the socket in it. Unused for stdio.
	SocketDir string
	// Network is the internal Docker network the container joins, its
	// only network.
	Network string
	// ProxyAddr is the egress proxy's host:port on Network. Required when
	// the manifest declares egress.
	ProxyAddr string
}

// Validate refuses a spec ContainerArgs would turn into a container looser
// than the manifest asks for: a resource limit it cannot express is an
// error, never a dropped flag.
func (s Spec) Validate() error {
	var errs []error
	if s.Image == "" {
		errs = append(errs, errors.New("no image"))
	}
	if !filepath.IsAbs(s.SrvDir) {
		errs = append(errs, fmt.Errorf("package dir %q is not absolute", s.SrvDir))
	}
	if len(s.Entrypoint) == 0 {
		errs = append(errs, errors.New("the manifest has no entrypoint"))
	}
	switch s.Transport {
	case TransportStdio:
	case TransportHTTP:
		if !filepath.IsAbs(s.SocketDir) {
			errs = append(errs, fmt.Errorf("http transport needs an absolute socket dir, got %q", s.SocketDir))
		}
	default:
		errs = append(errs, fmt.Errorf("transport %q is neither stdio nor http", s.Transport))
	}
	if s.Network == "" {
		errs = append(errs, errors.New("no network"))
	}
	if len(s.Manifest.Egress) > 0 && s.ProxyAddr == "" {
		errs = append(errs, errors.New("the manifest declares egress but no proxy is set"))
	}
	if s.Manifest.Resources.PidsMax <= 0 {
		errs = append(errs, fmt.Errorf("resources.pids_max %d is not positive", s.Manifest.Resources.PidsMax))
	}
	if _, err := dockerMemory(s.Manifest.Resources.MemoryMax); err != nil {
		errs = append(errs, err)
	}
	if _, err := dockerCPUs(s.Manifest.Resources.CPUMax); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("smoke: container spec: %w", err)
	}
	return nil
}

// ContainerArgs is the argv of the package's docker run. It assumes
// spec.Validate passed. Every flag is load-bearing and the tests assert them
// field by field:
//
//   - --init, so the package is not PID 1: under the executor bwrap is the
//     namespace's init, and a PID 1 that installs no SIGTERM handler, which
//     is most servers, would ignore the SIGTERM smoke's shutdown sends, or,
//     for a Go binary, exit 2 instead of dying of it;
//   - --read-only root, the non-root uid, every capability dropped and
//     no-new-privileges, so the package can change nothing but its tmpfs;
//   - tmpfs /state and /tmp owned by that uid, the only writable paths;
//   - the unpacked tar bound read-only at /srv and the working directory,
//     as the executor binds its package cache;
//   - --memory, --cpus and --pids-limit from manifest.resources;
//   - the internal network only, with the proxy sidecar as HTTPS_PROXY and
//     HTTP_PROXY: a package that ignores them has no route at all;
//   - the environment in the executor's order: control variables first,
//     then the manifest env, then params, with control names dropped from
//     both, then the proxy variables last and unconditionally.
func ContainerArgs(spec Spec) []string {
	args := []string{"run", "--rm", "--init", "--name", spec.Name}
	if spec.Transport == TransportStdio {
		args = append(args, "-i")
	}
	tmpfsOpts := ":rw,nosuid,nodev,exec,uid=65534,gid=65534"
	args = append(args,
		"--network", spec.Network,
		"--read-only",
		"--user", nobody,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--tmpfs", stateDir+tmpfsOpts,
		"--tmpfs", tmpDir+tmpfsOpts,
		"--mount", "type=bind,src="+spec.SrvDir+",dst="+srvDir+",readonly",
		"--workdir", srvDir,
	)
	if spec.Transport == TransportHTTP {
		args = append(args, "--mount", "type=bind,src="+spec.SocketDir+",dst="+socketDir)
	}
	mem, _ := dockerMemory(spec.Manifest.Resources.MemoryMax)
	cpus, _ := dockerCPUs(spec.Manifest.Resources.CPUMax)
	args = append(args,
		"--memory", mem,
		"--cpus", cpus,
		"--pids-limit", strconv.Itoa(spec.Manifest.Resources.PidsMax),
	)
	for _, e := range containerEnv(spec) {
		args = append(args, "--env", e)
	}
	args = append(args, spec.Image)
	if spec.Transport == TransportHTTP {
		// The package creates its socket under the default umask, 022,
		// and a unix socket needs write permission to connect; the probe
		// on the host is neither the package's uid nor its group, so
		// without this it could never dial. umask is a shell builtin with
		// no docker run flag, hence the one-line wrapper.
		args = append(args, "/bin/sh", "-c", `umask 0000 && exec "$@"`, "mcplib-smoke")
	}
	return append(args, spec.Entrypoint...)
}

// containerEnv is the package's environment as NAME=value lines, ordered as
// ContainerArgs documents.
func containerEnv(spec Spec) []string {
	// PATH is the runtime line alone, as the executor sets it: bwrap binds
	// no system directories there, so a package that shells out to, say,
	// /usr/bin/git works here only by accident and must fail. A native
	// package gets an empty PATH. The image's root file system is still
	// present, which is what the http umask wrapper's absolute /bin/sh
	// relies on.
	path := ""
	if spec.Runtime != "" && spec.Runtime != "native" {
		path = runtimeRoot + "/" + spec.Runtime + "/bin"
	}
	names := []string{"PATH", "HOME", "TMPDIR", ListenSocketEnv}
	values := map[string]string{
		"PATH":          path,
		"HOME":          stateDir,
		"TMPDIR":        tmpDir,
		ListenSocketEnv: ListenSocket,
	}
	add := func(k, v string) {
		if k == "" || slices.Contains(controlEnv, k) {
			return
		}
		if _, ok := values[k]; !ok {
			names = append(names, k)
		}
		values[k] = v
	}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		add(k, spec.Env[k])
	}
	for _, p := range spec.Manifest.Params {
		if v, ok := spec.ParamValues[p.Name]; ok {
			add(p.Env, v)
		} else if v, ok := DummyValue(p); ok {
			add(p.Env, v)
		}
	}
	out := make([]string, 0, len(names)+6)
	for _, k := range names {
		out = append(out, k+"="+values[k])
	}
	if spec.ProxyAddr != "" {
		u := "http://" + spec.ProxyAddr
		out = append(out,
			"HTTPS_PROXY="+u, "HTTP_PROXY="+u, "https_proxy="+u, "http_proxy="+u,
			"NO_PROXY=", "no_proxy=",
		)
	}
	return out
}

// DummyValue is what smoke sets a param to, and whether it sets it at all: a
// secret gets smoke-dummy-<name>, so a leak in a log is recognisable; a
// required non-secret param gets smoke; an optional non-secret param stays
// unset, so the package runs on its own default.
func DummyValue(p manifest.Param) (string, bool) {
	switch {
	case p.Secret:
		return "smoke-dummy-" + p.Name, true
	case p.Required:
		return "smoke", true
	default:
		return "", false
	}
}

// dockerMemory converts a cgroup-style memory_max (bytes, or a Ki, Mi or Gi
// suffix) into docker's --memory unit, which spells the binary suffixes k, m
// and g.
func dockerMemory(v string) (string, error) {
	num, unit := v, ""
	for _, suf := range []string{"Ki", "Mi", "Gi"} {
		if n, ok := strings.CutSuffix(v, suf); ok {
			num, unit = n, strings.ToLower(suf[:1])
			break
		}
	}
	n, err := strconv.ParseUint(num, 10, 64)
	if err != nil || n == 0 {
		return "", fmt.Errorf("resources.memory_max %q is not a positive byte count with an optional Ki, Mi or Gi suffix", v)
	}
	return strconv.FormatUint(n, 10) + unit, nil
}

// dockerCPUs converts a cgroup cpu.max "<quota> <period>" into docker's
// --cpus, quota over period with two decimals. "max" has no --cpus
// equivalent and is refused rather than run unlimited.
func dockerCPUs(v string) (string, error) {
	fields := strings.Fields(v)
	if len(fields) == 2 {
		q, err1 := strconv.ParseUint(fields[0], 10, 64)
		p, err2 := strconv.ParseUint(fields[1], 10, 64)
		if err1 == nil && err2 == nil && q > 0 && p > 0 {
			cpus := strconv.FormatFloat(float64(q)/float64(p), 'f', 2, 64)
			if cpus == "0.00" {
				// docker reads --cpus 0 as no limit at all, the opposite
				// of the tiny share the manifest asks for.
				return "", fmt.Errorf("resources.cpu_max %q is under 0.01 CPU, which docker's --cpus cannot express", v)
			}
			return cpus, nil
		}
	}
	return "", fmt.Errorf("resources.cpu_max %q is not \"<quota> <period>\" with both positive", v)
}

// Docker runs the docker CLI.
type Docker struct {
	// Binary is the docker CLI. Empty means "docker" on PATH.
	Binary string
}

func (d Docker) bin() string {
	if d.Binary == "" {
		return "docker"
	}
	return d.Binary
}

// run runs one docker command to completion and returns its trimmed stdout.
// stderr is folded into the error, since a docker failure explains itself
// there.
func (d Docker) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// logs returns a container's combined stdout and stderr, trimmed.
func (d Docker) logs(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, d.bin(), "logs", name).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker logs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Stack is one smoke run's Docker resources: an internal network, the egress
// proxy sidecar on it and on the default bridge, and the package container on
// the internal network only. The package's only reachable peer is the proxy.
//
// Always defer Down, before calling Up: Down removes whatever Up and Start
// created, including after a partial Up, and is safe to call twice.
type Stack struct {
	Docker Docker
	// Image runs the proxy. The build image serves: the proxy is the static
	// mcplib binary, which needs nothing from the image but a kernel.
	Image string
	// MCPLib is the host path to a static linux mcplib binary, bound
	// read-only into the proxy container.
	MCPLib string
	// LogDir is the host directory the proxy writes proxy.jsonl into.
	LogDir string
	// Allow is the hosts the proxy admits.
	Allow []string

	network   string
	netMade   bool
	proxyMade bool
	pkgs      []string
}

// Network is the internal network's name, set by Up.
func (s *Stack) Network() string { return s.network }

// ProxyAddr is the proxy's host:port on the internal network, set by Up.
func (s *Stack) ProxyAddr() string { return s.proxyName() + ":" + proxyPort }

// ProxyLog is the host path of the proxy's attempt log, one Attempt per line.
func (s *Stack) ProxyLog() string { return filepath.Join(s.LogDir, proxyLogName) }

func (s *Stack) proxyName() string { return s.network + "-proxy" }

// Up creates the internal network and starts the proxy on it and on the
// default bridge. It returns once the proxy container is running.
func (s *Stack) Up(ctx context.Context) error {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("smoke: network name: %w", err)
	}
	s.network = "mcplib-smoke-" + hex.EncodeToString(b[:])
	if _, err := s.Docker.run(ctx, "network", "create", "--internal", s.network); err != nil {
		return fmt.Errorf("smoke: %w", err)
	}
	s.netMade = true
	// Marked before the run: a docker run that fails after creating the
	// container still leaves one for Down to remove.
	s.proxyMade = true
	if _, err := s.Docker.run(ctx, s.proxyArgs(os.Getuid(), os.Getgid())...); err != nil {
		return fmt.Errorf("smoke: start proxy: %w", err)
	}
	if _, err := s.Docker.run(ctx, "network", "connect", "bridge", s.proxyName()); err != nil {
		return fmt.Errorf("smoke: proxy egress: %w", err)
	}
	return s.waitRunning(ctx, s.proxyName())
}

// proxyArgs is the proxy sidecar's docker run. It runs as the caller's uid
// and gid so the log it writes into LogDir stays readable and deletable on
// the host, and is otherwise as locked down as the package. It has no --rm:
// a proxy that exits at once must leave its container behind so waitRunning
// can report its logs; Down removes it.
func (s *Stack) proxyArgs(uid, gid int) []string {
	return []string{
		"run", "-d",
		"--name", s.proxyName(),
		"--network", s.network,
		"--read-only",
		"--user", strconv.Itoa(uid) + ":" + strconv.Itoa(gid),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--mount", "type=bind,src=" + s.MCPLib + ",dst=/mcplib,readonly",
		"--mount", "type=bind,src=" + s.LogDir + ",dst=/log",
		s.Image,
		"/mcplib", "egress-proxy",
		"--listen", ":" + proxyPort,
		"--allow", strings.Join(s.Allow, ","),
		"--log", "/log/" + proxyLogName,
	}
}

// waitRunning polls until name is running. Go's listener is up within
// milliseconds of the process starting, so running is close enough to
// listening, and a package that dials before then retries like any client.
// A container that has already exited will never run, so that fails at once
// with its logs, which say why.
func (s *Stack) waitRunning(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		out, err := s.Docker.run(ctx, "inspect", "-f", "{{.State.Status}}", name)
		if err == nil {
			switch out {
			case "running":
				return nil
			case "exited", "dead":
				logs, lerr := s.Docker.logs(ctx, name)
				if lerr != nil {
					logs = lerr.Error()
				}
				return fmt.Errorf("smoke: %s exited before it was ready: %s", name, logs)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("smoke: %s did not start: %w", name, errors.Join(ctx.Err(), err))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Spec fills spec's Network, ProxyAddr and, if empty, Name from the stack.
func (s *Stack) Spec(spec Spec) Spec {
	spec.Network = s.network
	spec.ProxyAddr = s.ProxyAddr()
	if spec.Name == "" {
		spec.Name = s.network + "-pkg"
	}
	return spec
}

// stderrKeep is how much of the package's stderr a Container keeps.
const stderrKeep = 64 << 10

// Container is a running package container. For stdio, Stdin and Stdout
// are the package's; for http they are nil and the package is reached over
// its socket. There is no stderr pipe to read: Start drains stderr itself,
// because an undrained pipe fills at 64 KiB and blocks a chatty package,
// which the probe would then misreport as a timeout. Stderr returns the
// tail.
type Container struct {
	Name   string
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	stderr *tailBuffer
	cmd    *exec.Cmd
	docker Docker
}

// Stderr is the last 64 KiB the package wrote to stderr, for a failure
// report. It is complete once Wait has returned.
func (c *Container) Stderr() []byte { return c.stderr.Bytes() }

// Wait returns when the container exits, with docker run's exit status,
// which is the package's. Call it exactly once, after reading Stdout to EOF
// or abandoning it.
func (c *Container) Wait() error { return c.cmd.Wait() }

// tailBuffer is an io.Writer that keeps only the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return n, nil
	}
	if over := len(b.buf) + len(p) - b.max; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

func (b *tailBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.buf)
}

// Stop asks the package to exit with SIGTERM, then kills it after grace.
func (c *Container) Stop(ctx context.Context, grace time.Duration) error {
	_, err := c.docker.run(ctx, "stop", "-t", strconv.Itoa(int(grace/time.Second)), c.Name)
	return err
}

// Start runs the package container, attached, in the stack. spec is passed
// through Stack.Spec and Validate first.
func (s *Stack) Start(ctx context.Context, spec Spec) (*Container, error) {
	spec = s.Spec(spec)
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, s.Docker.bin(), ContainerArgs(spec)...)
	c := &Container{Name: spec.Name, cmd: cmd, docker: s.Docker, stderr: &tailBuffer{max: stderrKeep}}
	// A non-*os.File writer makes os/exec copy stderr from its own
	// goroutine for the container's whole life, and Wait waits for that
	// copy, so the drain can never stall.
	cmd.Stderr = c.stderr
	var err1, err2 error
	if spec.Transport == TransportStdio {
		c.Stdin, err1 = cmd.StdinPipe()
		c.Stdout, err2 = cmd.StdoutPipe()
	}
	if err := errors.Join(err1, err2); err != nil {
		return nil, fmt.Errorf("smoke: pipes: %w", err)
	}
	s.pkgs = append(s.pkgs, spec.Name)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("smoke: start package: %w", err)
	}
	return c, nil
}

// Down removes every container Start ran, the proxy and the network. It
// ignores ctx's cancellation, since it is the cleanup a cancelled run needs
// most, and it is idempotent: what is already gone is not an error.
func (s *Stack) Down(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	var errs []error
	// rm reports whether the thing is gone, so a failed removal stays
	// recorded and a second Down retries it.
	rm := func(args ...string) bool {
		_, err := s.Docker.run(ctx, args...)
		if err == nil || strings.Contains(err.Error(), "No such") || strings.Contains(err.Error(), "not found") {
			return true
		}
		errs = append(errs, err)
		return false
	}
	var left []string
	for _, name := range s.pkgs {
		if !rm("rm", "-f", name) {
			left = append(left, name)
		}
	}
	s.pkgs = left
	if s.proxyMade && rm("rm", "-f", s.proxyName()) {
		s.proxyMade = false
	}
	if s.netMade && rm("network", "rm", s.network) {
		s.netMade = false
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("smoke: down: %w", err)
	}
	return nil
}
