"""fixture-count: a first-party MCP stdio server that exists only to be
packaged into the contract fixture MCPGW's tests consume. It answers one
tool, count, which returns how many times it has been called, and imports its
one vendored dependency so the python backend's PYTHONPATH is exercised."""

import json
import sys

import six  # vendored under .venv by pip install --require-hashes

calls = 0


def reply(msg_id, result):
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": msg_id, "result": result}) + "\n")
    sys.stdout.flush()


for line in sys.stdin:
    try:
        req = json.loads(line)
    except ValueError:
        continue
    if "id" not in req:
        continue
    method = req.get("method")
    if method == "initialize":
        reply(req["id"], {
            "protocolVersion": "2025-06-18",
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "fixture-count", "version": six.text_type("1.0.0")},
        })
    elif method == "tools/list":
        reply(req["id"], {"tools": [{"name": "count", "description": "Count calls.", "inputSchema": {"type": "object"}}]})
    elif method == "tools/call":
        calls += 1
        reply(req["id"], {"content": [{"type": "text", "text": str(calls)}]})
    else:
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": req["id"], "error": {"code": -32601, "message": "method not found"}}) + "\n")
        sys.stdout.flush()
