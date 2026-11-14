package validate

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// BuildEstimate is what this host's merge history says one package costs to
// build: the average merge time over Merges merges.
type BuildEstimate struct {
	Average time.Duration
	Merges  int
}

// BuildBudget caps how deep a bump is built: a package whose average build is
// above Limit, or which has no build history, is taken no further than
// configure. A zero Limit is no budget.
//
// History is read once per run, before the plan is priced, and the same value
// is handed to the plan and to the run, so the depth the operator confirms is
// the depth that runs.
type BuildBudget struct {
	Limit time.Duration
	// History is keyed by category/package, without slot or label.
	History map[string]BuildEstimate
}

// Cap applies the budget to a depth decision for the registry key pkg. Only a
// depth that compiles is capped: configure and below build nothing the budget
// is about. The reason always says what the budget decided, so a capped bump
// is never mistaken for a policy choice. A cap is the operator's instruction,
// like --depth, so it does not count as skipped by policy.
func (b BuildBudget) Cap(pkg string, decision DepthDecision) DepthDecision {
	if b.Limit <= 0 || decision.Depth <= DepthConfigure {
		return decision
	}
	estimate, known := b.History[budgetKey(pkg)]
	switch {
	case !known:
		decision.Reason += fmt.Sprintf("; capped at %s by the %s build budget: this host has no build history for it, so it counts as expensive",
			DepthConfigure, b.Limit)
	case estimate.Average > b.Limit:
		decision.Reason += fmt.Sprintf("; capped at %s by the %s build budget: it averages %s over %d merge(s) here",
			DepthConfigure, b.Limit, estimate.Average, estimate.Merges)
	default:
		decision.Reason += fmt.Sprintf("; within the %s build budget: it averages %s over %d merge(s) here",
			b.Limit, estimate.Average, estimate.Merges)
		return decision
	}
	decision.Depth = DepthConfigure
	return decision
}

// budgetKey drops a registry key's ":slot" or "@label" suffix: merge history
// knows packages, not registry entries.
func budgetKey(pkg string) string {
	key := strings.TrimSpace(pkg)
	if i := strings.IndexByte(key, '@'); i >= 0 {
		key = key[:i]
	}
	if i := strings.IndexByte(key, ':'); i >= 0 {
		key = key[:i]
	}
	return key
}

// ParseBuildHistory reads the output of `qlop -a -M` (averages, in seconds):
//
//	www-client/chromium: 6269 average for 29 merges
//	www-client/chromium: 2 average for 2 unmerges
//
// Only merge lines count; unmerges are not builds. A line in any other shape
// is ignored.
func ParseBuildHistory(out []byte) map[string]BuildEstimate {
	history := map[string]BuildEstimate{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		atom, rest, ok := strings.Cut(scanner.Text(), ": ")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) != 5 || fields[1] != "average" || fields[2] != "for" ||
			(fields[4] != "merges" && fields[4] != "merge") {
			continue
		}
		seconds, err1 := strconv.Atoi(fields[0])
		merges, err2 := strconv.Atoi(fields[3])
		if err1 != nil || err2 != nil || seconds < 0 || merges <= 0 {
			continue
		}
		history[strings.TrimSpace(atom)] = BuildEstimate{Average: time.Duration(seconds) * time.Second, Merges: merges}
	}
	return history
}
