//go:build !linux

package validate

import "errors"

// probeNetNS on a non-Linux host cannot answer the question at all.
//
// SysProcAttr.Cloneflags is a Linux field, and network namespaces are a Linux
// facility. Rather than pretend, this reports UNDETERMINED — which
// ProbeIsolation turns into ok=false, exactly like a denial.
//
// That equivalence is the point, and it is worth stating where someone might
// be tempted to "fix" it: a probe that could not run has proved nothing, so it
// must never produce a plain pass. Treating undetermined as success on the
// platforms that cannot measure it would put the unverified green back, on the
// hosts least able to notice.
var probeNetNS = func() error {
	return errors.New("undetermined: network namespaces are a Linux facility and cannot be probed on this platform")
}
