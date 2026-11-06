package autoupdate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Story 069: a packages.toml record that sets min_age_days and published_path
// holds a candidate younger than the age, reading its publication time from
// the same upstream response the version came from (R1.1–R1.4, R2.1, R2.2).
//
// The records are written as TOML and loaded by NewChecker, so the contract
// these tests pin is the two keys the story names, not the Go field names
// behind them. The clock is injected, so the seven-day arithmetic is exact.
//
// The fixtures are npm-shaped on purpose: npm's `time` map is keyed by the
// version, and a version holds dots — "time.{version}" substituted naively
// into a dotted path walks time → "2" → "1" → "310" and finds nothing, which
// a held-for-a-missing-time result would hide. The old-enough tests are the
// ones that catch it, and the neighbouring keys ("2.1.31", "2.1.3100") are
// there so a lookup that matches the wrong key fires in both directions.

const (
	minAgePkg      = "dev-util/claude-code"
	minAgeOtherPkg = "dev-util/claude-code-twin"
	minAgeCurrent  = "2.1.300"
	minAgeLatest   = "2.1.310"
	minAgeOlder    = "2.1.309"
)

// minAgeNow is the "now" every check below runs at unless a test moves it.
var minAgeNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

const minAgeDay = 24 * time.Hour

func minAgeStamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// minAgeBody renders an npm registry document whose dist-tags.latest is
// minAgeLatest. times maps a key of the `time` object to its value; a nil
// value is a JSON null, and a nil map drops the `time` object altogether.
func minAgeBody(t *testing.T, times map[string]any) string {
	t.Helper()
	doc := map[string]any{
		"name":      "@anthropic-ai/claude-code",
		"dist-tags": map[string]any{"latest": minAgeLatest, "next": "2.1.311"},
		"versions": map[string]any{
			minAgeCurrent: map[string]any{"version": minAgeCurrent},
			minAgeOlder:   map[string]any{"version": minAgeOlder},
			minAgeLatest:  map[string]any{"version": minAgeLatest},
		},
	}
	if times != nil {
		doc["time"] = times
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("fixture: marshal registry body: %v", err)
	}
	return string(b)
}

// minAgeTimes is a `time` object with every key the fixtures need: the
// current version and the older release long published, the two prefix
// neighbours of the candidate at the given ages, and the candidate itself.
func minAgeTimes(latest any, neighbours time.Time) map[string]any {
	return map[string]any{
		"created":     minAgeStamp(minAgeNow.Add(-400 * minAgeDay)),
		"modified":    minAgeStamp(minAgeNow.Add(-1 * time.Hour)),
		minAgeCurrent: minAgeStamp(minAgeNow.Add(-60 * minAgeDay)),
		"2.1.31":      minAgeStamp(neighbours),
		"2.1.3100":    minAgeStamp(neighbours),
		minAgeOlder:   minAgeStamp(minAgeNow.Add(-30 * minAgeDay)),
		minAgeLatest:  latest,
	}
}

// minAgeRecord is one packages.toml record over url; gated adds the two keys.
func minAgeRecord(pkg, url string, gated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%q]\nurl = %q\nparser = \"json\"\npath = \"dist-tags.latest\"\n", pkg, url)
	if gated {
		b.WriteString("min_age_days = 7\npublished_path = \"time.{version}\"\n")
	}
	b.WriteString("\n")
	return b.String()
}

func minAgeWriteRegistry(t *testing.T, overlayDir string, records ...string) {
	t.Helper()
	dir := filepath.Join(overlayDir, ".autoupdate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("fixture: mkdir .autoupdate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "packages.toml"), []byte(strings.Join(records, "")), 0o644); err != nil {
		t.Fatalf("fixture: write packages.toml: %v", err)
	}
}

// minAgeLogSink is a log destination the race detector accepts.
type minAgeLogSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *minAgeLogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *minAgeLogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// minAgeRun is one `bentoo overlay autoupdate` run: a new checker over the
// same overlay and state directories, its clock fixed at now.
type minAgeRun struct {
	overlayDir, configDir string
	now                   time.Time
}

