// Command fixture-echo is a first-party MCP server that exists only to be
// packaged into the contract fixture MCPGW's tests consume. It speaks MCP
// stdio framing, one JSON-RPC message per line, and answers one tool, echo.
package main

import (
	"bufio"
	"encoding/json"
	"os"
)

type request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var r request
		if json.Unmarshal(in.Bytes(), &r) != nil || len(r.ID) == 0 {
			continue // a notification, or not JSON-RPC at all
		}
		var result any
		switch r.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fixture-echo", "version": "1.0.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name":        "echo",
				"description": "Return the text it is given.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			}}}
		case "tools/call":
			var p struct {
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(r.Params, &p)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": p.Arguments.Text}}}
		default:
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			continue
		}
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result})
	}
}
