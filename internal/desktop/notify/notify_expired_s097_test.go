package notify_test

// Story 097, sub-task 15.1: NotificationClosed carries a reason, and only
// some reasons end a notification. KDE Plasma emits reason 1 (expired) when a
// normal-urgency popup times out into its history, where the id stays live
// and replaceable: the Notifier must keep treating that id as the condition's
// notification, so a critical still replaces the warning (R6.6, R6.16), a
// clear still withdraws it (R6.12, R6.16) and a click from the history still
// reaches the tray. Reasons 2 (dismissed), 3 (closed by a call), 4
// (undefined) and a malformed reason end it: the next notification for that
// condition goes out as new (R6.17).
//
// Hostile halves come first. Keeping expired records must not let them pile
// up without bound, must not keep two records alive for one condition, must
// not drop a record of a neighbouring condition or a notice, and must not
// survive a change of server owner (sub-task 9.1).
//
// The fake server is s097Server (notify_system_s097_test.go): it never
// forgets an id, so a NotificationClosed with a chosen reason is emitted with
// srv.emit while the id stays live, as on Plasma. Reuses newNotifier,
// noticeMsg, systemMsg, s097Send, s097Ctx, lastCall, the key* constants and,
// for the owner change, the handoffServer helpers.

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
)

// Close reasons of the Desktop Notifications spec.
const (
	expdReasonExpired   = uint32(1)
	expdReasonDismissed = uint32(2)
	expdReasonClosed    = uint32(3)
	expdReasonUndefined = uint32(4)
)

// expdExpiredCap is how many expired records the Notifier keeps.
const expdExpiredCap = 64

// expdExpire has srv report each id expired, as Plasma does when a popup
// times out into its history; srv keeps every id live.
func expdExpire(t *testing.T, srv *s097Server, ids ...uint32) {
	t.Helper()
	for _, id := range ids {
		srv.emit(t, "NotificationClosed", id, expdReasonExpired)
	}
}

