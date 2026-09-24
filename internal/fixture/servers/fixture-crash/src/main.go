// Command fixture-crash is smoke's negative fixture for a server that dies
// mid-handshake: it reads MCP stdio framing and exits 3 the moment it reads
// initialize, before answering. smoke must fail the run on the unanswered
// initialize and on the exit before shutdown.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	for in.Scan() {
		var r struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &r) == nil && r.Method == "initialize" {
			fmt.Fprintln(os.Stderr, "fixture-crash: exiting during initialize, as designed")
			os.Exit(3)
		}
	}
}
