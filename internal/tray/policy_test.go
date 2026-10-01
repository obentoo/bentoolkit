package tray

// Pinned signatures (story 072, Test Advisor, accepted by the orchestrator):
//
//	func acceptFeed(st state.State, f notices.Feed, now time.Time) (ok bool, reason string)
//	func decideNotifications(st *state.State, accepted []notices.Notice, firstRun, paused bool, cfg config.TrayConfig) []notify.Message
//	func viewFor(st state.State) sni.View
//
// decideNotifications upserts a record for every accepted notice (Title, URL,
// Severity, Type, Updated), marks non-security notices read on a first run,
// and returns the messages to send now. It never sets Notified: the App does
// that after a successful Send, which is what makes a failed send retry
// (R6.12). A summary message (R6.8) has an empty NoticeID.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/notices"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

var policyNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func savedState() state.State {
	return state.State{Format: 1, Saved: true, Notices: map[string]state.Record{}}
}

// words are digit-free titles, so a count found in a summary text can only be
// the count.
var words = []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot", "Golf", "Hotel", "India", "Juliett", "Kilo", "Lima"}

func notice(i int, typ, severity string) notices.Notice {
	id := fmt.Sprintf("2026-10-%02d-%s", i+1, strings.ToLower(words[i]))
	return notices.Notice{
		ID: id, Title: words[i] + " title", Summary: words[i] + " summary",
		URL:  "https://obentoo.org/notices/" + id + "/",
		Type: typ, Severity: severity, Source: notices.SourceFeed,
		Published: policyNow.Add(-time.Duration(12-i) * time.Hour),
		Updated:   policyNow.Add(-time.Duration(12-i) * time.Hour),
	}
}

func byNotice(ms []notify.Message) map[string]notify.Message {
	m := map[string]notify.Message{}
	for _, msg := range ms {
		m[msg.NoticeID] = msg
	}
	return m
}

func summaries(ms []notify.Message) []notify.Message {
	var out []notify.Message
	for _, m := range ms {
		if m.NoticeID == "" {
			out = append(out, m)
		}
	}
	return out
}

func actionKeys(m notify.Message) []string {
	var keys []string
	for _, a := range m.Actions {
		keys = append(keys, a.Key)
	}
	return keys
}

// --- acceptFeed ---

// TestAcceptFeed_EqualSerialIsNotARollback is the hostile half of R2.4,
// authored first: re-fetching the same build (equal serial) is not a rollback.
func TestAcceptFeed_EqualSerialIsNotARollback(t *testing.T) {
	st := savedState()
	st.Serial = 1790812800
	f := notices.Feed{Serial: 1790812800, Expires: policyNow.Add(24 * time.Hour)}
	if ok, reason := acceptFeed(st, f, policyNow); !ok {
		t.Errorf("an equal serial was rejected: %s", reason)
	}
}

// TestAcceptFeed_LowerSerialIsRejectedNamingBoth is R2.4.
func TestAcceptFeed_LowerSerialIsRejectedNamingBoth(t *testing.T) {
	st := savedState()
	st.Serial = 1790812800
	f := notices.Feed{Serial: 1790000000, Expires: policyNow.Add(24 * time.Hour)}
	ok, reason := acceptFeed(st, f, policyNow)
	if ok {
		t.Fatal("a feed with a lower serial was accepted")
	}
	for _, s := range []string{"1790812800", "1790000000"} {
		if !strings.Contains(reason, s) {
			t.Errorf("reason %q does not name serial %s", reason, s)
		}
	}
}

