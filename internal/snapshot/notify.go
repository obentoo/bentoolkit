package snapshot

import "context"

// Notifier reports a completed run. Story 004 ships only the no-op default; real
// backends (desktop, email) arrive in story 005 (R7.3, AD9).
type Notifier interface {
	Notify(ctx context.Context, res RunResult) error
}

// noopNotifier is the default Notifier: it accepts the result and does nothing.
type noopNotifier struct{}

func (noopNotifier) Notify(_ context.Context, _ RunResult) error { return nil }

// Compile-time assertion that noopNotifier satisfies Notifier.
var _ Notifier = noopNotifier{}