// minAgeOutcome is what a run left behind: the check result, the text it
// reported (the result as printed plus every log line), and the pending entry
// for the package as read back from the state directory.
type minAgeOutcome struct {
	result  *CheckResult
	report  string
	pending *PendingUpdate
}

func (r minAgeRun) check(t *testing.T, pkg string, force bool) minAgeOutcome {
	t.Helper()
	logs := &minAgeLogSink{}
	now := r.now
	c, err := NewChecker(r.overlayDir,
		WithConfigDir(r.configDir),
		WithRateLimiter(unlimitedRateLimiter()),
		WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		WithNowFunc(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	result, _ := c.CheckPackage(t.Context(), pkg, force)
	if result == nil {
		t.Fatal("CheckPackage returned no result")
	}
	// Read the queue back from disk: what the next run and the applier see.
	onDisk, err := NewPendingList(r.configDir)
	if err != nil {
		t.Fatalf("pending state unreadable after the check: %v", err)
	}
	out := minAgeOutcome{result: result, report: fmt.Sprintf("%+v\n%s", *result, logs.String())}
	if got, ok := onDisk.Get(pkg); ok {
		cp := *got
		out.pending = &cp
	}
	return out
}

func minAgeSetup(t *testing.T, body string, records func(url string) []string) (*holdServer, minAgeRun) {
	t.Helper()
	srv := newHoldServer(t, http.StatusOK, body)
	overlayDir, configDir := t.TempDir(), t.TempDir()
	createTestEbuild(t, overlayDir, minAgePkg, minAgeCurrent)
	minAgeWriteRegistry(t, overlayDir, records(srv.srv.URL)...)
	return srv, minAgeRun{overlayDir: overlayDir, configDir: configDir, now: minAgeNow}
}

func minAgeGated(url string) []string { return []string{minAgeRecord(minAgePkg, url, true)} }

// expectHeld asserts R1.2: the current version is kept and nothing is queued.
func (o minAgeOutcome) expectHeld(t *testing.T) {
	t.Helper()
	if o.result.CurrentVersion != minAgeCurrent {
		t.Errorf("CurrentVersion = %q, want %q kept", o.result.CurrentVersion, minAgeCurrent)
	}
	if o.pending != nil {
		t.Errorf("a too-young candidate was queued: %+v\nreport:\n%s", *o.pending, o.report)
	}
}

// expectHeldReport asserts R1.2's report line, to the day of both times.
func (o minAgeOutcome) expectHeldReport(t *testing.T, published, eligible time.Time) {
	t.Helper()
	re := regexp.MustCompile(`held by min_age_days: ` + regexp.QuoteMeta(minAgeLatest) +
		` published ` + published.UTC().Format("2006-01-02") + `[^,]*, eligible ` + eligible.UTC().Format("2006-01-02"))
	if !re.MatchString(o.report) {
		t.Errorf("report does not say %q\nreport:\n%s", re.String(), o.report)
	}
}

func (o minAgeOutcome) expectQueued(t *testing.T, version string) {
	t.Helper()
	if o.pending == nil {
		t.Fatalf("nothing queued; want a bump to %s\nreport:\n%s", version, o.report)
	}
	if o.pending.NewVersion != version || o.pending.CurrentVersion != minAgeCurrent {
		t.Errorf("pending = {Current:%q New:%q}, want {%q %q}", o.pending.CurrentVersion, o.pending.NewVersion, minAgeCurrent, version)
	}
	if strings.Contains(o.report, "held by min_age_days") {
		t.Errorf("an old-enough candidate was reported held\nreport:\n%s", o.report)
	}
}

// TestCheckPackage_MinAgeHoldsYoungCandidate pins R1.1 and R1.2: a candidate
// published one day ago is held, the report gives its publication time and
// the time it becomes eligible, and the cache-hit path holds it too.
func TestCheckPackage_MinAgeHoldsYoungCandidate(t *testing.T) {
	published := minAgeNow.Add(-1 * minAgeDay)
	// The neighbours are old: a lookup that resolves "2.1.31" or "2.1.3100"
	// instead of "2.1.310" would read an old time and bump.
	body := minAgeBody(t, minAgeTimes(minAgeStamp(published), minAgeNow.Add(-90*minAgeDay)))

	t.Run("fresh fetch", func(t *testing.T) {
		_, run := minAgeSetup(t, body, minAgeGated)
		out := run.check(t, minAgePkg, true)
		out.expectHeld(t)
		out.expectHeldReport(t, published, published.Add(7*minAgeDay))
	})

	t.Run("the next run, served from the version cache", func(t *testing.T) {
		_, run := minAgeSetup(t, body, minAgeGated)
		run.check(t, minAgePkg, true).expectHeld(t)
		run.now = minAgeNow.Add(time.Minute)
		run.check(t, minAgePkg, false).expectHeld(t)
	})

	t.Run("one second short of seven days", func(t *testing.T) {
		almost := minAgeNow.Add(-7*minAgeDay + time.Second)
		_, run := minAgeSetup(t, minAgeBody(t, minAgeTimes(minAgeStamp(almost), minAgeNow.Add(-90*minAgeDay))), minAgeGated)
		out := run.check(t, minAgePkg, true)
		out.expectHeld(t)
		out.expectHeldReport(t, almost, almost.Add(7*minAgeDay))
	})

	t.Run("published in the future", func(t *testing.T) {
		// A clock skew between npm and this host is a negative age, not an old one.
		future := minAgeNow.Add(1 * minAgeDay)
		_, run := minAgeSetup(t, minAgeBody(t, minAgeTimes(minAgeStamp(future), minAgeNow.Add(-90*minAgeDay))), minAgeGated)
		run.check(t, minAgePkg, true).expectHeld(t)
	})
}

// TestCheckPackage_MinAgeProceedsWhenOldEnough pins R1.3: a candidate at least
// min_age_days old is queued exactly as a record without the keys queues it.
func TestCheckPackage_MinAgeProceedsWhenOldEnough(t *testing.T) {
	// The neighbours are young: a lookup that resolves the wrong key would
	// read a young time and hold.
	young := minAgeNow.Add(-1 * time.Hour)

	t.Run("published long ago", func(t *testing.T) {
		body := minAgeBody(t, minAgeTimes(minAgeStamp(minAgeNow.Add(-15*minAgeDay)), young))
		_, gated := minAgeSetup(t, body, minAgeGated)
		_, plain := minAgeSetup(t, body, func(url string) []string { return []string{minAgeRecord(minAgePkg, url, false)} })

		got := gated.check(t, minAgePkg, true)
		got.expectQueued(t, minAgeLatest)
		want := plain.check(t, minAgePkg, true)
		want.expectQueued(t, minAgeLatest)
		if got.pending != nil && want.pending != nil &&
			(got.pending.CommitHash != want.pending.CommitHash || got.pending.AuxValue != want.pending.AuxValue ||
				got.pending.Status != want.pending.Status || got.result.UpstreamVersion != want.result.UpstreamVersion) {
			t.Errorf("an old-enough gated bump differs from the ungated one\n gated: %+v\nplain: %+v", *got.pending, *want.pending)
		}
	})

	t.Run("exactly seven days", func(t *testing.T) {
		body := minAgeBody(t, minAgeTimes(minAgeStamp(minAgeNow.Add(-7*minAgeDay)), young))
		_, run := minAgeSetup(t, body, minAgeGated)
		run.check(t, minAgePkg, true).expectQueued(t, minAgeLatest)
	})
}

// TestCheckPackage_MinAgeHoldsWhenPublishedTimeUnreadable pins R1.4: no time,
// or one that does not parse, is a too-young candidate, and the report says so.
func TestCheckPackage_MinAgeHoldsWhenPublishedTimeUnreadable(t *testing.T) {
	old := minAgeNow.Add(-90 * minAgeDay)
	withoutLatest := minAgeTimes(nil, old)
	delete(withoutLatest, minAgeLatest)

	cases := map[string]map[string]any{
		"no key for the candidate": withoutLatest,
		"no time object at all":    nil,
		"null":                     minAgeTimes(nil, old),
		"empty string":             minAgeTimes("", old),
		"not a date":               minAgeTimes("yesterday", old),
		"a number":                 minAgeTimes(1761307200, old),
		"an object":                minAgeTimes(map[string]any{"at": minAgeStamp(old)}, old),
	}
	for name, times := range cases {
		t.Run(name, func(t *testing.T) {
			_, run := minAgeSetup(t, minAgeBody(t, times), minAgeGated)
			out := run.check(t, minAgePkg, true)
			out.expectHeld(t)
			if !strings.Contains(out.report, "min_age_days") || !strings.Contains(out.report, minAgeLatest) {
				t.Errorf("report does not say the candidate %s is held by min_age_days\nreport:\n%s", minAgeLatest, out.report)
			}
		})
	}
}

// TestCheckPackage_MinAgeNeverStepsDown pins R2.1: when the latest version is
// too young, an older release newer than the current one is not bumped to,
// however old it is; the run keeps the current version and names the latest.
func TestCheckPackage_MinAgeNeverStepsDown(t *testing.T) {
	published := minAgeNow.Add(-2 * minAgeDay)
	body := minAgeBody(t, minAgeTimes(minAgeStamp(published), minAgeNow.Add(-90*minAgeDay)))
	_, run := minAgeSetup(t, body, minAgeGated)

	out := run.check(t, minAgePkg, true)
	out.expectHeld(t)
	if out.pending != nil && out.pending.NewVersion == minAgeOlder {
		t.Errorf("stepped down to the older eligible %s", minAgeOlder)
	}
	if out.result.UpstreamVersion != minAgeLatest {
		t.Errorf("UpstreamVersion = %q, want the held latest %q reported", out.result.UpstreamVersion, minAgeLatest)
	}
	out.expectHeldReport(t, published, published.Add(7*minAgeDay))
}

// TestCheckPackage_MinAgeBumpsHeldVersionOnLaterRun pins R2.2: the publication
// time does not move, the clock does, and the run that finds the held version
// old enough queues it.
func TestCheckPackage_MinAgeBumpsHeldVersionOnLaterRun(t *testing.T) {
	published := minAgeNow.Add(-1 * minAgeDay)
	body := minAgeBody(t, minAgeTimes(minAgeStamp(published), minAgeNow.Add(-90*minAgeDay)))
	_, run := minAgeSetup(t, body, minAgeGated)

	run.check(t, minAgePkg, true).expectHeld(t)

	run.now = minAgeNow.Add(5 * minAgeDay) // six days old
	run.check(t, minAgePkg, true).expectHeld(t)

	run.now = minAgeNow.Add(6 * minAgeDay) // seven days old
	run.check(t, minAgePkg, true).expectQueued(t, minAgeLatest)
}

// TestCheckPackage_MinAgeIsPerRecord pins R1.5 beside the gate: in one
// registry, the record without the keys bumps the same young candidate the
// gated record holds — the gate neither leaks to it nor is lost to it.
func TestCheckPackage_MinAgeIsPerRecord(t *testing.T) {
	body := minAgeBody(t, minAgeTimes(minAgeStamp(minAgeNow.Add(-1*minAgeDay)), minAgeNow.Add(-90*minAgeDay)))
	_, run := minAgeSetup(t, body, func(url string) []string {
		return []string{minAgeRecord(minAgePkg, url, true), minAgeRecord(minAgeOtherPkg, url, false)}
	})
	createTestEbuild(t, run.overlayDir, minAgeOtherPkg, minAgeCurrent)

	run.check(t, minAgeOtherPkg, true).expectQueued(t, minAgeLatest)
	run.check(t, minAgePkg, true).expectHeld(t)
	// The ungated record's entry survives the gated record's hold.
	if again := run.check(t, minAgeOtherPkg, false); again.pending == nil {
		t.Errorf("the ungated record's bump was dropped by the gated record's hold")
	}
}