// TestAcceptFeed_PastExpiryIsRejectedNamingIt is R2.5, with the first-run and
// newer-serial cases that must pass.
func TestAcceptFeed_PastExpiryIsRejectedNamingIt(t *testing.T) {
	expires := policyNow.Add(-time.Second)
	ok, reason := acceptFeed(savedState(), notices.Feed{Serial: 5, Expires: expires}, policyNow)
	if ok {
		t.Fatal("an expired feed was accepted")
	}
	if !strings.Contains(reason, expires.UTC().Format(time.RFC3339)) {
		t.Errorf("reason %q does not name the expiry %s", reason, expires.UTC().Format(time.RFC3339))
	}
	if ok, reason := acceptFeed(state.State{}, notices.Feed{Serial: 5, Expires: policyNow.Add(time.Second)}, policyNow); !ok {
		t.Errorf("a current feed on a first run was rejected: %s", reason)
	}
	st := savedState()
	st.Serial = 4
	if ok, reason := acceptFeed(st, notices.Feed{Serial: 5, Expires: policyNow.Add(time.Hour)}, policyNow); !ok {
		t.Errorf("a newer serial was rejected: %s", reason)
	}
}

// --- decideNotifications ---

// TestDecide_SeverityMapsToUrgency is R6.1 and R6.2.
func TestDecide_SeverityMapsToUrgency(t *testing.T) {
	st := savedState()
	accepted := []notices.Notice{notice(0, "release", "info"), notice(1, "release", "warning"), notice(2, "security", "critical")}
	got := byNotice(decideNotifications(&st, accepted, false, false, config.TrayConfig{}))
	for i, want := range []byte{0, 1, 2} {
		m, ok := got[accepted[i].ID]
		if !ok {
			t.Errorf("%s was not notified", accepted[i].ID)
			continue
		}
		if m.Urgency != want {
			t.Errorf("%s severity %s: urgency %d, want %d", accepted[i].ID, accepted[i].Severity, m.Urgency, want)
		}
		if m.Summary != accepted[i].Title || m.Body != accepted[i].Summary {
			t.Errorf("%s: message %q/%q, want the title and summary", accepted[i].ID, m.Summary, m.Body)
		}
		if keys := actionKeys(m); len(keys) != 2 || keys[0] != "default" || keys[1] != "mark-read" {
			t.Errorf("%s actions = %v, want [default mark-read] (R6.5)", accepted[i].ID, keys)
		}
	}
}

// TestDecide_DowngradeCriticalSendsNormal is R6.3.
func TestDecide_DowngradeCriticalSendsNormal(t *testing.T) {
	st := savedState()
	n := notice(0, "security", "critical")
	got := byNotice(decideNotifications(&st, []notices.Notice{n}, false, false, config.TrayConfig{DowngradeCritical: true}))
	if m, ok := got[n.ID]; !ok || m.Urgency != 1 {
		t.Errorf("downgraded critical: %+v (sent %v), want urgency 1", m, ok)
	}
}

// TestDecide_RecordsEveryAcceptedNotice: the state learns each accepted
// notice's display fields, unread and not yet notified.
func TestDecide_RecordsEveryAcceptedNotice(t *testing.T) {
	st := savedState()
	n := notice(3, "release", "warning")
	decideNotifications(&st, []notices.Notice{n}, false, false, config.TrayConfig{})
	r, ok := st.Notices[n.ID]
	if !ok {
		t.Fatalf("no record for %s", n.ID)
	}
	if r.Title != n.Title || r.URL != n.URL || r.Severity != n.Severity || r.Type != n.Type || r.Read || r.Notified {
		t.Errorf("record = %+v, want the notice's fields, unread, not notified", r)
	}
}

// TestDecide_MutedTypeIsRecordedUnreadWithoutNotifying is R6.4.
func TestDecide_MutedTypeIsRecordedUnreadWithoutNotifying(t *testing.T) {
	st := savedState()
	muted, other := notice(0, "release", "warning"), notice(1, "announcement", "info")
	got := byNotice(decideNotifications(&st, []notices.Notice{muted, other}, false, false, config.TrayConfig{Mute: []string{"release"}}))
	if _, ok := got[muted.ID]; ok {
		t.Error("a muted type was notified")
	}
	if _, ok := got[other.ID]; !ok {
		t.Error("an unmuted type was not notified")
	}
	if r, ok := st.Notices[muted.ID]; !ok || r.Read {
		t.Errorf("muted notice record = %+v (present %v), want it recorded unread", r, ok)
	}
}

