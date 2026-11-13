// Command adlc is the single entry point for the ADLC framework.
package main

import (
	"os"

	"github.com/roman-ambinder/agentic-framework/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
