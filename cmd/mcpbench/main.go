// Command mcpbench is the CLI entry point.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/JSLEEKR/mcpbench/internal/cli"
)

func main() {
	err := cli.Execute(os.Args[1:], os.Stdout, os.Stderr)
	if err == nil {
		return
	}
	if errors.Is(err, cli.ErrRegression) {
		// compare output already printed; just exit non-zero
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "mcpbench:", err)
	os.Exit(1)
}