// TestDecide_SecurityCannotBeMuted is R6.13 at the policy.
func TestDecide_SecurityCannotBeMuted(t *testing.T) {
	st := savedState()
	n := notice(0, "security", "warning")
	got := byNotice(decideNotifications(&st, []notices.Notice{n}, false, false, config.TrayConfig{Mute: []string{"security"}}))
	if _, ok := got[n.ID]; !ok {
		t.Error("a security notice was muted")
	}
}

// TestDecide_BurstOfThreeIsIndividual is the boundary of R6.8: three is not
// "more than 3".
func TestDecide_BurstOfThreeIsIndividual(t *testing.T) {
	st := savedState()
	accepted := []notices.Notice{notice(0, "release", "info"), notice(1, "announcement", "info"), notice(2, "news", "warning")}
	got := decideNotifications(&st, accepted, false, false, config.TrayConfig{})
	if len(got) != 3 || len(summaries(got)) != 0 {
		t.Errorf("a burst of 3 produced %d messages (%d summaries), want 3 individual", len(got), len(summaries(got)))
	}
}

// TestDecide_BurstOfFourBecomesOneSummary is R6.8.
func TestDecide_BurstOfFourBecomesOneSummary(t *testing.T) {
	st := savedState()
	accepted := []notices.Notice{notice(0, "release", "info"), notice(1, "announcement", "info"), notice(2, "news", "warning"), notice(3, "release", "warning")}
	got := decideNotifications(&st, accepted, false, false, config.TrayConfig{})
	if len(got) != 1 || len(summaries(got)) != 1 {
		t.Fatalf("a burst of 4 produced %+v, want exactly one summary", got)
	}
	s := got[0]
	if !strings.Contains(s.Summary+" "+s.Body, "4") {
		t.Errorf("summary %q / %q does not state the count 4", s.Summary, s.Body)
	}
	if keys := actionKeys(s); len(keys) != 1 || keys[0] != "default" {
		t.Errorf("summary actions = %v, want only default", keys)
	}
}

// TestDecide_CriticalStaysIndividualInABurst is R6.8's exception, with the
// hostile detail that the summary counts only the non-critical notices.
func TestDecide_CriticalStaysIndividualInABurst(t *testing.T) {
	st := savedState()
	crit := notice(4, "security", "critical")
	accepted := []notices.Notice{notice(0, "release", "info"), notice(1, "announcement", "info"), notice(2, "news", "warning"), notice(3, "release", "warning"), crit}
	got := decideNotifications(&st, accepted, false, false, config.TrayConfig{})
	if len(got) != 2 {
		t.Fatalf("got %d messages, want a summary plus the critical notice: %+v", len(got), got)
	}
	if m, ok := byNotice(got)[crit.ID]; !ok || m.Urgency != 2 {
		t.Errorf("the critical notice was not sent individually at urgency 2: %+v", got)
	}
	s := summaries(got)
	if len(s) != 1 {
		t.Fatalf("summaries = %+v, want one", s)
	}
	text := s[0].Summary + " " + s[0].Body
	if !strings.Contains(text, "4") || strings.Contains(text, "5") {
		t.Errorf("summary %q should count the 4 non-critical notices, not 5", text)
	}
}

// TestDecide_FirstRunNotifiesOnlySecurity is R6.9.
func TestDecide_FirstRunNotifiesOnlySecurity(t *testing.T) {
	st := state.State{Notices: map[string]state.Record{}}
	sec := notice(0, "security", "warning")
	others := []notices.Notice{notice(1, "release", "critical"), notice(2, "announcement", "info"), notice(3, "news", "info")}
	accepted := append([]notices.Notice{sec}, others...)
	got := byNotice(decideNotifications(&st, accepted, true, false, config.TrayConfig{}))
	if _, ok := got[sec.ID]; !ok {
		t.Error("a security notice was not notified on the first run")
	}
	for _, n := range others {
		if _, ok := got[n.ID]; ok {
			t.Errorf("%s (%s) was notified on the first run", n.ID, n.Type)
		}
		if !st.Notices[n.ID].Read {
			t.Errorf("%s was not recorded as read on the first run", n.ID)
		}
	}
	if st.Notices[sec.ID].Read {
		t.Error("the security notice was marked read on the first run")
	}
}

