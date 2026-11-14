package anthropic

import (
	"os"

	"github.com/anthropics/anthropic-sdk-go/v2/config"
)

// HasCredentials reports whether the SDK's default credential chain can find
// a credential: ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, or a signed-in
// profile (for example from `ant auth login`) in the Anthropic config
// directory. Live tests use it to decide whether to run.
func HasCredentials() bool {
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		return true
	}
	_, err := config.LoadConfig()
	return err == nil
}
