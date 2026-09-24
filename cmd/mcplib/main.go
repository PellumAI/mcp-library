// Command mcplib is this repository's whole build surface: validate a recipe,
// build and pack a package, generate and sign the index, generate the site,
// and produce the fixture MCPGW's contract test consumes.
//
// Exit codes follow MCPGW's gatewayctl convention: 0 success, 1 a failure the
// command was asked to detect, 2 a usage error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
)

// errUsage marks a usage error, which exits 2 rather than 1.
var errUsage = errors.New("usage")

type command struct {
	summary string
	run     func(args []string, stdout, stderr io.Writer) error
	// hidden commands dispatch normally but usage leaves them off the list --
	// they're an implementation detail (smoke's sidecar entrypoint), not
	// something a person is meant to type.
	hidden bool
}

var commands = map[string]command{}

func register(name, summary string, run func(args []string, stdout, stderr io.Writer) error) {
	commands[name] = command{summary: summary, run: run}
}

func registerHidden(name, summary string, run func(args []string, stdout, stderr io.Writer) error) {
	commands[name] = command{summary: summary, run: run, hidden: true}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stderr)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	c, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "mcplib: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
	if err := c.run(args[1:], stdout, stderr); err != nil {
		if errors.Is(err, errUsage) || errors.Is(err, flag.ErrHelp) {
			if !errors.Is(err, flag.ErrHelp) {
				fmt.Fprintf(stderr, "mcplib %s: %v\n", args[0], err)
			}
			return 2
		}
		fmt.Fprintf(stderr, "mcplib %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: mcplib <command> [flags]")
	names := make([]string, 0, len(commands))
	for n, c := range commands {
		if c.hidden {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-12s %s\n", n, commands[n].summary)
	}
}

// newFlags returns a FlagSet whose parse errors are usage errors.
func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("mcplib "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func usageErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errUsage, fmt.Sprintf(format, a...))
}
