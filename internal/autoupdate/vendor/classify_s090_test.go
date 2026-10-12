package vendor

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

func TestParseSrcURI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Fetchable
	}{
		{
			name: "rename takes the name after the arrow",
			in:   "https://github.com/golang/vuln/archive/refs/tags/v1.9.0.tar.gz -> govulncheck-1.9.0.tar.gz",
			want: []Fetchable{{URI: "https://github.com/golang/vuln/archive/refs/tags/v1.9.0.tar.gz", Name: "govulncheck-1.9.0.tar.gz"}},
		},
		{
			name: "plain URI is named by its last path segment",
			in:   "https://distfiles.obentoo.org/govulncheck-1.9.0-vendor.tar.xz",
			want: []Fetchable{{URI: "https://distfiles.obentoo.org/govulncheck-1.9.0-vendor.tar.xz", Name: "govulncheck-1.9.0-vendor.tar.xz"}},
		},
		{
			name: "USE-conditional, negated and any-of groups contribute every member",
			in: "amd64? ( https://e.org/a-1.tar.gz ) !doc? ( https://e.org/b-1.tar.gz -> c-1.tar.gz ) " +
				"|| ( https://e.org/d-1.tar.gz )",
			want: []Fetchable{
				{URI: "https://e.org/a-1.tar.gz", Name: "a-1.tar.gz"},
				{URI: "https://e.org/b-1.tar.gz", Name: "c-1.tar.gz"},
				{URI: "https://e.org/d-1.tar.gz", Name: "d-1.tar.gz"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSrcURI(tc.in)
			if err != nil {
				t.Fatalf("ParseSrcURI: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ParseSrcURI(%q)\n got %+v\nwant %+v", tc.in, got, tc.want)
			}
		})
	}
}

const (
	s090Src    = "https://github.com/golang/vuln/archive/refs/tags/v1.9.0.tar.gz -> govulncheck-1.9.0.tar.gz"
	s090Vendor = "https://distfiles.obentoo.org/govulncheck-1.9.0-vendor.tar.xz"
)

func s090Entry(inherit, srcURI string) string {
	return "DESCRIPTION=Reports known vulnerabilities\nEAPI=8\nINHERIT=" + inherit + "\nSLOT=0\nSRC_URI=" + srcURI +
		"\n_md5_=3553763dd1811d0953643015cbf12601\n"
}

var s090Default = []string{"distfiles.obentoo.org"}

// R1.3 hostile halves first: inputs a loose rule would wrongly accept.
func TestClassifyRefusesNearMisses(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		hosts []string
	}{
		{"go-module only as a substring of another eclass", s090Entry("go-module-extra", s090Src+" "+s090Vendor), s090Default},
		{"go-module named only outside INHERIT", "DESCRIPTION=a go-module tool\nINHERIT=cargo\nSRC_URI=" + s090Src + " " + s090Vendor + "\n", s090Default},
		{"two vendor tarballs", s090Entry("go-module", s090Src+" "+s090Vendor+" https://distfiles.obentoo.org/other-1-vendor.tar.xz"), s090Default},
		{"two source distfiles", s090Entry("go-module", s090Src+" https://e.org/extra-1.tar.gz "+s090Vendor), s090Default},
		{"host is a longer name ending like the listed one", s090Entry("go-module", s090Src+" https://evil-distfiles.obentoo.org/govulncheck-1.9.0-vendor.tar.xz"), s090Default},
		{"listed host is only a prefix of the real host", s090Entry("go-module", s090Src+" https://distfiles.obentoo.org.evil.example/govulncheck-1.9.0-vendor.tar.xz"), s090Default},
		{"listed host only in the userinfo", s090Entry("go-module", s090Src+" https://distfiles.obentoo.org@evil.example/govulncheck-1.9.0-vendor.tar.xz"), s090Default},
		{"listed host only in the path", s090Entry("go-module", s090Src+" https://evil.example/distfiles.obentoo.org/govulncheck-1.9.0-vendor.tar.xz"), s090Default},
		{"vendor on a host the operator replaced", s090Entry("go-module", s090Src+" "+s090Vendor), []string{"mirror.example.org"}},
		{"vendor suffix but not xz", s090Entry("go-module", s090Src+" https://distfiles.obentoo.org/govulncheck-1.9.0-vendor.tar.gz"), s090Default},
		{"no vendor distfile", s090Entry("go-module", s090Src), s090Default},
		{"no SRC_URI line", "EAPI=8\nINHERIT=go-module\n", s090Default},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok, _ := Classify(strings.NewReader(tc.entry), tc.hosts)
			if ok {
				t.Fatalf("Classify accepted it as a Go vendor candidate: %+v", c)
			}
		})
	}
}

// R1.1 converse: differently shaped entries that ARE candidates.
func TestClassifyAcceptsEquivalentShapes(t *testing.T) {
	want := Candidate{
		SourceFile: "govulncheck-1.9.0.tar.gz",
		SourceURI:  "https://github.com/golang/vuln/archive/refs/tags/v1.9.0.tar.gz",
		VendorFile: "govulncheck-1.9.0-vendor.tar.xz",
		VendorURI:  s090Vendor,
	}
	cases := []struct {
		name  string
		entry string
		hosts []string
		want  Candidate
	}{
		{"real govulncheck shape", s090Entry("go-module", s090Src+" "+s090Vendor), s090Default, want},
		{"go-module among other eclasses", s090Entry("shell-completion go-module systemd", s090Src+" "+s090Vendor), s090Default, want},
		{"vendor listed before the source", s090Entry("go-module", s090Vendor+" "+s090Src), s090Default, want},
		{"host spelled in another case", s090Entry("go-module", s090Src+" https://DISTFILES.obentoo.org/govulncheck-1.9.0-vendor.tar.xz"), s090Default,
			Candidate{SourceFile: want.SourceFile, SourceURI: want.SourceURI, VendorFile: want.VendorFile, VendorURI: "https://DISTFILES.obentoo.org/govulncheck-1.9.0-vendor.tar.xz"}},
		{"vendor renamed by an arrow", s090Entry("go-module", s090Src+" https://distfiles.obentoo.org/x/v.tar.xz -> govulncheck-1.9.0-vendor.tar.xz"), s090Default,
			Candidate{SourceFile: want.SourceFile, SourceURI: want.SourceURI, VendorFile: want.VendorFile, VendorURI: "https://distfiles.obentoo.org/x/v.tar.xz"}},
		{"operator-configured host", s090Entry("go-module", s090Src+" https://mirror.example.org/govulncheck-1.9.0-vendor.tar.xz"), []string{"mirror.example.org"},
			Candidate{SourceFile: want.SourceFile, SourceURI: want.SourceURI, VendorFile: want.VendorFile, VendorURI: "https://mirror.example.org/govulncheck-1.9.0-vendor.tar.xz"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok, err := Classify(strings.NewReader(tc.entry), tc.hosts)
			if err != nil || !ok {
				t.Fatalf("Classify = (%+v, %v, %v), want a candidate", c, ok, err)
			}
			if c != tc.want {
				t.Fatalf("Classify candidate\n got %+v\nwant %+v", c, tc.want)
			}
		})
	}
}

// R1.5 at the library: an unreadable entry is an error, never a candidate.
func TestClassifyUnreadableEntryIsAnError(t *testing.T) {
	boom := errors.New("disk said no")
	_, ok, err := Classify(iotest.ErrReader(boom), s090Default)
	if ok || err == nil {
		t.Fatalf("Classify(unreadable) = ok %v, err %v; want ok=false and an error", ok, err)
	}
}
