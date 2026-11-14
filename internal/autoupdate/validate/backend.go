package validate

import (
	"errors"
	"fmt"
)

// BuildBackend is where the build gates' commands run: this host, a virtual
// machine, a container. The gates build the same argv on every backend; a
// backend only says how that argv is run there.
type BuildBackend interface {
	// Name is the backend's name in config, reports and stage records.
	Name() string
	// Wrap turns the command a build gate would run on this host into the
	// command that runs it in the backend.
	Wrap(name string, arg []string) (string, []string)
}

// The backend names config and --build-backend accept.
const (
	BackendVM        = "vm"
	BackendContainer = "container"
)

// BackendNames lists every backend name, the default first.
var BackendNames = []string{BackendHost, BackendVM, BackendContainer}

// HostBackend runs the build gates on this machine, as bentoo always has.
type HostBackend struct{}

// Name is BackendHost.
func (HostBackend) Name() string { return BackendHost }

// Wrap runs the command as it is.
func (HostBackend) Wrap(name string, arg []string) (string, []string) { return name, arg }

// ErrBackendUnavailable is returned for a backend name that is known but not
// implemented yet.
var ErrBackendUnavailable = errors.New("build backend not available yet")

// ErrBackendIsAnEnvironment is returned for a backend that runs in a separate
// machine: the caller builds it (buildenv), because it needs a running
// environment this package does not start.
var ErrBackendIsAnEnvironment = errors.New("the build backend is a separate environment")

// NewBuildBackend returns the backend named name. An unknown name is an error,
// and so is a known one that is not built yet: a build never falls back to
// another backend without saying so.
func NewBuildBackend(name string) (BuildBackend, error) {
	switch name {
	case "", BackendHost:
		return HostBackend{}, nil
	case BackendContainer:
		return nil, ErrBackendIsAnEnvironment
	case BackendVM:
		return nil, fmt.Errorf("%w: %q — use %q or %q until it is", ErrBackendUnavailable, name, BackendHost, BackendContainer)
	}
	return nil, fmt.Errorf("unknown build backend %q: want one of %v", name, BackendNames)
}
