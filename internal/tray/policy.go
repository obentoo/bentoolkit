// Package tray is bentoo-tray's event loop and its notification and icon
// policy (S072-R2, R6, R8, R9).
package tray

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/desktop/sni"
	"github.com/obentoo/bentoolkit/internal/notices"
	"github.com/obentoo/bentoolkit/internal/tray/messages"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// Notification action keys reported back in ActionInvoked (R6.5).
const (
	actionDefault  = "default"
	actionMarkRead = "mark-read"
)

// Notice types and severities the policy distinguishes.
const (
	typeSecurity     = "security"
	severityInfo     = "info"
	severityCritical = "critical"
)

// Notification urgencies (the spec's urgency hint).
const (
	urgencyLow      byte = 0
	urgencyNormal   byte = 1
	urgencyCritical byte = 2
)

// burstLimit is the most non-critical notices sent one by one in a check;
// more become a single summary (R6.8).
const burstLimit = 3

// menuEntries is how many unread notices the menu lists (R9.1).
const menuEntries = 10

// acceptFeed reports whether a fetched feed may replace the current notices.
// A serial lower than the last accepted one is a rollback (R2.4); an equal
// serial is the same build fetched again and is accepted. A feed whose
// expiry has passed is stale (R2.5). reason names the offending values for
// the WARN the caller logs.
func acceptFeed(st state.State, f notices.Feed, now time.Time) (ok bool, reason string) {
	if f.Serial < st.Serial {
		return false, fmt.Sprintf("feed serial %d is lower than the last accepted serial %d", f.Serial, st.Serial)
	}
	if !f.Expires.After(now) {
		return false, fmt.Sprintf("feed expired at %s", f.Expires.UTC().Format(time.RFC3339))
	}
	return true, ""
}

// decideNotifications records every accepted notice in st and returns the
// notifications to send now.
//
// Each accepted notice's display fields are upserted, keeping its Read and
// Notified flags. On a first run every accepted notice that is not security is
// marked read (R6.9). The messages are then built from the state, not from
// accepted alone: every record neither Read nor Notified, whose type is not
// muted (R6.4) and that the pause does not hold (R6.10), is pending, so a
// pause end, a 304 or a restart still sends what was held or failed (R6.11,
// R6.12). Critical notices are sent one by one; more than burstLimit others
// become one summary with an empty NoticeID whose Covers lists, sorted, the
// IDs it stands for (R6.8).
//
// It never sets Notified: the caller does after a successful Send, for the
// message's NoticeID or, for a summary, for every ID in Covers. A failed Send
// leaves them pending, which is what retries it (R6.12).
func decideNotifications(st *state.State, accepted []notices.Notice, firstRun, paused bool, cfg config.TrayConfig) []notify.Message {
	if st.Notices == nil {
		st.Notices = map[string]state.Record{}
	}
	for _, n := range accepted {
		r := st.Notices[n.ID]
		r.Title, r.Summary, r.URL = n.Title, n.Summary, n.URL
		r.Severity, r.Type, r.Updated = n.Severity, n.Type, n.Updated
		if firstRun && n.Type != typeSecurity {
			r.Read = true
		}
		st.Notices[n.ID] = r
	}

	// GetMute already drops "security" (R6.13); its warnings are run's to log.
	muted, _ := cfg.GetMute()
	var critical, other []string
	for _, id := range sortedIDs(st.Notices, false) {
		r := st.Notices[id]
		switch {
		case r.Read || r.Notified, slices.Contains(muted, r.Type):
			continue
		case paused && (r.Type != typeSecurity || r.Severity != severityCritical):
			continue
		case r.Severity == severityCritical:
			critical = append(critical, id)
		default:
			other = append(other, id)
		}
	}

	msgs := make([]notify.Message, 0, len(critical)+len(other))
	for _, id := range critical {
		msgs = append(msgs, noticeMessage(id, st.Notices[id], cfg.DowngradeCritical))
	}
	if len(other) > burstLimit {
		urgency := urgencyLow
		for _, id := range other {
			urgency = max(urgency, urgencyFor(st.Notices[id].Severity, cfg.DowngradeCritical))
		}
		return append(msgs, notify.Message{
			Summary: messages.Format(messages.SummaryNewNotices, len(other)),
			Urgency: urgency,
			Actions: []notify.Action{{Key: actionDefault, Label: messages.Text(messages.ActionOpen)}},
			Covers:  slices.Sorted(slices.Values(other)),
		})
	}
	for _, id := range other {
		msgs = append(msgs, noticeMessage(id, st.Notices[id], cfg.DowngradeCritical))
	}
	return msgs
}

// noticeMessage is the notification of one notice: its title as summary and
// its summary as body (R6.1), with Open and Mark as read (R6.5). Escaping is
// notify.Send's job.
func noticeMessage(id string, r state.Record, downgrade bool) notify.Message {
	return notify.Message{
		NoticeID: id,
		Summary:  r.Title,
		Body:     r.Summary,
		Urgency:  urgencyFor(r.Severity, downgrade),
		Actions: []notify.Action{
			{Key: actionDefault, Label: messages.Text(messages.ActionOpen)},
			{Key: actionMarkRead, Label: messages.Text(messages.ActionMarkRead)},
		},
	}
}

// urgencyFor maps a severity to an urgency (R6.2); downgrade sends critical
// at normal urgency (R6.3).
func urgencyFor(severity string, downgrade bool) byte {
	switch severity {
	case severityInfo:
		return urgencyLow
	case severityCritical:
		if downgrade {
			return urgencyNormal
		}
		return urgencyCritical
	default:
		return urgencyNormal
	}
}

// viewFor is what the tray shows for st: the unread count, whether an unread
// notice is critical (R8.4, R8.5, R8.7), the menuEntries newest unread
// notices with how many more exist (R9.1), and whether a pause is set.
func viewFor(st state.State) sni.View {
	v := sni.View{Paused: !st.PauseUntil.IsZero()}
	for _, id := range sortedIDs(st.Notices, true) {
		r := st.Notices[id]
		if r.Read {
			continue
		}
		v.Unread++
		v.Critical = v.Critical || r.Severity == severityCritical
		if len(v.Entries) < menuEntries {
			v.Entries = append(v.Entries, sni.MenuEntry{NoticeID: id, Title: r.Title})
		} else {
			v.More++
		}
	}
	return v
}

// sortedIDs returns the IDs of records ordered by Updated, oldest first or,
// with newestFirst, newest first; equal times fall back to the ID so the
// order never depends on map iteration.
func sortedIDs(records map[string]state.Record, newestFirst bool) []string {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		c := records[a].Updated.Compare(records[b].Updated)
		if newestFirst {
			c = -c
		}
		return cmp.Or(c, cmp.Compare(a, b))
	})
	return ids
}
