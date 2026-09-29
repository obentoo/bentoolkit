package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestYieldSignalsToPromptHandsTheSignalBackAndRestoresIt pins what one
// Ctrl+C at a confirmation prompt rests on: while the prompt waits, the policy
// is not cancellable, so the process-wide handler re-raises the signal and it
// ends the command (TestS058NonCancellableCommandDiesOfTheFirstSignal proves
// that half); afterwards the command's own policy is back.
func TestYieldSignalsToPromptHandsTheSignalBackAndRestoresIt(t *testing.T) {
	ctx, stop, policy := processContext()
	t.Cleanup(stop)

	cmd := &cobra.Command{Use: "work", Annotations: map[string]string{cancellableAnnotation: "true"}}
	cmd.SetContext(withSignalPolicy(ctx, policy))
	policy.setFrom(cmd)
	if !policy.cancellable.Load() {
		t.Fatal("setup: the annotated command did not make the policy cancellable")
	}

	restore := yieldSignalsToPrompt(cmd)
	if policy.cancellable.Load() {
		t.Error("while the prompt waits the policy is still cancellable: a first Ctrl+C would only cancel a context the stdin read never looks at")
	}
	restore()
	if !policy.cancellable.Load() {
		t.Error("after the prompt the policy stays non-cancellable: the command's later work would die of a signal instead of being cancelled")
	}
}

// TestYieldSignalsToPromptWithoutAPolicyIsANoOp covers a handler called
// directly, as many tests do: there is no process-wide policy to change.
func TestYieldSignalsToPromptWithoutAPolicyIsANoOp(t *testing.T) {
	restore := yieldSignalsToPrompt(&cobra.Command{Use: "work"})
	restore()
}
