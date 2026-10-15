package distfiles

// Story 097, sub-task 6.1: bentoo-tray watches the host's DISTDIR by default,
// so the portageq question behind Resolve is exported as HostDistdir. The
// stubs of resolve_test.go stand in for portageq; no test runs the real one.

import "testing"

// TestS097HostDistdir_AsksPortageqDistdir: HostDistdir asks `portageq
// distdir`, not the PORTAGE_TMPDIR question TempRoot asks, and returns the
// answer as a path.
func TestS097HostDistdir_AsksPortageqDistdir(t *testing.T) {
	call := stubPortageqOutput(t, "/var/cache/distfiles\n")
	if got := HostDistdir(t.Context()); got != "/var/cache/distfiles" {
		t.Errorf("HostDistdir() = %q, want %q", got, "/var/cache/distfiles")
	}
	if want := "portageq distdir"; call.argv() != want {
		t.Errorf("HostDistdir asked %q, want %q", call.argv(), want)
	}
	if call.calls != 1 {
		t.Errorf("HostDistdir ran portageq %d times, want 1", call.calls)
	}
}

// TestS097HostDistdir_EmptyWhenUnanswered: every unanswered question is "",
// never an error, so the tray can warn and watch the remaining defaults.
func TestS097HostDistdir_EmptyWhenUnanswered(t *testing.T) {
	t.Run("portageq is not installed", func(t *testing.T) {
		stubPortageqUnavailable(t)
		if got := HostDistdir(t.Context()); got != "" {
			t.Errorf("HostDistdir() = %q, want \"\"", got)
		}
	})
	t.Run("the answer is not an absolute path", func(t *testing.T) {
		stubPortageqOutput(t, "var/cache/distfiles\n")
		if got := HostDistdir(t.Context()); got != "" {
			t.Errorf("HostDistdir() = %q, want \"\"", got)
		}
	})
	t.Run("the answer is empty", func(t *testing.T) {
		stubPortageqOutput(t, "\n")
		if got := HostDistdir(t.Context()); got != "" {
			t.Errorf("HostDistdir() = %q, want \"\"", got)
		}
	})
}