// expdSync proves n has handled every signal srv emitted before the call:
// a probe notice is sent and srv clicks it; signals are handled in order, so
// the probe's click is reported only after every earlier one. The events
// reported before the probe's click are returned.
func expdSync(t *testing.T, n *notify.Notifier, srv *s097Server, tag string) []notify.Event {
	t.Helper()
	probe := "probe-" + tag
	var before []notify.Event
	deadline := time.Now().Add(3 * time.Second)
	for {
		id := s097Send(t, n, noticeMsg(probe, probe, "b", 0))
		srv.emit(t, "ActionInvoked", id, "default")
		wait := time.After(300 * time.Millisecond)
	collect:
		for {
			select {
			case ev := <-n.Events():
				if ev.NoticeID == probe {
					return before
				}
				before = append(before, ev)
			case <-wait:
				break collect
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("syncing %s: the Notifier never reported a click from the server", tag)
		}
	}
}

// expdReplaces sends a critical system notification for key that asks to
// replace id and returns the replaces_id the server received and the id it
// returned.
func expdReplaces(t *testing.T, n *notify.Notifier, srv *s097Server, key string, id uint32) (replaces, got uint32) {
	t.Helper()
	got = s097Send(t, n, systemMsg("Condition "+key, key, id, 2))
	return lastCall(t, srv).Replaces, got
}

// expdAssertEnded checks that id no longer stands for key: Close sends no
// CloseNotification, then a notification asking to replace id goes out as
// new. Close is checked first, because the new notification is itself a
// record of key.
func expdAssertEnded(t *testing.T, n *notify.Notifier, srv *s097Server, key string, id uint32, why string) {
	t.Helper()
	before := len(srv.closed())
	if err := n.Close(s097Ctx(t), id, key); err != nil {
		t.Errorf("%s: Close(%d, %q) = %v, want nil with no call", why, id, key, err)
	}
	if got := srv.closed()[before:]; len(got) != 0 {
		t.Errorf("%s: CloseNotification calls %v for id %d, want none (the notification has ended)", why, got, id)
	}
	if r, _ := expdReplaces(t, n, srv, key, id); r != 0 {
		t.Errorf("%s: replaces_id = %d, want 0 (the next notification for %q must be new)", why, r, key)
	}
}

// expdAssertStands checks that id still stands for key: a notification asking
// to replace it passes replaces_id = id and keeps the id.
func expdAssertStands(t *testing.T, n *notify.Notifier, srv *s097Server, key string, id uint32, why string) {
	t.Helper()
	r, got := expdReplaces(t, n, srv, key, id)
	if r != id {
		t.Errorf("%s: replaces_id = %d, want %d (the notification of %q still stands on the server)", why, r, id, key)
	}
	if got != id {
		t.Errorf("%s: Send returned %d, want the replaced id %d", why, got, id)
	}
}

// expdClicks returns the events n reports for clicks srv emits on ids, in
// order, without waiting on a timer for an absent one.
func expdClicks(t *testing.T, n *notify.Notifier, srv *s097Server, tag string, ids ...uint32) []notify.Event {
	t.Helper()
	for _, id := range ids {
		srv.emit(t, "ActionInvoked", id, "default")
	}
	return expdSync(t, n, srv, tag)
}

// TestS097Expired_ExpiredSystemNotificationIsStillReplacedAndWithdrawn is
// R6.16's hostile half, measured on Plasma 6.8: the server reports the
// warning expired while its id stays live. The critical that follows must
// still replace it (one notification in the history, not two, R6.6) and a
// clear must still withdraw it (R6.12).
func TestS097Expired_ExpiredSystemNotificationIsStillReplacedAndWithdrawn(t *testing.T) {
	t.Run("critical replaces the expired warning", func(t *testing.T) {
		addr := dbustest.Session(t)
		srv := startS097Server(t, addr)
		n := newNotifier(t, addr)

		warn := s097Send(t, n, systemMsg("Low disk space on /var/tmp/portage", keyPortage, 0, 1))
		expdExpire(t, srv, warn)
		expdSync(t, n, srv, "expire-warning")

		expdAssertStands(t, n, srv, keyPortage, warn, "critical after the warning expired")

		// The replacement expires too; the clear still withdraws that id.
		expdExpire(t, srv, warn)
		expdSync(t, n, srv, "expire-critical")
		if err := n.Close(s097Ctx(t), warn, keyPortage); err != nil {
			t.Fatalf("Close(%d) of the expired replacement: %v", warn, err)
		}
		if got := srv.closed(); !slices.Equal(got, []uint32{warn}) {
			t.Errorf("CloseNotification calls = %v, want exactly [%d]: the expired notification stays in the history", got, warn)
		}

		// The withdrawal ended it (the server answered with reason 3).
		expdSync(t, n, srv, "after-close")
		expdAssertEnded(t, n, srv, keyPortage, warn, "after the withdrawal")
	})

	t.Run("a clear withdraws the expired warning", func(t *testing.T) {
		addr := dbustest.Session(t)
		srv := startS097Server(t, addr)
		n := newNotifier(t, addr)

		keep := s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, 0, 1))
		warn := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
		expdExpire(t, srv, keep, warn)
		expdSync(t, n, srv, "expire-both")

		if err := n.Close(s097Ctx(t), warn, keyRoot); err != nil {
			t.Fatalf("Close(%d) of an expired warning: %v", warn, err)
		}
		if got := srv.closed(); !slices.Equal(got, []uint32{warn}) {
			t.Errorf("CloseNotification calls = %v, want exactly [%d] (not the memory notification %d)", got, warn, keep)
		}
		// The other expired condition is untouched by the withdrawal.
		expdAssertStands(t, n, srv, keyMemory, keep, "the other expired condition")
	})

	t.Run("a replaced warning that then expires is still replaced", func(t *testing.T) {
		addr := dbustest.Session(t)
		srv := startS097Server(t, addr)
		n := newNotifier(t, addr)

		id := s097Send(t, n, systemMsg("CPU saturated", keyCPU, 0, 1))
		if again := s097Send(t, n, systemMsg("CPU saturated", keyCPU, id, 1)); again != id {
			t.Fatalf("fixture: the replacement got id %d, want %d", again, id)
		}
		expdExpire(t, srv, id)
		expdSync(t, n, srv, "expire-replaced")
		expdAssertStands(t, n, srv, keyCPU, id, "expired after a replacement")
	})
}

// TestS097Expired_ClickOnAnExpiredNotificationStillYieldsItsEvent is R6.16
// for the history: a click on a notice or a system notification after the
// server reported it expired is still reported, with the activation token
// the server issued for it.
func TestS097Expired_ClickOnAnExpiredNotificationStillYieldsItsEvent(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	notice := s097Send(t, n, noticeMsg("2026-10-15-cve", "CVE", "b", 1))
	sys := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
	expdExpire(t, srv, notice, sys)
	expdSync(t, n, srv, "expire-clicks")

	srv.emit(t, "ActivationToken", notice, "token-for-notice")
	evs := expdClicks(t, n, srv, "clicks", notice, sys)
	if len(evs) != 2 {
		t.Fatalf("events for clicks on two expired notifications = %+v, want 2 (the history still holds them)", evs)
	}
	if ev := evs[0]; ev.NoticeID != "2026-10-15-cve" || ev.System || ev.Action != "default" || ev.ActivationToken != "token-for-notice" {
		t.Errorf("click on the expired notice: event = %+v, want notice 2026-10-15-cve/default with token-for-notice", ev)
	}
	if ev := evs[1]; !ev.System || ev.NoticeID != "" || ev.Action != "default" || ev.ActivationToken != "" {
		t.Errorf("click on the expired system notification: event = %+v, want System=true/default, no token", ev)
	}
}

