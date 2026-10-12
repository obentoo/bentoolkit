package autoupdate

import "context"

// DefaultApplyBuildConcurrency bounds how many applies of one batch run their
// build gates at once. The rest of each apply — fetch, `pkgdev manifest`,
// staging — keeps the full --concurrency pool; only the build is narrowed,
// because concurrent builds contend for CPU and for space under PORTAGE_TMPDIR
// (60 MB measured for one gst configure).
const DefaultApplyBuildConcurrency = 2

// DefaultApplyLLMConcurrency bounds how many LLM agents (manifest fixer, build
// fixer, bump reviewer) one Applier runs at once. Each agent is a long-lived
// claude child with its own token budget, so a pool of --concurrency of them
// would spend the budget and the provider's rate limit many times over.
const DefaultApplyLLMConcurrency = 2

// applySlots are the per-stage limits an Applier enforces inside Apply, so a
// batch can run every package concurrently and still hold the expensive stages
// to fewer. A nil channel means unbounded, which is what an Applier built as a
// struct literal in a test gets.
type applySlots struct {
	// build bounds the build gates selected by the depth policy.
	build chan struct{}
	// compile serialises the privileged --compile step: its confirmation prompt
	// and its sudo/doas invocation share one terminal and must not interleave.
	compile chan struct{}
	// llm bounds the LLM agents.
	llm chan struct{}
}

func newApplySlots(build, llm int) applySlots {
	return applySlots{
		build:   make(chan struct{}, max(build, 1)),
		compile: make(chan struct{}, 1),
		llm:     make(chan struct{}, max(llm, 1)),
	}
}

// WithApplierBuildConcurrency sets how many applies may run their build gates
// at once (default DefaultApplyBuildConcurrency; values below 1 mean 1).
func WithApplierBuildConcurrency(n int) ApplierOption {
	return func(a *Applier) { a.buildLimit = n }
}

// WithApplierLLMConcurrency sets how many LLM agents may run at once (default
// DefaultApplyLLMConcurrency; values below 1 mean 1).
func WithApplierLLMConcurrency(n int) ApplierOption {
	return func(a *Applier) { a.llmLimit = n }
}

// withSlot runs fn while holding one slot of slots. A free slot is taken at
// once, even on a done ctx, so an uncontended stage behaves exactly as it did
// unbounded and fn's own cancellation handling still decides. Only the wait for
// a busy slot gives up when ctx ends: fn never runs and ctx's error is returned
// with T's zero value. A nil slots runs fn at once.
func withSlot[T any](ctx context.Context, slots chan struct{}, fn func() (T, error)) (T, error) {
	if slots != nil {
		select {
		case slots <- struct{}{}:
		default:
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				var zero T
				return zero, ctx.Err()
			}
		}
		defer func() { <-slots }()
	}
	return fn()
}
