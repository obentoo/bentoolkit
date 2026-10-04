package distfiles

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPortageqLookupStopsWhenTheCallerCancels pins that the host query is
// bounded by the CALLER's context, not only by portageqTimeout. A portageq that
// never answers is cancelled mid-flight; each entry point that reaches the query
// must return its "unanswered" value well before the package's own timeout.
//
// The timeout is raised far above the assertion bound on purpose: if the query
// ignored ctx it would run until portageqTimeout, and the elapsed-time check
// below would fail.
func TestPortageqLookupStopsWhenTheCallerCancels(t *testing.T) {
	const cancelAfter = 100 * time.Millisecond
	const bound = 3 * time.Second

	cases := []struct {
		name string
		// ask runs the entry point and reports whether it answered at all.
		ask func(ctx context.Context) bool
	}{
		{"Locate", func(ctx context.Context) bool {
			_, found := Locate(ctx, "", "")
			return found
		}},
		{"TempRoot", func(ctx context.Context) bool {
			return TempRoot(ctx) != ""
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubPortageqTimeout(t, 10*time.Second)
			call := stubPortageqExec(t, "sleep", "30")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			timer := time.AfterFunc(cancelAfter, cancel)
			defer timer.Stop()

			start := time.Now()
			answered := tc.ask(ctx)
			elapsed := time.Since(start)

			if call.calls != 1 {
				t.Fatalf("portageq was asked %d time(s), want 1", call.calls)
			}
			if answered {
				t.Errorf("%s answered after its context was cancelled; a cancelled query is unanswered", tc.name)
			}
			if elapsed > bound {
				t.Errorf("%s returned after %v; a cancelled caller must stop the portageq query at once, not after portageqTimeout", tc.name, elapsed)
			}
		})
	}
}

// TestResolveOnACancelledContextReportsTheCancel pins that a done ctx is an
// answer of its own on the host rung, not an "unanswered" rung that lets the
// precedence fall through to DefaultCache. Falling through would create and
// probe a directory the host never named, and an interrupt would come back as
// ErrDistdirNotWritable instead of context.Canceled.
func TestResolveOnACancelledContextReportsTheCancel(t *testing.T) {
	host := t.TempDir()
	stubPortageqExec(t, "echo", host)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	dir, err := Resolve(ctx, "", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve on a cancelled context: dir=%q err=%v; want errors.Is(err, context.Canceled)", dir.Path, err)
	}
	if dir.Path != "" || dir.Created {
		t.Errorf("Resolve on a cancelled context returned dir %+v; want the zero Dir", dir)
	}

	live, err := Resolve(t.Context(), "", "")
	if err != nil || live.Path != host {
		t.Errorf("Resolve on a live context = (%q, %v); want the host DISTDIR %q", live.Path, err, host)
	}
}