// TestS097Expired_DismissedOrClosedSendsTheNextAsNew is R6.17: a
// notification the user dismissed (reason 2) or that a CloseNotification
// call closed (reason 3) has ended, so it is neither replaced nor withdrawn.
func TestS097Expired_DismissedOrClosedSendsTheNextAsNew(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason uint32
	}{
		{"dismissed by the user", expdReasonDismissed},
		{"closed by a call", expdReasonClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := dbustest.Session(t)
			srv := startS097Server(t, addr)
			n := newNotifier(t, addr)

			id := s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, 0, 1))
			srv.emit(t, "NotificationClosed", id, tc.reason)
			expdSync(t, n, srv, "reason")
			expdAssertEnded(t, n, srv, keyMemory, id, fmt.Sprintf("reason %d", tc.reason))
		})
	}
}

// TestS097Expired_UndefinedOrMalformedReasonEndsTheNotification: reason 4
// (undefined) ends a notification, and so does a reason that is not a
// uint32, even one whose value reads as 1: only a well-formed reason 1 keeps
// the record.
func TestS097Expired_UndefinedOrMalformedReasonEndsTheNotification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason any
	}{
		{"reason 4", expdReasonUndefined},
		{"reason 1 as int32", int32(1)},
		{"reason 1 as a string", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := dbustest.Session(t)
			srv := startS097Server(t, addr)
			n := newNotifier(t, addr)

			id := s097Send(t, n, systemMsg("CPU saturated", keyCPU, 0, 1))
			srv.emit(t, "NotificationClosed", id, tc.reason)
			expdSync(t, n, srv, "reason")
			expdAssertEnded(t, n, srv, keyCPU, id, tc.name)
		})
	}
}

// TestS097Expired_ExpiredRecordsAreCapped: expired records cannot pile up
// without bound. Of 65 expired notifications the oldest is forgotten and the
// 64 others still stand; a notification that never expired is not counted
// and is never evicted for one that did.
func TestS097Expired_ExpiredRecordsAreCapped(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	// Older than every expired record, and never expired.
	live := s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, 0, 2))
	notice := s097Send(t, n, noticeMsg("2026-10-15-live", "Live", "b", 1))

	const total = expdExpiredCap + 1
	keys := make([]string, total)
	ids := make([]uint32, total)
	for i := range total {
		keys[i] = fmt.Sprintf("disk:/mnt/expd-%02d", i)
		ids[i] = s097Send(t, n, systemMsg("Low disk space on "+keys[i], keys[i], 0, 1))
	}
	expdExpire(t, srv, ids...)
	expdSync(t, n, srv, "expire-65")

	// The live notification and the notice are not expired records.
	expdAssertStands(t, n, srv, keyMemory, live, "a live record older than every expired one")
	evs := expdClicks(t, n, srv, "live-notice", notice)
	if len(evs) != 1 || evs[0].NoticeID != "2026-10-15-live" {
		t.Errorf("click on a live notice after 65 expiries: events = %+v, want one for 2026-10-15-live", evs)
	}

	// The newest and the second oldest expired records are kept.
	expdAssertStands(t, n, srv, keys[total-1], ids[total-1], "the newest expired record")
	expdAssertStands(t, n, srv, keys[1], ids[1], "the second oldest expired record (64 are kept)")

	// The oldest is gone: its key goes out as new.
	before := len(srv.closed())
	if err := n.Close(s097Ctx(t), ids[0], keys[0]); err != nil {
		t.Errorf("Close of the evicted record = %v, want nil with no call", err)
	}
	if got := srv.closed()[before:]; len(got) != 0 {
		t.Errorf("CloseNotification %v for the evicted oldest expired record, want none", got)
	}
	if r, _ := expdReplaces(t, n, srv, keys[0], ids[0]); r != 0 {
		t.Errorf("replaces_id = %d for the oldest of 65 expired records, want 0 (only 64 are kept)", r)
	}
}