// TestDecide_PausedHoldsAllButCriticalSecurity is R6.10, hostile halves first:
// a critical notice that is not security, and a security notice that is not
// critical, are both held.
func TestDecide_PausedHoldsAllButCriticalSecurity(t *testing.T) {
	st := savedState()
	critRelease, warnSecurity, critSecurity := notice(0, "release", "critical"), notice(1, "security", "warning"), notice(2, "security", "critical")
	got := byNotice(decideNotifications(&st, []notices.Notice{critRelease, warnSecurity, critSecurity}, false, true, config.TrayConfig{}))
	for _, n := range []notices.Notice{critRelease, warnSecurity} {
		if _, ok := got[n.ID]; ok {
			t.Errorf("%s (%s/%s) was sent while paused", n.ID, n.Type, n.Severity)
		}
		if r := st.Notices[n.ID]; r.Read || r.Notified {
			t.Errorf("held notice %s record = %+v, want unread and not notified", n.ID, r)
		}
	}
	if _, ok := got[critSecurity.ID]; !ok {
		t.Error("a critical security notice was held by the pause")
	}
}

// TestDecide_PauseEndReleasesHeldNoticesGrouped is R6.11: the held notices go
// out when the pause ends, grouped as in R6.8.
func TestDecide_PauseEndReleasesHeldNoticesGrouped(t *testing.T) {
	st := savedState()
	accepted := []notices.Notice{notice(0, "release", "info"), notice(1, "announcement", "info"), notice(2, "news", "warning"), notice(3, "release", "warning"), notice(4, "news", "info")}
	if got := decideNotifications(&st, accepted, false, true, config.TrayConfig{}); len(got) != 0 {
		t.Fatalf("paused: %d messages sent, want 0", len(got))
	}
	got := decideNotifications(&st, accepted, false, false, config.TrayConfig{})
	s := summaries(got)
	if len(got) != 1 || len(s) != 1 || !strings.Contains(s[0].Summary+" "+s[0].Body, "5") {
		t.Errorf("after the pause: %+v, want one summary of 5", got)
	}
}

// TestDecide_UnsentNoticeIsRetried is R6.12: until the App marks a notice
// Notified, it is decided again; once marked, it is not repeated, and a notice
// the user already read is never sent.
func TestDecide_UnsentNoticeIsRetried(t *testing.T) {
	st := savedState()
	n := notice(0, "release", "warning")
	for i := 0; i < 2; i++ {
		if _, ok := byNotice(decideNotifications(&st, []notices.Notice{n}, false, false, config.TrayConfig{}))[n.ID]; !ok {
			t.Fatalf("check %d: an unsent notice was not decided again", i+1)
		}
	}
	r := st.Notices[n.ID]
	r.Notified = true
	st.Notices[n.ID] = r
	if got := decideNotifications(&st, []notices.Notice{n}, false, false, config.TrayConfig{}); len(got) != 0 {
		t.Errorf("a notified notice was sent again: %+v", got)
	}

	read := notice(1, "release", "warning")
	st.Notices[read.ID] = state.Record{Title: read.Title, Read: true}
	if _, ok := byNotice(decideNotifications(&st, []notices.Notice{read}, false, false, config.TrayConfig{}))[read.ID]; ok {
		t.Error("a notice the user already read was notified")
	}
}

