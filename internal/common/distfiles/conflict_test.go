package distfiles

import "testing"

func TestManifestDistConflicts(t *testing.T) {
	const good = "DIST zip2-3.4.5.tar.gz 55897113 BLAKE2B aa SHA512 bb"
	cases := []struct {
		name              string
		published, staged []byte
		want              []string
	}{
		{"identical record is no conflict", s084Body(good), s084Body(good), nil},
		{"same record laid out differently is no conflict",
			s084Body("\tDIST  zip2-3.4.5.tar.gz\t55897113 BLAKE2B aa SHA512 bb"), s084Body(good), nil},
		{"new filename is no conflict", s084Body(good), s084Body("DIST zip2-3.4.6.tar.gz 1 BLAKE2B cc SHA512 dd"), nil},
		{"staged lists a subset of the published digests",
			s084Body(good), s084Body("DIST zip2-3.4.5.tar.gz 55897113 BLAKE2B aa"), nil},
		{"truncated copy: smaller size and other digests",
			s084Body(good), s084Body("DIST zip2-3.4.5.tar.gz 41772565 BLAKE2B xx SHA512 yy"), []string{"zip2-3.4.5.tar.gz"}},
		{"same size, one digest differs",
			s084Body(good), s084Body("DIST zip2-3.4.5.tar.gz 55897113 BLAKE2B aa SHA512 zz"), []string{"zip2-3.4.5.tar.gz"}},
		{"no digest algorithm in common",
			s084Body(good), s084Body("DIST zip2-3.4.5.tar.gz 55897113 SHA256 aa"), []string{"zip2-3.4.5.tar.gz"}},
		{"second published duplicate differs",
			s084Body(good, "DIST zip2-3.4.5.tar.gz 1 BLAKE2B q SHA512 q"), s084Body(good), []string{"zip2-3.4.5.tar.gz"}},
		{"published record with no usable filename is ignored",
			s084Body("DIST ../zip2-3.4.5.tar.gz 1 BLAKE2B q SHA512 q"), s084Body("DIST ../zip2-3.4.5.tar.gz 2 BLAKE2B r SHA512 r"), nil},
		{"empty sides", nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ManifestDistConflicts(c.published, c.staged)
			if len(got) != len(c.want) {
				t.Fatalf("got %d conflicts %+v, want %v", len(got), got, c.want)
			}
			for i, name := range c.want {
				if got[i].Name != name {
					t.Errorf("conflict %d names %q, want %q", i, got[i].Name, name)
				}
				if got[i].Published == "" || got[i].Staged == "" {
					t.Errorf("conflict %d does not carry both records: %+v", i, got[i])
				}
			}
		})
	}
}
