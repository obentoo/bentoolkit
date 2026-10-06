package validate

// ProbeIsolation reports whether THIS PROCESS can create a network namespace,
// and — when it cannot — why.
//
// It measures rather than infers. Portage reports `network-sandbox` in FEATURES
// but emits no warning when the unshare fails for lack of privilege, so a green
// build could mean "network cut off" or "full network access". Inferring from
// euid or capabilities would repeat that defect, so the probe starts a
// short-lived child with the namespace requested at fork time and reads the
// kernel's answer. A child, not unshare on this thread: Go cannot reliably
// retire an OS thread left in a foreign namespace, so in-process unsharing would
// contaminate whichever goroutines later land on it.
//
// A probe that could not run has proved NOTHING, so it returns false with a
// reason exactly like a denial does: a failure of its own must never become
// evidence of isolation. reason is empty if and only if ok is true.
func ProbeIsolation() (bool, string) {
	if err := probeNetNS(); err != nil {
		return false, "could not create a network namespace: " + err.Error()
	}
	return true, ""
}
