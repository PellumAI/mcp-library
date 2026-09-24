// Package audit collects deterministic supply-chain evidence for a server:
// its locked dependencies, their known vulnerabilities, and their licences.
// `mcplib audit` (a later task) composes these leaf pieces into a report.
package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dep is one locked dependency, resolved to an exact version.
type Dep struct {
	Ecosystem     string `json:"ecosystem"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	InstallScript bool   `json:"install_script"`
}

// ReadLock reads a lockfile, dispatching on its base name: package-lock.json
// for node, requirements.txt for python, go.sum for native Go. Deps come
// back sorted by name then version, so the same lockfile always reads the
// same way.
func ReadLock(path string) ([]Dep, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("audit: read lockfile %s: %w", path, err)
	}

	var deps []Dep
	switch filepath.Base(path) {
	case "package-lock.json":
		deps, err = readNPMLock(data)
	case "requirements.txt":
		deps, err = readRequirements(data)
	case "go.sum":
		deps, err = readGoSum(data)
	default:
		return nil, fmt.Errorf("audit: unknown lockfile %s", path)
	}
	if err != nil {
		return nil, err
	}

	sort.Slice(deps, func(i, j int) bool {
		if deps[i].Name != deps[j].Name {
			return deps[i].Name < deps[j].Name
		}
		return deps[i].Version < deps[j].Version
	})
	return deps, nil
}

// npmLockFile is the handful of package-lock.json v3 fields ReadLock needs.
type npmLockFile struct {
	Packages map[string]npmPackageEntry `json:"packages"`
}

type npmPackageEntry struct {
	Version          string `json:"version"`
	HasInstallScript bool   `json:"hasInstallScript"`
}

// readNPMLock reads a package-lock.json v3 tree. Its "packages" map keys
// every installed package by its node_modules path, including the root
// project under the empty-string key, which this skips.
func readNPMLock(data []byte) ([]Dep, error) {
	var lf npmLockFile
	if err := json.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("audit: parse package-lock.json: %w", err)
	}

	deps := make([]Dep, 0, len(lf.Packages))
	for key, pkg := range lf.Packages {
		if key == "" {
			continue // the root project itself, not a locked dependency
		}
		deps = append(deps, Dep{
			Ecosystem:     "npm",
			Name:          npmPackageName(key),
			Version:       pkg.Version,
			InstallScript: pkg.HasInstallScript,
		})
	}
	return deps, nil
}

// npmPackageName recovers a package's name from its node_modules path,
// including nested paths like node_modules/a/node_modules/@scope/b, whose
// name is everything after the last node_modules/ segment.
func npmPackageName(key string) string {
	const marker = "node_modules/"
	idx := strings.LastIndex(key, marker)
	if idx == -1 {
		return key
	}
	return key[idx+len(marker):]
}

// readRequirements reads a pip-compile --generate-hashes requirements.txt:
// "name==version \" lines followed by one or more "--hash=..." continuation
// lines. A requirement with no "==" pin is an error, since an unpinned
// dependency can't be locked to an exact, audited version.
func readRequirements(data []byte) ([]Dep, error) {
	var deps []Dep
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "--hash") {
			continue
		}
		line = strings.TrimSuffix(line, "\\")
		line = strings.TrimSpace(line)

		name, version, ok := strings.Cut(line, "==")
		if !ok {
			return nil, fmt.Errorf("audit: unpinned requirement %q", line)
		}
		deps = append(deps, Dep{
			Ecosystem: "PyPI",
			Name:      strings.TrimSpace(name),
			Version:   strings.TrimSpace(version),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("audit: read requirements.txt: %w", err)
	}
	return deps, nil
}

// readGoSum reads a go.sum: "module version hash" lines, one pair of lines
// per module version (the plain line and a "version/go.mod" line for the
// go.mod hash alone). The /go.mod lines are skipped since they name no
// separate dependency.
func readGoSum(data []byte) ([]Dep, error) {
	var deps []Dep
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		module, version := fields[0], fields[1]
		if strings.HasSuffix(version, "/go.mod") {
			continue
		}
		deps = append(deps, Dep{
			Ecosystem: "Go",
			Name:      module,
			Version:   version,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("audit: read go.sum: %w", err)
	}
	return deps, nil
}
