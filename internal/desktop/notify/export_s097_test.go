package notify

import "github.com/godbus/dbus/v5"

// OwnerView is a Notifier's view of the notification server's owner together
// with its record of one id, captured so a test can put it back later.
type OwnerView struct {
	owner    string
	ownerSeq dbus.Sequence
	id       uint32
	rec      sentNotification
	ok       bool
}

// CaptureOwnerView returns n's current view of the owner and of id.
func CaptureOwnerView(n *Notifier, id uint32) OwnerView {
	n.mu.Lock()
	defer n.mu.Unlock()
	rec, ok := n.sent[id]
	return OwnerView{owner: n.owner, ownerSeq: n.ownerSeq, id: id, rec: rec, ok: ok}
}

// RestoreOwnerView puts v back, as if the owner changes applied since had not
// reached n yet. Send and Close then pass their ownership check for v's id
// against the owner that left: this is the window between that check and
// their bus call, held open for as long as the test needs.
func RestoreOwnerView(n *Notifier, v OwnerView) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.owner, n.ownerSeq = v.owner, v.ownerSeq
	if v.ok {
		n.sent[v.id] = v.rec
	}
}
