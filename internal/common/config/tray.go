package config

import (
	"fmt"
	"slices"
	"time"
)

const (
	// DefaultTrayInterval is how often bentoo-tray checks the feed when
	// tray.interval is absent.
	DefaultTrayInterval = 6 * time.Hour
	// MinTrayInterval is the floor tray.interval is clamped to.
	MinTrayInterval = time.Hour
	// DefaultTrayFeedURL is the notices feed bentoo-tray reads by default.
	DefaultTrayFeedURL = "https://obentoo.org/notices.json"
)

// mutableNoticeTypes are the notice types tray.mute may silence. "security" is
// deliberately absent: a security notice can never be muted.
var mutableNoticeTypes = []string{"release", "news", "announcement"}

// TrayConfig is the `tray:` block read by bentoo-tray. The getters
// apply the defaults and return their warnings as strings: this package does
// not log, the caller does.
type TrayConfig struct {
	// Interval is a time.ParseDuration string; empty means DefaultTrayInterval.
	Interval string `yaml:"interval,omitempty"`
	// FeedURL is the notices feed; empty means DefaultTrayFeedURL.
	FeedURL string `yaml:"feed_url,omitempty"`
	// Mute lists notice types that are recorded unread but never notified.
	Mute []string `yaml:"mute,omitempty"`
	// DowngradeCritical sends critical notices at normal urgency.
	DowngradeCritical bool `yaml:"downgrade_critical,omitempty"`
	// SkipMetered is a pointer so an explicit false stays distinct from an
	// absent key, whose default is true.
	SkipMetered *bool `yaml:"skip_metered,omitempty"`
}

// GetInterval returns the check interval: DefaultTrayInterval when unset or
// unparsable, and never below MinTrayInterval. Each correction comes back as a
// warning naming the configured value.
func (t TrayConfig) GetInterval() (time.Duration, []string) {
	if t.Interval == "" {
		return DefaultTrayInterval, nil
	}
	d, err := time.ParseDuration(t.Interval)
	if err != nil {
		return DefaultTrayInterval, []string{fmt.Sprintf(
			"tray.interval %q is not a duration; using %s", t.Interval, DefaultTrayInterval)}
	}
	if d < MinTrayInterval {
		return MinTrayInterval, []string{fmt.Sprintf(
			"tray.interval %q is below the %s minimum; using %s", t.Interval, MinTrayInterval, MinTrayInterval)}
	}
	return d, nil
}

// GetFeedURL returns the configured feed URL or DefaultTrayFeedURL.
func (t TrayConfig) GetFeedURL() string {
	if t.FeedURL == "" {
		return DefaultTrayFeedURL
	}
	return t.FeedURL
}

// GetMute returns the mutable notice types listed in tray.mute, in their
// configured order. "security" and unknown types (matched case-sensitively)
// are dropped, one warning each.
func (t TrayConfig) GetMute() (types []string, warnings []string) {
	for _, typ := range t.Mute {
		switch {
		case typ == "security":
			warnings = append(warnings, "tray.mute: security notices cannot be muted; ignoring \"security\"")
		case !slices.Contains(mutableNoticeTypes, typ):
			warnings = append(warnings, fmt.Sprintf(
				"tray.mute: unknown notice type %q ignored (known: release, news, announcement)", typ))
		default:
			types = append(types, typ)
		}
	}
	return types, warnings
}

// GetSkipMetered reports whether checks are skipped on a metered connection;
// true unless tray.skip_metered is explicitly false.
func (t TrayConfig) GetSkipMetered() bool {
	return t.SkipMetered == nil || *t.SkipMetered
}
