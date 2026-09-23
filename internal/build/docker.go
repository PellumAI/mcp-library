package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Paths inside the build container.
const (
	containerWork = "/build"
	containerSrc  = containerWork + "/src"
	runtimeRoot   = "/opt/mcpgw/runtimes"
)

// StepSpec is one build step.
type StepSpec struct {
	Image   string
	Work    string // the scratch tree on the host, bound at /build
	Argv    []string
	Arch    string
	Runtime string
	// Toolchains are extra runtime lines on PATH after the package's own.
	Toolchains []string
	Network    bool
}

// Runner runs one step inside the pinned build image. The production
// implementation is Docker; the test implementation records.
type Runner interface {
	Run(ctx context.Context, spec StepSpec) error
}

// Docker runs steps with the docker CLI.
type Docker struct {
	// Binary is the docker CLI. Empty means "docker" on PATH.
	Binary string
	Stdout io.Writer
	Stderr io.Writer
}

// Run implements Runner.
func (d Docker) Run(ctx context.Context, spec StepSpec) error {
	bin := d.Binary
	if bin == "" {
		bin = "docker"
	}
	cmd := exec.CommandContext(ctx, bin, DockerArgs(spec, os.Getuid(), os.Getgid())...)
	cmd.Stdout, cmd.Stderr = d.Stdout, d.Stderr
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stderr
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker run: %w", err)
	}
	return nil
}

// DockerArgs is the argv of one step's docker run. Every flag is
// load-bearing and the tests assert them field by field:
//
//   - --network none unless the step is a lockfile-enforcing installer;
//   - the caller's uid and gid, so the scratch tree stays deletable on the
//     host; ownership never reaches the tar, whose headers are normalised;
//   - SOURCE_DATE_EPOCH, TZ and LC_ALL fixed, so a toolchain that stamps a
//     time or sorts by locale does it the same way every run;
//   - GOFLAGS with -mod=readonly, CGO off, and GOOS and GOARCH for the one
//     architecture being built;
//   - PATH naming the ONE runtime line the recipe pins, first, so a step
//     resolves node or python3 from the tree the executor will bind;
//   - every cache and HOME inside the scratch tree, never on the host.
func DockerArgs(spec StepSpec, uid, gid int) []string {
	path := "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
	for i := len(spec.Toolchains) - 1; i >= 0; i-- {
		path = runtimeRoot + "/" + spec.Toolchains[i] + "/bin:" + path
	}
	if spec.Runtime != "" && spec.Runtime != "native" {
		path = runtimeRoot + "/" + spec.Runtime + "/bin:" + path
	}
	args := []string{"run", "--rm"}
	if !spec.Network {
		args = append(args, "--network", "none")
	}
	args = append(args,
		"--user", strconv.Itoa(uid)+":"+strconv.Itoa(gid),
		"--workdir", containerSrc,
		"--mount", "type=bind,src="+spec.Work+",dst="+containerWork,
	)
	env := []string{
		"SOURCE_DATE_EPOCH=0",
		"TZ=UTC",
		"LC_ALL=C",
		"HOME=" + containerWork + "/home",
		"GOTOOLCHAIN=local",
		"GOFLAGS=-mod=readonly -modcacherw -buildvcs=false",
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=" + spec.Arch,
		"GOMODCACHE=" + containerWork + "/cache/gomod",
		"GOCACHE=" + containerWork + "/cache/gobuild",
		"GOPATH=" + containerWork + "/cache/gopath",
		"PATH=" + path,
		"npm_config_cache=" + containerWork + "/cache/npm",
		"npm_config_update_notifier=false",
		"npm_config_fund=false",
		"npm_config_audit=false",
		"PIP_NO_COMPILE=1",
		"PIP_DISABLE_PIP_VERSION_CHECK=1",
		"PIP_CACHE_DIR=" + containerWork + "/cache/pip",
		"PYTHONDONTWRITEBYTECODE=1",
	}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	args = append(args, spec.Image)
	return append(args, spec.Argv...)
}

// String renders a step for a log line.
func (s StepSpec) String() string { return strings.Join(s.Argv, " ") }
