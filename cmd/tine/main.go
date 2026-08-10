// Command tine serves per-instance MCP endpoints for configured API
// integrations.
package main

import (
	"fmt"
	"os"
)

func main() {
	ctx, err := parse(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "tine: %v\n", err)
		os.Exit(1)
	}
	if err := ctx.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tine: %v\n", err)
		os.Exit(1)
	}
}
