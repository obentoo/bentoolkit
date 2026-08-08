package validate

// ProbeIsolation reports whether THIS PROCESS can create a network namespace,
// and — when it cannot — why.
//
// # Why this is measured and not inferred
//
// The compile gate currently prints a plain green whenever the build succeeds.
// It does not check isolation; Portage reports `network-sandbox` in FEATURES,
// and creating the namespace needs privilege that an ordinary user does not
// have, and Portage emits no warning when the unshare fails. So a pass could
// mean "built with the network cut off" or "built with full network access",
// and nothing printed distinguished them.
//
// Inferring the answer from euid or from capabilities was rejected for the same
// reason: inference instead of measurement IS the defect. So the probe asks the
// kernel — it starts a short-lived child with the namespace requested at fork
// time, and reads the answer the kernel gives.
//
// # Why a child and not unshare on this thread
//
// Go multiplexes goroutines onto OS threads and cannot reliably retire one left
// in a foreign namespace, so unsharing in-process would contaminate whichever
// goroutines later land on that thread. A child is created and reaped, and
// nothing in this process changes.
//
// # Undetermined is never a pass
//
// A probe that could not run has proved NOTHING, so it returns false with a
// reason exactly like a denial does. The one thing this function must never do
// is let a failure of its own become evidence of isolation.
//
// reason is empty if and only if ok is true.
func ProbeIsolation() (bool, string) {
	if err := probeNetNS(); err != nil {
		return false, "could not create a network namespace: " + err.Error()
	}
	return true, ""
}
