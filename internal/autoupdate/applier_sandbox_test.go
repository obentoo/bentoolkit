package autoupdate

import (
	"context"
	"os/exec"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/buildenv"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// separateMachine is a build backend that is a separate machine, as a
// container or VM session is; only its type matters here.
type separateMachine struct{}

func (separateMachine) Name() string                                      { return validate.BackendContainer }
func (separateMachine) Wrap(name string, arg []string) (string, []string) { return name, arg }
func (separateMachine) Prepare(context.Context, buildenv.PrepareRequest) error {
	return nil
}
func (separateMachine) Adapt(_ context.Context, cmd *exec.Cmd) *exec.Cmd { return cmd }
func (separateMachine) Fresh(context.Context) error                      { return nil }
func (separateMachine) LastPassFresh() bool                              { return true }
func (separateMachine) Isolated(context.Context) (bool, string)          { return true, "" }
func (separateMachine) Alive(context.Context) bool                       { return true }
func (separateMachine) Identity() string                                 { return "test" }

func TestSandboxHostBuild(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		backend validate.BuildBackend
		want    bool
	}{
		{"host backend, sandbox on", true, validate.HostBackend{}, true},
		{"host backend, sandbox off", false, validate.HostBackend{}, false},
		{"separate machine is never wrapped", true, separateMachine{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Applier{hostSandbox: tc.enabled, buildBackend: tc.backend}
			if got := a.sandboxHostBuild(); got != tc.want {
				t.Errorf("sandboxHostBuild() = %v, want %v", got, tc.want)
			}
		})
	}
}
