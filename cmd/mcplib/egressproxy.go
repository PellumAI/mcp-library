package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pellumai/mcp-library/internal/smoke"
)

// shutdownGrace bounds how long egress-proxy waits for in-flight tunnels to
// drain after SIGTERM/SIGINT before giving up.
const shutdownGrace = 5 * time.Second

func init() {
	registerHidden("egress-proxy", "run the CONNECT allow-list egress proxy smoke's sidecar uses", cmdEgressProxy)
}

// cmdEgressProxy is `mcplib egress-proxy --listen :3128 --allow a,b --log
// /log/proxy.jsonl`. smoke launches it as a sidecar container on the
// package's internal Docker network; it is hidden from `usage` because it is
// never meant to be typed by a person.
func cmdEgressProxy(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("egress-proxy", stderr)
	listen := fs.String("listen", ":3128", "address to listen on")
	allow := fs.String("allow", "", "comma-separated hostnames to allow (any port)")
	logPath := fs.String("log", "", "path to append the JSON-lines attempt log; defaults to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var hosts []string
	for _, h := range strings.Split(*allow, ",") {
		h = strings.TrimSpace(h)
		if h != "" {
			hosts = append(hosts, h)
		}
	}

	logWriter := stderr
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("egress-proxy: open log: %w", err)
		}
		defer f.Close()
		logWriter = f
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("egress-proxy: listen: %w", err)
	}

	srv := &http.Server{
		Handler:           smoke.NewProxy(hosts, logWriter),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("egress-proxy: shutdown: %w", err)
		}
		return nil
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("egress-proxy: serve: %w", err)
		}
		return nil
	}
}
