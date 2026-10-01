// Package notices holds the bentoo notices feed contract: the types a notice
// is carried in, and the parser that turns the site's JSON Feed 1.1 document
// into them.
//
// The validation here is the subset the tray needs to match packages safely.
// It is deliberately more lenient than the site, which enforces text lengths
// and control characters at the producer; notification text is escaped by the
// notifier anyway, so strings are carried verbatim.
package notices

import (
	"errors"
	"fmt"
	"time"
)

// Source tells where a Notice came from.
type Source uint8

const (
	// SourceFeed is a notice read from the site's JSON Feed.
	SourceFeed Source = iota + 1
	// SourceNews is a notice read from the portage news of the bentoo
	// repository, the offline source.
	SourceNews
)

// String names the source for logs.
func (s Source) String() string {
	switch s {
	case SourceFeed:
		return "feed"
	case SourceNews:
		return "news"
	default:
		return fmt.Sprintf("Source(%d)", uint8(s))
	}
}

// Range is one version bound of an affects entry: Op is one of < <= = >= >
// and Ver a PMS version.
type Range struct{ Op, Ver string }

// Affects names a package a notice is about. Slot is empty when the notice
// covers every slot; Ranges are ANDed, and an empty list covers every version.
type Affects struct {
	CP, Slot string
	Ranges   []Range
}

// Notice is one validated notice.
type Notice struct {
	ID, Title, Summary, URL, Type, Severity string
	Affects                                 []Affects
	Published, Updated                      time.Time
	Source                                  Source
}

// Feed is a validated notices feed: its serial, the instant it expires and
// its notices in document order.
type Feed struct {
	Serial  int64
	Expires time.Time
	Items   []Notice
}

// ErrInvalidFeed is wrapped by every error ParseFeed returns: the document is
// not a bentoo notices feed, or one of its items breaks the contract.
var ErrInvalidFeed = errors.New("invalid notices feed")

// ItemError reports an item that breaks the feed contract: ID is the item's id
// as written (possibly empty or itself the invalid value), Field the failing
// field's path inside the item (for example "affects[0].ranges[1].ver") and
// Reason what is wrong with it. It wraps ErrInvalidFeed.
type ItemError struct {
	ID, Field, Reason string
}

// Error quotes the ID and the field, because both can come from the network.
func (e *ItemError) Error() string {
	return fmt.Sprintf("%v: item %q: field %q: %s", ErrInvalidFeed, e.ID, e.Field, e.Reason)
}

// Unwrap makes errors.Is(err, ErrInvalidFeed) hold for every item error.
func (e *ItemError) Unwrap() error { return ErrInvalidFeed }
