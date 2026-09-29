package main

// Story 071, sub-task 9.2 (validation fixes, round 1): `notice revise` with no
// notice.site_path has no site YAML to show — it must say the site notice was
// not updated, not point at a YAML block it did not print (R4.2, R6.2).

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNoticeReviseNoSite_SaysTheSiteWasNotUpdated(t *testing.T) {
	e := noticeEnvSetupS071(t, false)
	if stdout, stderr, code := e.c.Run(securityArgsS071(e)...); code != 0 {
		t.Fatalf("notice new: exit %d\n%s\n%s", code, stdout, stderr)
	}
	id := "2026-09-28-foo-cve"
	stdout, stderr, code := e.c.Run("notice", "revise", id, "--title", "foo 1.2 heap overflow, fixed", "--body-file", e.bodyFile)
	if code != 0 {
		t.Fatalf("notice revise: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if want := filepath.Join(e.c.Overlay(), "metadata", "news", id, id+".en.txt"); !strings.Contains(stdout, want) {
		t.Errorf("stdout does not print the revised news item %s:\n%s", want, stdout)
	}
	if strings.Contains(stdout, "save this as") || strings.Contains(stdout, "YAML above") {
		t.Errorf("stdout points at a YAML block that revise did not print:\n%s", stdout)
	}
	if !strings.Contains(stdout, "not updated") {
		t.Errorf("stdout does not say the site notice was not updated:\n%s", stdout)
	}
}
