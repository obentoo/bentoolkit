package autoupdate

// Story 087, sub-task 4.5 (R10.2): hostile half of the shared secrets warning.
// A Checker built WITHOUT the new error sink keeps today's behaviour: each one
// logs its own `resolving GitHub token: failed` warning carrying the error. A
// fix that silenced the Checker's warning outright, rather than routing it to
// a caller-provided sink, would pass the cmd/bentoo count and fail here.
//
// Green before the fix by design; it must stay green after it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

const s087d45TokenMsg = "resolving GitHub token: failed; continuing with unauthenticated GitHub API access"

func TestS087_4_5_CheckerWithoutSinkStillWarns(t *testing.T) {
	root := s062IsolateAutoupdate(t)
	// Present but unreadable: a directory where the user secrets file belongs.
	secretsPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bentoo", "secrets")
	if err := os.MkdirAll(secretsPath, 0o750); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	cfg := &registry.PackagesConfig{Packages: map[string]registry.PackageConfig{}}

	for i := range 2 {
		rec := &s062Recorder{}
		if _, err := NewChecker(filepath.Join(root, "overlay"),
			WithConfigDir(filepath.Join(root, fmt.Sprintf("config-%d", i))),
			WithPackagesConfig(cfg),
			WithLogger(rec.logger()),
		); err != nil {
			t.Fatalf("Checker %d: NewChecker: %v", i+1, err)
		}
		var hits []map[string]any
		for _, r := range rec.records(t) {
			if r["level"] == "WARN" && r["msg"] == s087d45TokenMsg {
				hits = append(hits, r)
			}
		}
		if len(hits) != 1 {
			t.Errorf("Checker %d: %d %q warnings, want 1 (default unchanged)\nrecords: %v", i+1, len(hits), s087d45TokenMsg, rec.records(t))
			continue
		}
		errText := fmt.Sprint(hits[0]["err"])
		if !strings.Contains(errText, secrets.ErrUnreadable.Error()) || !strings.Contains(errText, secretsPath) {
			t.Errorf("Checker %d: err = %q, want the unreadable-secrets error naming %s", i+1, errText, secretsPath)
		}
	}
}