// TestS097Expired_RecordingAKeyDropsItsOlderRecord: one condition has one
// notification. Recording a new notification for key K forgets any older
// record of K, so a stale id of K can neither be replaced nor withdrawn,
// while records of other keys, notices, and the replaced record itself are
// left alone.
func TestS097Expired_RecordingAKeyDropsItsOlderRecord(t *testing.T) {
	// Wrong collapse first: records that must stay apart.
	t.Run("a neighbouring key keeps its expired record", func(t *testing.T) {
		addr := dbustest.Session(t)
		srv := startS097Server(t, addr)
		n := newNotifier(t, addr)

		portage := s097Send(t, n, systemMsg("Low disk space on /var/tmp/portage", keyPortage, 0, 1))
		expdExpire(t, srv, portage)
		expdSync(t, n, srv, "expire-portage")
		s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
		s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
		expdAssertStands(t, n, srv, keyPortage, portage, "disk:/ recorded twice beside disk:/var/tmp/portage")
	})

	t.Run("notices never drop one another", func(t *testing.T) {
		addr := dbustest.Session(t)
		srv := startS097Server(t, addr)
		n := newNotifier(t, addr)

		first := s097Send(t, n, noticeMsg("2026-10-15-a", "A", "b", 1))
		expdExpire(t, srv, first)
		expdSync(t, n, srv, "expire-notice")
		s097Send(t, n, noticeMsg("2026-10-15-b", "B", "b", 1))
		s097Send(t, n, systemMsg("Keyless", "", 0, 1))
		evs := expdClicks(t, n, srv, "notice-click", first)
		if len(evs) != 1 || evs[0].NoticeID != "2026-10-15-a" {
			t.Errorf("click on the first notice after another notice and a keyless system notification: events = %+v, want one for 2026-10-15-a", evs)
		}
	})

	t.Run("a replacement keeps its own record", func(t *testing.T) {
		addr := dbustest.Session(t)
		srv := startS097Server(t, addr)
		n := newNotifier(t, addr)

		id := s097Send(t, n, systemMsg("CPU saturated", keyCPU, 0, 1))
		expdAssertStands(t, n, srv, keyCPU, id, "first replacement")
		if err := n.Close(s097Ctx(t), id, keyCPU); err != nil {
			t.Fatalf("Close(%d): %v", id, err)
		}
		if got := srv.closed(); !slices.Equal(got, []uint32{id}) {
			t.Errorf("CloseNotification calls = %v, want exactly [%d]: the replacement dropped its own record", got, id)
		}
	})

	// Wrong split: two records alive for one condition.
	t.Run("a live older record of the key is dropped", func(t *testing.T) {
		addr := dbustest.Session(t)
		srv := startS097Server(t, addr)
		n := newNotifier(t, addr)

		older := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
		newer := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 2))
		if newer == older {
			t.Fatalf("fixture: the second notification got the same id %d", older)
		}
		expdAssertStands(t, n, srv, keyRoot, newer, "the newer record of the key")
		expdAssertEnded(t, n, srv, keyRoot, older, "an older record of the same key")
	})

	t.Run("an expired older record of the key is dropped", func(t *testing.T) {
		addr := dbustest.Session(t)
		srv := startS097Server(t, addr)
		n := newNotifier(t, addr)

		older := s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, 0, 1))
		expdExpire(t, srv, older)
		expdSync(t, n, srv, "expire-older")
		newer := s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, 0, 2))
		expdAssertStands(t, n, srv, keyMemory, newer, "the newer record of the key")
		expdAssertEnded(t, n, srv, keyMemory, older, "an expired older record of the same key")
	})
}

// TestS097Expired_OwnerChangeStillDropsAnExpiredRecord is sub-task 9.1
// unchanged: an expired record belongs to the server that assigned it, and a
// --replace handoff forgets it like any other record; the new server's id is
// another application's.
func TestS097Expired_OwnerChangeStillDropsAnExpiredRecord(t *testing.T) {
	addr := dbustest.Session(t)
	first := startFirstServer(t, addr)
	n := newNotifier(t, addr)

	stale, err := n.Send(s097Ctx(t), systemMsg("CPU saturated", keyCPU, 0, 1))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	first.emit(t, "NotificationClosed", stale, expdReasonExpired)
	if evs := syncOwner(t, n, first, "expire"); len(evs) != 0 {
		t.Fatalf("fixture: unexpected events: %+v", evs)
	}

	second := replaceServer(t, addr)
	if foreign := foreignNotify(t, addr); foreign != stale {
		t.Fatalf("fixture: the foreign notification on the new server got id %d, want %d", foreign, stale)
	}
	if evs := syncOwner(t, n, second, "handoff"); len(evs) != 0 {
		t.Fatalf("fixture: unexpected events: %+v", evs)
	}

	if err := n.Close(s097Ctx(t), stale, keyCPU); err != nil {
		t.Errorf("Close(%d) after the handoff = %v, want nil with no call", stale, err)
	}
	if got, old := second.closed(), first.closed(); len(got) != 0 || len(old) != 0 {
		t.Errorf("CloseNotification sent after the handoff: new server %v, old server %v; want none", got, old)
	}
	if _, err := n.Send(s097Ctx(t), systemMsg("CPU saturated", keyCPU, stale, 2)); err != nil {
		t.Fatalf("Send after the handoff: %v", err)
	}
	if c := second.last(t); c.Replaces != 0 {
		t.Errorf("replaces_id = %d on the new server for an expired record of the old one, want 0", c.Replaces)
	}
}
