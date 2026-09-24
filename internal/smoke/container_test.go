package smoke

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/manifest"
)

// context7Spec builds a Spec from the real servers/context7/manifest.json, so
// a manifest change that moves a limit shows up here rather than in a docker
// run.
func context7Spec(t *testing.T) Spec {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "servers", "context7", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := manifest.ParseRuntime(raw)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Runtime    string            `json:"runtime"`
		Entrypoint []string          `json:"entrypoint"`
		Transport  string            `json:"transport"`
		Env        map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return Spec{
		Name:       "mcplib-smoke-abc-pkg",
		Image:      "ghcr.io/pellumai/mcp-library/build@sha256:0000",
		SrvDir:     "/work/srv",
		Runtime:    m.Runtime,
		Entrypoint: m.Entrypoint,
		Manifest:   rt,
		Env:        m.Env,
		Transport:  m.Transport,
		Network:    "mcplib-smoke-abc",
		ProxyAddr:  "mcplib-smoke-abc-proxy:3128",
	}
}

// flagValue returns the value following the first occurrence of flag.
func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("%s missing from %q", flag, args)
	}
	return args[i+1]
}

// flagValues returns every value following an occurrence of flag.
func flagValues(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func envMap(args []string) map[string]string {
	m := map[string]string{}
	for _, e := range flagValues(args, "--env") {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}

func TestContainerArgs_Context7(t *testing.T) {
	spec := context7Spec(t)
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	args := ContainerArgs(spec)

	if args[0] != "run" || !slices.Contains(args, "--rm") {
		t.Errorf("want run --rm, got %q", args[:2])
	}
	if !slices.Contains(args, "-i") {
		t.Error("stdio transport needs -i")
	}
	for _, f := range []string{"--read-only"} {
		if !slices.Contains(args, f) {
			t.Errorf("%s missing", f)
		}
	}
	for flag, want := range map[string]string{
		"--name":         "mcplib-smoke-abc-pkg",
		"--user":         "65534:65534",
		"--cap-drop":     "ALL",
		"--security-opt": "no-new-privileges",
		"--network":      "mcplib-smoke-abc",
		"--workdir":      "/srv",
		"--memory":       "384m",
		"--cpus":         "1.00",
		"--pids-limit":   "64",
	} {
		if got := flagValue(t, args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	tmpfs := flagValues(args, "--tmpfs")
	for _, want := range []string{
		"/state:rw,nosuid,nodev,exec,uid=65534,gid=65534",
		"/tmp:rw,nosuid,nodev,exec,uid=65534,gid=65534",
	} {
		if !slices.Contains(tmpfs, want) {
			t.Errorf("tmpfs %q missing from %q", want, tmpfs)
		}
	}
	mounts := flagValues(args, "--mount")
	if want := "type=bind,src=/work/srv,dst=/srv,readonly"; !slices.Equal(mounts, []string{want}) {
		t.Errorf("mounts = %q, want only %q", mounts, want)
	}

	env := envMap(args)
	for k, want := range map[string]string{
		"PATH":                "/opt/mcpgw/runtimes/node@22/bin:/usr/local/bin:/usr/bin:/bin",
		"HOME":                "/state",
		"TMPDIR":              "/tmp",
		"MCPGW_LISTEN_SOCKET": "/run/mcpgw/mcp.sock",
		"NODE_ENV":            "production",
		"CONTEXT7_API_KEY":    "smoke-dummy-api_key",
		"HTTPS_PROXY":         "http://mcplib-smoke-abc-proxy:3128",
		"HTTP_PROXY":          "http://mcplib-smoke-abc-proxy:3128",
		"https_proxy":         "http://mcplib-smoke-abc-proxy:3128",
		"http_proxy":          "http://mcplib-smoke-abc-proxy:3128",
		"NO_PROXY":            "",
		"no_proxy":            "",
	} {
		if got, ok := env[k]; !ok || got != want {
			t.Errorf("env %s = %q (present %v), want %q", k, got, ok, want)
		}
	}
	// PATH is the first env, as the executor orders it.
	if first := flagValues(args, "--env")[0]; !strings.HasPrefix(first, "PATH=") {
		t.Errorf("first env = %q, want PATH", first)
	}

	// The image, then the entrypoint, end the argv.
	tail := args[len(args)-3:]
	want := []string{spec.Image, "node", "node_modules/@upstash/context7-mcp/dist/index.js"}
	if !slices.Equal(tail, want) {
		t.Errorf("tail = %q, want %q", tail, want)
	}
}

func TestContainerArgs_ControlEnvNotShadowed(t *testing.T) {
	spec := context7Spec(t)
	spec.Env = map[string]string{"PATH": "/evil", "HTTPS_PROXY": "http://evil", "FOO": "bar"}
	env := envMap(ContainerArgs(spec))
	if env["PATH"] != "/opt/mcpgw/runtimes/node@22/bin:/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("PATH shadowed: %q", env["PATH"])
	}
	if env["HTTPS_PROXY"] != "http://mcplib-smoke-abc-proxy:3128" {
		t.Errorf("HTTPS_PROXY shadowed: %q", env["HTTPS_PROXY"])
	}
	if env["FOO"] != "bar" {
		t.Errorf("FOO = %q", env["FOO"])
	}
}

func TestContainerArgs_Params(t *testing.T) {
	spec := context7Spec(t)
	spec.Manifest.Params = []manifest.Param{
		{Name: "token", Env: "TOK", Secret: true},
		{Name: "host", Env: "HOST", Required: true},
		{Name: "opt", Env: "OPT"},
	}
	env := envMap(ContainerArgs(spec))
	if env["TOK"] != "smoke-dummy-token" || env["HOST"] != "smoke" {
		t.Errorf("params env = %v", env)
	}
	if _, ok := env["OPT"]; ok {
		t.Error("an optional non-secret param must stay unset")
	}
}

func TestContainerArgs_HTTPAndNative(t *testing.T) {
	spec := context7Spec(t)
	spec.Transport = "http"
	spec.SocketDir = "/work/sock"
	spec.Runtime = "native"
	spec.Entrypoint = []string{"/srv/bin/server"}
	args := ContainerArgs(spec)
	if slices.Contains(args, "-i") {
		t.Error("http transport must not attach stdin")
	}
	if mounts := flagValues(args, "--mount"); !slices.Contains(mounts, "type=bind,src=/work/sock,dst=/run/mcpgw") {
		t.Errorf("socket dir not bound: %q", mounts)
	}
	if got := envMap(args)["PATH"]; got != "/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("native PATH = %q", got)
	}
	tail := args[slices.Index(args, spec.Image):]
	want := []string{spec.Image, "/bin/sh", "-c", `umask 0000 && exec "$@"`, "mcplib-smoke", "/srv/bin/server"}
	if !slices.Equal(tail, want) {
		t.Errorf("tail = %q, want %q", tail, want)
	}
}

func TestDockerMemory(t *testing.T) {
	for in, want := range map[string]string{
		"384Mi": "384m", "512Ki": "512k", "2Gi": "2g", "1048576": "1048576",
	} {
		got, err := dockerMemory(in)
		if err != nil || got != want {
			t.Errorf("dockerMemory(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "max", "12Xi", "Mi", "-1Mi"} {
		if _, err := dockerMemory(bad); err == nil {
			t.Errorf("dockerMemory(%q) accepted", bad)
		}
	}
}

func TestDockerCPUs(t *testing.T) {
	for in, want := range map[string]string{
		"100000 100000": "1.00", "50000 100000": "0.50", "200000 100000": "2.00", "33333 100000": "0.33",
	} {
		got, err := dockerCPUs(in)
		if err != nil || got != want {
			t.Errorf("dockerCPUs(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "max 100000", "100000", "1 0", "a b"} {
		if _, err := dockerCPUs(bad); err == nil {
			t.Errorf("dockerCPUs(%q) accepted", bad)
		}
	}
}

func TestSpecValidate(t *testing.T) {
	base := context7Spec(t)
	for name, mut := range map[string]func(*Spec){
		"no image":      func(s *Spec) { s.Image = "" },
		"relative srv":  func(s *Spec) { s.SrvDir = "srv" },
		"no entrypoint": func(s *Spec) { s.Entrypoint = nil },
		"bad transport": func(s *Spec) { s.Transport = "sse" },
		"http no sock":  func(s *Spec) { s.Transport = "http" },
		"no network":    func(s *Spec) { s.Network = "" },
		"no pids":       func(s *Spec) { s.Manifest.Resources.PidsMax = 0 },
		"bad memory":    func(s *Spec) { s.Manifest.Resources.MemoryMax = "max" },
		"bad cpu":       func(s *Spec) { s.Manifest.Resources.CPUMax = "max 100000" },
		"egress no proxy": func(s *Spec) {
			s.ProxyAddr = ""
		},
	} {
		s := base
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate accepted", name)
		}
	}
}

func TestProxyArgs(t *testing.T) {
	st := &Stack{Image: "img", MCPLib: "/host/mcplib", LogDir: "/host/log", Allow: []string{"a.com", "b.com"}}
	st.network = "mcplib-smoke-abc"
	args := st.proxyArgs(1000, 1000)
	for flag, want := range map[string]string{
		"--name":         "mcplib-smoke-abc-proxy",
		"--network":      "mcplib-smoke-abc",
		"--user":         "1000:1000",
		"--cap-drop":     "ALL",
		"--security-opt": "no-new-privileges",
	} {
		if got := flagValue(t, args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	if !slices.Contains(args, "-d") || !slices.Contains(args, "--read-only") {
		t.Errorf("want -d and --read-only: %q", args)
	}
	mounts := flagValues(args, "--mount")
	for _, want := range []string{
		"type=bind,src=/host/mcplib,dst=/mcplib,readonly",
		"type=bind,src=/host/log,dst=/log",
	} {
		if !slices.Contains(mounts, want) {
			t.Errorf("mount %q missing from %q", want, mounts)
		}
	}
	tail := args[slices.Index(args, "img"):]
	want := []string{"img", "/mcplib", "egress-proxy", "--listen", ":3128", "--allow", "a.com,b.com", "--log", "/log/proxy.jsonl"}
	if !slices.Equal(tail, want) {
		t.Errorf("tail = %q, want %q", tail, want)
	}
	if st.ProxyAddr() != "mcplib-smoke-abc-proxy:3128" {
		t.Errorf("ProxyAddr = %q", st.ProxyAddr())
	}
}

// fakeDocker writes a docker stand-in that logs each argv as one line and
// fails any command whose argv contains $FAKE_DOCKER_FAIL. It is a shell
// script, not docker: the lifecycle's ordering and cleanup are testable
// without a daemon, and Task 7's integration tests cover the real one.
func fakeDocker(t *testing.T, fail string) (Docker, func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "docker")
	body := "#!/bin/sh\n" +
		"echo \"$*\" >> " + log + "\n" +
		"case \"$*\" in\n" +
		"  *'" + fail + "'*) echo 'boom' >&2; exit 1 ;;\n" +
		"  inspect*) echo true ;;\n" +
		"esac\n"
	if fail == "" {
		body = strings.Replace(body, "  *''*) echo 'boom' >&2; exit 1 ;;\n", "", 1)
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return Docker{Binary: script}, func() []string {
		b, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

func TestStack_UpDown(t *testing.T) {
	d, calls := fakeDocker(t, "")
	st := &Stack{Docker: d, Image: "img", MCPLib: "/host/mcplib", LogDir: "/host/log"}
	ctx := context.Background()
	if err := st.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	net := st.Network()
	if !strings.HasPrefix(net, "mcplib-smoke-") {
		t.Fatalf("network = %q", net)
	}
	c, err := st.Start(ctx, context7Spec(t))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = c.Stdin.Close()
	_, _ = io.Copy(io.Discard, c.Stdout)
	_, _ = io.Copy(io.Discard, c.Stderr)
	if err := c.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if c.Name != "mcplib-smoke-abc-pkg" {
		t.Errorf("Start kept spec.Name: %q", c.Name)
	}
	if err := st.Down(ctx); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if err := st.Down(ctx); err != nil {
		t.Fatalf("second Down: %v", err)
	}
	got := calls()
	want := []string{
		"network create --internal " + net,
		"run -d --rm --name " + net + "-proxy",
		"network connect bridge " + net + "-proxy",
		"inspect -f {{.State.Running}} " + net + "-proxy",
		"run --rm --name mcplib-smoke-abc-pkg -i --network " + net,
		"rm -f mcplib-smoke-abc-pkg",
		"rm -f " + net + "-proxy",
		"network rm " + net,
	}
	if len(got) != len(want) {
		t.Fatalf("calls = %q, want %d", got, len(want))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("call %d = %q, want prefix %q", i, got[i], want[i])
		}
	}
}

func TestStack_DownAfterFailedUp(t *testing.T) {
	d, calls := fakeDocker(t, "network connect")
	st := &Stack{Docker: d, Image: "img", MCPLib: "/m", LogDir: "/l"}
	if err := st.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded with a failing network connect")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Down must still clean up after a cancelled run.
	if err := st.Down(ctx); err != nil {
		t.Fatalf("Down: %v", err)
	}
	got := calls()
	if n := len(got); n < 2 || got[n-2] != "rm -f "+st.Network()+"-proxy" || got[n-1] != "network rm "+st.Network() {
		t.Errorf("Down did not remove the proxy and network: %q", got)
	}
}

func TestStack_DownRetriesFailedRemoval(t *testing.T) {
	d, calls := fakeDocker(t, "network rm")
	st := &Stack{Docker: d, Image: "img", MCPLib: "/m", LogDir: "/l"}
	if err := st.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := st.Down(context.Background()); err == nil {
		t.Fatal("Down hid a failed network rm")
	}
	_ = st.Down(context.Background())
	var rms int
	for _, c := range calls() {
		if strings.HasPrefix(c, "network rm") {
			rms++
		}
	}
	if rms != 2 {
		t.Errorf("network rm ran %d times, want a retry on the second Down", rms)
	}
}
