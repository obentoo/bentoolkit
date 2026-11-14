package validate

import (
	"strings"
	"testing"
	"time"
)

func TestParseBuildHistory(t *testing.T) {
	out := []byte(`dev-lang/rust: 570 average for 23 merges
sys-libs/zlib: 7 average for 1 merge
www-client/chromium: 6269 average for 29 merges
www-client/chromium: 2 average for 2 unmerges
garbage line
app-misc/odd: 1′30″ average for 2 merges
`)
	got := ParseBuildHistory(out)
	want := map[string]BuildEstimate{
		"dev-lang/rust":       {570 * time.Second, 23},
		"sys-libs/zlib":       {7 * time.Second, 1},
		"www-client/chromium": {6269 * time.Second, 29},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %+v, want %+v (an unmerge line must not overwrite a merge line)", k, got[k], v)
		}
	}
}

// TestBuildBudget_Cap: only a depth that compiles is capped; above the budget
// or with no history it drops to configure and says why, within it stays.
func TestBuildBudget_Cap(t *testing.T) {
	b := BuildBudget{Limit: 30 * time.Minute, History: map[string]BuildEstimate{
		"www-client/chromium": {104 * time.Minute, 29},
		"sys-libs/zlib":       {7 * time.Second, 4},
	}}
	for _, tc := range []struct {
		pkg       string
		depth     Depth
		wantDepth Depth
		wantWords string
	}{
		{"www-client/chromium", DepthInstall, DepthConfigure, "averages 1h44m0s over 29 merge(s)"},
		{"www-client/chromium:0", DepthCompile, DepthConfigure, "capped at configure"},
		{"sys-libs/zlib@stable", DepthInstall, DepthInstall, "within the 30m0s build budget"},
		{"app-misc/new", DepthCompile, DepthConfigure, "no build history"},
		{"www-client/chromium", DepthConfigure, DepthConfigure, ""},
	} {
		got := b.Cap(tc.pkg, DepthDecision{Depth: tc.depth, Reason: "policy"})
		if got.Depth != tc.wantDepth || !strings.Contains(got.Reason, tc.wantWords) || got.SkippedByPolicy {
			t.Errorf("Cap(%s, %s) = %s %q skipped=%v; want %s naming %q, not skipped by policy",
				tc.pkg, tc.depth, got.Depth, got.Reason, got.SkippedByPolicy, tc.wantDepth, tc.wantWords)
		}
	}
	if got := (BuildBudget{}).Cap("app-misc/new", DepthDecision{Depth: DepthInstall, Reason: "policy"}); got.Depth != DepthInstall || got.Reason != "policy" {
		t.Errorf("no budget changed the decision: %+v", got)
	}
}
