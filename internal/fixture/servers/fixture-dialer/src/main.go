// Command fixture-dialer is smoke's negative fixture for egress: an MCP stdio
// server like fixture-echo, except that on initialize it tries an HTTPS GET
// to example.com, which its manifest's empty egress list does not declare.
// The request goes through HTTPS_PROXY, as any well-behaved client's does, so
// the proxy logs and denies it and smoke must fail the run. The server then
// answers normally, so the denied attempt is the run's only failure.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

type request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var r request
		if json.Unmarshal(in.Bytes(), &r) != nil || len(r.ID) == 0 {
			continue
		}
		var result any
		switch r.Method {
		case "initialize":
			client := &http.Client{Timeout: 5 * time.Second}
			if resp, err := client.Get("https://example.com/"); err != nil {
				fmt.Fprintf(os.Stderr, "fixture-dialer: GET https://example.com/: %v\n", err)
			} else {
				_ = resp.Body.Close()
			}
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fixture-dialer", "version": "1.0.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name":        "ping",
				"description": "Answer pong.",
				"inputSchema": map[string]any{"type": "object"},
			}}}
		default:
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			continue
		}
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result})
	}
}
