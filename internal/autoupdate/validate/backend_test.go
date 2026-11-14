package validate

import (
	"errors"
	"slices"
	"testing"
)

func TestNewBuildBackend(t *testing.T) {
	for _, name := range []string{"", BackendHost} {
		b, err := NewBuildBackend(name)
		if err != nil || b.Name() != BackendHost {
			t.Errorf("NewBuildBackend(%q) = %v, %v; want the host backend", name, b, err)
		}
	}
	for _, name := range []string{BackendVM, BackendContainer} {
		if _, err := NewBuildBackend(name); !errors.Is(err, ErrBackendUnavailable) {
			t.Errorf("NewBuildBackend(%q) err = %v; want ErrBackendUnavailable, never a silent host", name, err)
		}
	}
	if _, err := NewBuildBackend("kvm"); err == nil || errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("an unknown name was not refused as unknown: %v", err)
	}
}

func TestHostBackendRunsTheCommandAsItIs(t *testing.T) {
	name, arg := HostBackend{}.Wrap("ebuild", []string{"x.ebuild", "clean", "configure"})
	if name != "ebuild" || !slices.Equal(arg, []string{"x.ebuild", "clean", "configure"}) {
		t.Errorf("host Wrap changed the command: %s %v", name, arg)
	}
}
