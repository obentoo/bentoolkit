package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultManifestConcurrency bounds how many `pkgdev manifest` runs (and the
// distfile fetches around them) one Applier lets download at once, whatever
// --concurrency is. Forty workers downloading together share one link, so each
// run saw a fortieth of it and a large distfile could not finish in any fixed
// budget. Waiting for a slot costs no budget: the timers below start once the
// slot is held.
const DefaultManifestConcurrency = 4

// DefaultManifestStallTimeout is how long a manifest run may go without any
// sign of progress — no output line from pkgdev or its fetchers, no byte added
// to the distdir — before it is stopped. A slow download keeps the run alive;
// only a stuck one is stopped.
const DefaultManifestStallTimeout = 5 * time.Minute

// DefaultManifestTimeout is the absolute ceiling of one manifest run, however
// steadily it progresses. It exists so a trickling download cannot hold a slot
// for the rest of the day.
const DefaultManifestTimeout = time.Hour

// egencacheTimeout bounds the local egencache run that reads the staged
// candidate's expanded SRC_URI. It touches no network, so it keeps a fixed
// budget rather than the manifest limits.
const egencacheTimeout = 5 * time.Minute

// ManifestLimits are the limits a manifest run executes under. A zero field
// means its default (DefaultManifestConcurrency, DefaultManifestStallTimeout,
// DefaultManifestTimeout).
type ManifestLimits struct {
	// Concurrency is how many manifest runs may download at once.
	Concurrency int
	// StallTimeout stops a run that shows no progress for this long.
	StallTimeout time.Duration
	// Timeout stops a run that is still going after this long.
	Timeout time.Duration
}

// withDefaults fills every zero or negative field with its default.
func (l ManifestLimits) withDefaults() ManifestLimits {
	if l.Concurrency < 1 {
		l.Concurrency = DefaultManifestConcurrency
	}
	if l.StallTimeout <= 0 {
		l.StallTimeout = DefaultManifestStallTimeout
	}
	if l.Timeout <= 0 {
		l.Timeout = DefaultManifestTimeout
	}
	return l
}

// WithApplierManifestLimits sets the limits every manifest run of this Applier
// executes under; zero fields keep their defaults.
func WithApplierManifestLimits(l ManifestLimits) ApplierOption {
	return func(a *Applier) { a.manifestLimits = l }
}

// withSweeperManifestLimits hands a sweeper the slot channel it shares with
// the Applier that built it, and the two timeouts. A nil channel is no bound.
func withSweeperManifestLimits(slots chan struct{}, l ManifestLimits) sweeperOption {
	return func(s *sweeper) {
		s.manifestSlots = slots
		s.manifestLimits = l
	}
}

// takeManifestSlot blocks until a manifest slot is free, or ctx ends. The
// returned release must be called exactly once. A nil sweeper channel never
// blocks.
func (s *sweeper) takeManifestSlot(ctx context.Context, pkg string) (func(), error) {
	if s.manifestSlots == nil {
		return func() {}, nil
	}
	select {
	case s.manifestSlots <- struct{}{}:
	default:
		s.log.Debug("manifest: waiting for a download slot", "package", pkg, "limit", cap(s.manifestSlots))
		select {
		case s.manifestSlots <- struct{}{}:
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for a manifest download slot for %s: %w", pkg, ctx.Err())
		}
	}
	return func() { <-s.manifestSlots }, nil
}

// manifestWatch cancels the context a manifest run executes under when the run
// stalls or reaches its ceiling. The cause it records says which limit fired
// and which key raises it.
//
// Progress is either signal: a write to the run's output (pkgdev and the
// fetchers it starts print as they download), or a change in the total size of
// the distdir's files. On the host's shared DISTDIR another worker's download
// also counts, which can only keep a run alive longer; the ceiling still holds.
type manifestWatch struct {
	cancel   context.CancelCauseFunc
	stopCeil context.CancelFunc
	writes   atomic.Int64
	done     chan struct{}
	wg       sync.WaitGroup
	once     sync.Once
}

// manifestPollInterval is how often the watch looks for progress: a tenth of
// the stall budget, kept between 10 ms (tests) and 15 s (a large DISTDIR is
// listed once per poll).
func manifestPollInterval(stall time.Duration) time.Duration {
	return min(max(stall/10, 10*time.Millisecond), 15*time.Second)
}

// withManifestWatch derives the run's context from parent and starts watching
// it. probe returns a value that changes when the distdir gains bytes; nil
// watches the output alone. stop must be called when the run returns.
func withManifestWatch(parent context.Context, l ManifestLimits, probe func() int64) (context.Context, *manifestWatch) {
	l = l.withDefaults()
	w := &manifestWatch{done: make(chan struct{})}
	causeCtx, cancel := context.WithCancelCause(parent)
	ceilErr := fmt.Errorf("%w: still running at the %s ceiling (autoupdate.manifest.timeout raises it)",
		ErrManifestTimedOut, l.Timeout)
	ctx, stopCeil := context.WithTimeoutCause(causeCtx, l.Timeout, ceilErr)
	w.cancel, w.stopCeil = cancel, stopCeil
	if probe == nil {
		probe = func() int64 { return 0 }
	}

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		tick := time.NewTicker(manifestPollInterval(l.StallTimeout))
		defer tick.Stop()
		lastSize, lastWrites, lastProgress := probe(), w.writes.Load(), time.Now()
		for {
			select {
			case <-w.done:
				return
			case <-ctx.Done():
				return
			case now := <-tick.C:
				size, writes := probe(), w.writes.Load()
				if size != lastSize || writes != lastWrites {
					lastSize, lastWrites, lastProgress = size, writes, now
					continue
				}
				if now.Sub(lastProgress) >= l.StallTimeout {
					cancel(fmt.Errorf("%w: no download progress for %s (autoupdate.manifest.stall_timeout raises it)",
						ErrManifestTimedOut, l.StallTimeout))
					return
				}
			}
		}
	}()
	return ctx, w
}

// Write counts output as progress; it is teed beside the run's real output.
func (w *manifestWatch) Write(p []byte) (int, error) {
	w.writes.Add(1)
	return len(p), nil
}

// stop ends the watch and releases its contexts. Safe to call twice.
func (w *manifestWatch) stop() {
	w.once.Do(func() {
		close(w.done)
		w.wg.Wait()
		w.stopCeil()
		w.cancel(nil)
	})
}

// manifestLimitFired returns the cause when one of the watch's own limits
// stopped the run under ctx, or nil when none did (success, or the parent was
// cancelled). Call it before stop, which cancels ctx with no cause of its own.
func manifestLimitFired(parent, ctx context.Context) error {
	if parent.Err() != nil {
		return nil //nolint:nilerr // the parent's own end is the caller's news, not a limit of this run
	}
	if cause := context.Cause(ctx); errors.Is(cause, ErrManifestTimedOut) {
		return cause
	}
	return nil
}

// distdirBytes sums the sizes of the regular files directly in dir. Symlinks
// (cache links) are not followed: they do not grow. An unreadable directory is
// 0, which only means "no progress seen there".
func distdirBytes(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total
}
