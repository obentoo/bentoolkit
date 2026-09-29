package autoupdate

import (
	"os"
	"testing"
)

// TestClaudeClient_EndpointTestsLeaveThePriorEndpointInPlace runs each test that
// changes CLAUDE_API_ENDPOINT from every state the variable can be in
// beforehand, and checks that state is still there when the test ends.
//
// Present-but-empty and absent are different states. A restoration that reads
// the variable with os.Getenv and writes it back turns absent into empty; one
// that treats empty as "nothing to restore" turns empty into absent. Both are
// checked before the plain case of a set value.
func TestClaudeClient_EndpointTestsLeaveThePriorEndpointInPlace(t *testing.T) {
	const variable = "CLAUDE_API_ENDPOINT"

	endpointTests := []struct {
		name string
		run  func(*testing.T)
	}{
		{"TestClaudeClient_DefaultEndpoint", TestClaudeClient_DefaultEndpoint},
		{"TestClaudeClient_EndpointOverride", TestClaudeClient_EndpointOverride},
	}
	priors := []struct {
		name    string
		present bool
		value   string
	}{
		{"present_and_empty", true, ""},
		{"absent", false, ""},
		{"set", true, "https://prior.invalid/v1/messages"},
	}

	for _, prior := range priors {
		t.Run(prior.name, func(t *testing.T) {
			for _, tc := range endpointTests {
				t.Run(tc.name, func(t *testing.T) {
					t.Setenv(variable, prior.value)
					if !prior.present {
						if err := os.Unsetenv(variable); err != nil {
							t.Fatalf("unsetting %s before running %s: %v", variable, tc.name, err)
						}
					}

					t.Run("run", tc.run)

					got, present := os.LookupEnv(variable)
					if present != prior.present {
						t.Fatalf("%s left %s present=%t (value %q); before it ran the variable was present=%t (value %q)",
							tc.name, variable, present, got, prior.present, prior.value)
					}
					if got != prior.value {
						t.Fatalf("%s left %s = %q; before it ran it was %q", tc.name, variable, got, prior.value)
					}
				})
			}
		})
	}
}