// TestDecide_StoredPendingRecordIsSentWithoutAFeed: pending notifications
// come from the state, not only from this check's accepted notices. A 304, a
// pause ending on its timer, or a restart must still send a held or failed
// notice, rebuilt from the stored Title and Summary (R6.11, R6.12).
func TestDecide_StoredPendingRecordIsSentWithoutAFeed(t *testing.T) {
	st := savedState()
	st.Notices["2026-10-01-held"] = state.Record{Title: "Held title", Summary: "Held summary",
		URL: "https://obentoo.org/notices/2026-10-01-held/", Severity: "warning", Type: "release"}
	st.Notices["2026-10-01-muted"] = state.Record{Title: "Muted title", Summary: "m", Severity: "info", Type: "announcement"}
	st.Notices["2026-10-01-done"] = state.Record{Title: "Done title", Summary: "d", Severity: "info", Type: "release", Notified: true}
	st.Notices["2026-10-01-seen"] = state.Record{Title: "Seen title", Summary: "s", Severity: "info", Type: "release", Read: true}
	cfg := config.TrayConfig{Mute: []string{"announcement"}}

	if got := decideNotifications(&st, nil, false, true, cfg); len(got) != 0 {
		t.Fatalf("paused, nothing accepted: %+v, want the pending record held", got)
	}
	got := byNotice(decideNotifications(&st, nil, false, false, cfg))
	m, ok := got["2026-10-01-held"]
	if !ok {
		t.Fatalf("a stored pending record was not sent when nothing was accepted: %+v", got)
	}
	if m.Summary != "Held title" || m.Body != "Held summary" || m.Urgency != 1 {
		t.Errorf("rebuilt message = %+v, want the stored title, summary and urgency 1", m)
	}
	for _, id := range []string{"2026-10-01-muted", "2026-10-01-done", "2026-10-01-seen"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s was sent; it is muted, already notified or read", id)
		}
	}
}

// --- viewFor ---

func record(n notices.Notice, read bool) state.Record {
	return state.Record{Title: n.Title, URL: n.URL, Severity: n.Severity, Type: n.Type, Read: read, Updated: n.Updated, LastSeen: policyNow}
}

// TestViewFor_ReadCriticalDoesNotTurnTheIconRed is the hostile half of R8.7,
// authored first: only an UNREAD critical notice makes the view critical.
func TestViewFor_ReadCriticalDoesNotTurnTheIconRed(t *testing.T) {
	st := savedState()
	crit, warn := notice(0, "security", "critical"), notice(1, "release", "warning")
	st.Notices[crit.ID] = record(crit, true)
	st.Notices[warn.ID] = record(warn, false)
	v := viewFor(st)
	if v.Critical {
		t.Error("a read critical notice made the view critical")
	}
	if v.Unread != 1 {
		t.Errorf("Unread = %d, want 1", v.Unread)
	}
}

// TestViewFor_CountsUnreadAndFlagsCritical is R8.4, R8.5 and R8.7.
func TestViewFor_CountsUnreadAndFlagsCritical(t *testing.T) {
	if v := viewFor(savedState()); v.Unread != 0 || v.Critical || len(v.Entries) != 0 || v.More != 0 {
		t.Errorf("empty state: view = %+v, want nothing unread", v)
	}
	st := savedState()
	crit := notice(0, "security", "critical")
	st.Notices[crit.ID] = record(crit, false)
	st.Notices[notice(1, "news", "info").ID] = record(notice(1, "news", "info"), false)
	if v := viewFor(st); v.Unread != 2 || !v.Critical {
		t.Errorf("view = %+v, want Unread 2 and Critical", v)
	}
}

// TestViewFor_ListsTheTenNewestUnreadAndCountsTheRest is R9.1.
func TestViewFor_ListsTheTenNewestUnreadAndCountsTheRest(t *testing.T) {
	st := savedState()
	for i := 0; i < 12; i++ {
		n := notice(i, "release", "info")
		st.Notices[n.ID] = record(n, false)
	}
	read := notice(11, "release", "info")
	read.ID = "2026-12-31-read"
	read.Updated = policyNow.Add(time.Hour) // the newest of all, but read
	st.Notices[read.ID] = record(read, true)

	v := viewFor(st)
	if len(v.Entries) != 10 {
		t.Fatalf("Entries = %d, want 10", len(v.Entries))
	}
	if v.More != 2 {
		t.Errorf("More = %d, want 2", v.More)
	}
	for i, e := range v.Entries {
		want := notice(11-i, "release", "info")
		if e.NoticeID != want.ID || e.Title != want.Title {
			t.Errorf("entry %d = %+v, want %s %q (newest first)", i, e, want.ID, want.Title)
		}
	}
	for _, e := range v.Entries {
		if e.NoticeID == read.ID {
			t.Error("a read notice is listed in the menu")
		}
	}
}
