package autoupdate

import "testing"

func TestApplyTransforms(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		rules [][]string
		want  string
	}{
		{"nil rules is identity", "7.1.2-24", nil, "7.1.2-24"},
		{"imagemagick dash to dot", "7.1.2-24", [][]string{{"-", "."}}, "7.1.2.24"},
		{
			"godot suffix mapping",
			"4.3-beta1",
			[][]string{{"-stable", ""}, {"-beta", "_beta"}, {"-rc", "_rc"}, {"-dev", "_alpha"}},
			"4.3_beta1",
		},
		{
			"godot stable becomes bare",
			"4.3-stable",
			[][]string{{"-stable", ""}, {"-beta", "_beta"}},
			"4.3",
		},
		{"rules applied in order", "a", [][]string{{"a", "b"}, {"b", "c"}}, "c"},
		{"wrong arity rule skipped", "7-1", [][]string{{"-"}, {"-", "."}}, "7.1"},
		{"regex replacement with group", "v1-2", [][]string{{`(\d)-(\d)`, "$1.$2"}}, "v1.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applyTransforms(tt.in, tt.rules); got != tt.want {
				t.Fatalf("applyTransforms(%q, %v) = %q, want %q", tt.in, tt.rules, got, tt.want)
			}
		})
	}
}

func TestApplyTransforms_BadRegexWarnsAndSkips(t *testing.T) {
	lc := captureWarnLogs(t)
	// "[" is an invalid regex: it must be skipped, the valid rule still applies.
	got := applyTransforms("7-1", [][]string{{"[", "X"}, {"-", "."}})
	if got != "7.1" {
		t.Fatalf("got %q, want %q", got, "7.1")
	}
	if len(lc.all()) == 0 {
		t.Fatalf("expected a warning for the bad regex, got none")
	}
}

func TestSelectVersion(t *testing.T) {
	tests := []struct {
		name      string
		cands     []string
		transform [][]string
		mode      string
		want      string
	}{
		{
			name:  "gn max picks highest, not first",
			cands: []string{"0.2122", "0.2200", "0.2374", "0.2300"},
			mode:  "max",
			want:  "0.2374",
		},
		{
			name:  "last picks last comparable",
			cands: []string{"1.0", "1.2", "1.1"},
			mode:  "last",
			want:  "1.1",
		},
		{
			name:  "non-comparable candidates skipped",
			cands: []string{"latest", "1.4", "nightly"},
			mode:  "max",
			want:  "1.4",
		},
		{
			name:  "no comparable candidate yields empty",
			cands: []string{"latest", "nightly"},
			mode:  "max",
			want:  "",
		},
		{
			// Critical ordering: "7.1.2-24" is NOT a valid Gentoo version, so
			// without the transform every candidate would be discarded. The
			// transform must run per-candidate BEFORE validation/comparison.
			name:      "transform runs before validation",
			cands:     []string{"7.1.2-23", "7.1.2-24"},
			transform: [][]string{{"-", "."}},
			mode:      "max",
			want:      "7.1.2.24",
		},
		{
			name:  "v prefix is stripped before compare",
			cands: []string{"v1.2", "v1.10"},
			mode:  "max",
			want:  "1.10",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectVersion(tt.cands, tt.transform, tt.mode); got != tt.want {
				t.Fatalf("selectVersion(%v, %v, %q) = %q, want %q",
					tt.cands, tt.transform, tt.mode, got, tt.want)
			}
		})
	}
}
