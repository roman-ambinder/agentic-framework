// Command gate-g0-creds exits 0 when the Anthropic SDK can find a credential
// (ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN or a signed-in profile), else 1.
// scripts/gate-g0.sh runs it before spending anything.
package main

import (
	"os"

	"github.com/roman-ambinder/agentic-framework/internal/model/anthropic"
)

func main() {
	if !anthropic.HasCredentials() {
		os.Exit(1)
	}
}
