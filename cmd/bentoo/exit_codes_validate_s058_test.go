package main

// Authored for story 058, sub-task 4.1 — R1.1, R1.3, R2.6, R2.7.
//
// overlay validate is the command with four distinct codes (0, 1, 2, 130), so
// the hostile rows come first: an interruption must stay 130 and not collapse
// into 2 or 1, a run failure must stay 2 and not collapse into 1, and a --depth
// that does not parse must stay 1 and not be promoted to 2. The run is replaced
// through the validateRunner field of deps where the row needs a specific report.
//
// Red on arrival: overlay validate is still a Run command.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

func TestS058ValidateReturnsThroughRunE(t *testing.T) {
	if bad := s058NotOnRunE(t, "overlay validate"); len(bad) > 0 {
		t.Errorf("overlay validate still ends the process itself (R1.1): %v", bad)
	}
}

// s058StubValidate replaces the validation run on the deps the row's tree is
// built from.
func s058StubValidate(c *testCLI, fn func(context.Context, validate.Options) (validate.Report, error)) {
	c.deps.validateRunner = fn
}

func s058ValidateAnswers(report validate.Report, err error) func(*testing.T, *testCLI) []string {
	return func(_ *testing.T, c *testCLI) []string {
		s058StubValidate(c, func(context.Context, validate.Options) (validate.Report, error) { return report, err })
		return nil
	}
}

func s058ReportWithError(gate string) validate.Report {
	return validate.Report{Results: []validate.EbuildResult{{
		Package: "app-misc/foo",
		Version: "1.0",
		Gates: []validate.GateResult{{
			Gate:     gate,
			Outcome:  validate.OutcomeFailed,
			Findings: []validate.Finding{{Gate: gate, Severity: validate.SeverityError, Detail: "s058 error finding"}},
		}},
	}}}
}

func s058ValidateRows() []s058Row {
	partial := validate.Report{Results: []validate.EbuildResult{{
		Package: "app-misc/foo",
		Version: "1.0",
		Gates: []validate.GateResult{{
			Gate:    validate.GateOptions,
			Outcome: validate.OutcomeSkipped,
			Reason:  "the run was interrupted before this ebuild was validated",
		}},
	}}}
	return []s058Row{
		// Would wrongly collapse: 130 is neither 2 nor 1.
		{name: "an interrupted run", args: []string{"overlay", "validate"},
			setup: s058ValidateAnswers(partial, fmt.Errorf("the validation run was interrupted: %w", context.Canceled)), want: 130},
		{name: "the caller's cancellation reaches the run", args: []string{"overlay", "validate"},
			setup: func(_ *testing.T, c *testCLI) []string {
				s058StubValidate(c, func(ctx context.Context, _ validate.Options) (validate.Report, error) {
					select {
					case <-ctx.Done():
						return validate.Report{}, fmt.Errorf("the validation run was interrupted: %w", ctx.Err())
					case <-time.After(10 * time.Second):
						return validate.Report{}, nil
					}
				})
				return nil
			},
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				timer := time.AfterFunc(200*time.Millisecond, cancel)
				t.Cleanup(func() { timer.Stop(); cancel() })
				return ctx
			},
			want: 130},
		// Would wrongly collapse: 2 is not 1.
		{name: "a run that fails for another reason", args: []string{"overlay", "validate"},
			setup: s058ValidateAnswers(validate.Report{}, errors.New("s058 the staging root could not be created")), want: 2, once: "s058 the staging root could not be created"},
		{name: "a selector that matches nothing", args: []string{"overlay", "validate", "nosuch-cat"}, want: 2, once: `nothing in the overlay matches "nosuch-cat"`},
		{name: "an argument that is not a selector", args: []string{"overlay", "validate", "not a selector"}, want: 2},
		// Would wrongly be promoted: 1 is not 2.
		{name: "an error finding from a deciding gate", args: []string{"overlay", "validate"},
			setup: s058ValidateAnswers(s058ReportWithError(validate.GateOptions), nil), want: 1},
		{name: "a --depth that does not parse", args: []string{"overlay", "validate", "--depth=bogus"}, want: 1, once: "unknown validation depth"},
		{name: "two selectors", args: []string{"overlay", "validate", "a", "b"}, want: 1, usage: true},
		// Benign.
		{name: "an error finding from the QA gate alone", args: []string{"overlay", "validate"},
			setup: s058ValidateAnswers(s058ReportWithError(validate.GateQA), nil), want: 0},
		{name: "a clean run", args: []string{"overlay", "validate"}, setup: s058ValidateAnswers(validate.Report{}, nil), want: 0},
	}
}

func TestS058ValidateKeepsItsExitCodes(t *testing.T) {
	s058RunRows(t, s058ValidateRows())
}
