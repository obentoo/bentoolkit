package keywords

import (
	"bytes"
	"testing"
)

func TestRead(t *testing.T) {
	for name, tc := range map[string]struct {
		src                 string
		value               string
		singleLine, present bool
	}{
		// Hostile first: lines that look like the assignment but are not.
		"commented assignment is ignored": {src: "#KEYWORDS=\"amd64\"\n", present: false},
		"prefixed variable is ignored":    {src: "MY_KEYWORDS=\"amd64\"\n", present: false},
		"full value, not its first word":  {src: "KEYWORDS=\"~amd64 ~arm64\"\n", value: "~amd64 ~arm64", singleLine: true, present: true},
		"indented inside a branch":        {src: "else\n\tKEYWORDS=\"amd64 x86\"\nfi\n", value: "amd64 x86", singleLine: true, present: true},
		"absent":                          {src: "EAPI=8\nSLOT=\"0\"\n", present: false},
		"multi-line value":                {src: "KEYWORDS=\"amd64\n\tarm64\"\n", present: true},
		"computed value":                  {src: "KEYWORDS=\"${MY_KW}\"\n", present: true},
		"two assignments are ambiguous":   {src: "KEYWORDS=\"\"\nKEYWORDS=\"amd64\"\n", present: true},
		"empty single-line":               {src: "KEYWORDS=\"\"\n", value: "", singleLine: true, present: true},
	} {
		t.Run(name, func(t *testing.T) {
			value, singleLine, present := Read([]byte(tc.src))
			if value != tc.value || singleLine != tc.singleLine || present != tc.present {
				t.Errorf("Read(%q) = (%q, %v, %v), want (%q, %v, %v)",
					tc.src, value, singleLine, present, tc.value, tc.singleLine, tc.present)
			}
		})
	}
}

func TestRewrite(t *testing.T) {
	for name, tc := range map[string]struct {
		src, want string
		changed   bool
	}{
		"commented and prefixed lines stay byte-identical": {
			src:  "#KEYWORDS=\"amd64\"\nMY_KEYWORDS=\"amd64\"\n",
			want: "#KEYWORDS=\"amd64\"\nMY_KEYWORDS=\"amd64\"\n",
		},
		"keeps indentation": {
			src:     "else\n\tKEYWORDS=\"amd64 ~arm64\"\nfi\n",
			want:    "else\n\tKEYWORDS=\"~amd64\"\nfi\n",
			changed: true,
		},
		"already the target": {
			src:  "KEYWORDS=\"~amd64\"\n",
			want: "KEYWORDS=\"~amd64\"\n",
		},
		"multi-line is never edited": {
			src:  "KEYWORDS=\"amd64\n\tarm64\"\n",
			want: "KEYWORDS=\"amd64\n\tarm64\"\n",
		},
		"two assignments are never edited": {
			src:  "KEYWORDS=\"\"\nKEYWORDS=\"amd64\"\n",
			want: "KEYWORDS=\"\"\nKEYWORDS=\"amd64\"\n",
		},
		"absent stays absent": {
			src:  "EAPI=8\n",
			want: "EAPI=8\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, changed := Rewrite([]byte(tc.src), "~amd64")
			if !bytes.Equal(got, []byte(tc.want)) || changed != tc.changed {
				t.Errorf("Rewrite(%q) = (%q, %v), want (%q, %v)", tc.src, got, changed, tc.want, tc.changed)
			}
		})
	}
}
