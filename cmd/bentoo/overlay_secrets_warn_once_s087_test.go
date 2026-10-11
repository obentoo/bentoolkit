package main

// Story 087, sub-task 4.2 (R6.3, R10.7): an unreadable secrets file is warned
// about exactly once per `overlay compare` or `overlay autoupdate` run, naming
// the file and the error, whatever the number of configured repositories and
// of code paths that read the file (the per-repository token lookup and the
// GitHub token lookup included).
//
// The count does not depend on the wording: it is the number of WARN records
// whose text cites the unreadable secrets file. It must be exactly one with one
// configured repository and with three, and a second run in the same process
// must warn again (the warning is keyed by run, not by process).
//
// Hermetic: realignSetup puts HOME, XDG_CONFIG_HOME and PATH under t.TempDir;
// the unreadable user secrets file is a directory at
// $XDG_CONFIG_HOME/bentoo/secrets; every token variable the run reads is
// blanked. Every repository is `provider: local`, so nothing is downloaded.

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

const (
	s087d42Unreadable = "secrets: file present but unreadable"
	s087d42PerRepoMsg = `msg="resolving token for repository: failed; treating it as unset"`
)

var s087d42ThreeRepos = []string{"gentoo", "guru", "s087-third"}

// s087d42Buffer is a log sink safe under -race.
type s087d42Buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *s087d42Buffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *s087d42Buffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func s087d42OutputFlags(t *testing.T) {
	t.Helper()
	q, v, nc := quiet, verbose, noColor
	t.Cleanup(func() { quiet, verbose, noColor = q, v, nc })
	quiet, verbose, noColor = false, false, true
}

// s087d42BlankTokens blanks every token variable a run over repos could read,
// so only the (unreadable) secrets file is consulted.
func s087d42BlankTokens(t *testing.T, repos []string) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	for _, r := range repos {
		t.Setenv(repoTokenName(r), "")
	}
}

// s087d42UnreadableSecrets makes the user secrets file present but unreadable.
func s087d42UnreadableSecrets(t *testing.T) string {
	t.Helper()
	p := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bentoo", "secrets")
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	return p
}

// s087d42WriteConfig rewrites config.yaml with every name in repos as a local
// repository over gentooPath.
func s087d42WriteConfig(t *testing.T, fx realignFixture, repos []string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("overlay:\n  path: " + fx.overlayPath + "\n  remote: origin\n")
	b.WriteString("git:\n  user: Test\n  email: test@test.com\n")
	b.WriteString("repositories:\n")
	for _, r := range repos {
		b.WriteString("  " + r + ":\n    provider: local\n    path: " + fx.gentooPath + "\n")
	}
	p := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bentoo", "config.yaml")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// s087d42SecretsWarnings returns the WARN records that cite the unreadable
// secrets file.
func s087d42SecretsWarnings(logs string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.HasPrefix(line, "level=WARN ") && strings.Contains(line, s087d42Unreadable) {
			out = append(out, line)
		}
	}
	return out
}

// s087d42ExactlyOne fails unless logs hold exactly one warning about the
// unreadable secrets file, and that warning names the file and carries the
// error.
func s087d42ExactlyOne(t *testing.T, label, logs, secretsPath string) {
	t.Helper()
	recs := s087d42SecretsWarnings(logs)
	if len(recs) != 1 {
		t.Errorf("%s: %d warnings about the unreadable secrets file, want exactly 1\nlogs:\n%s", label, len(recs), logs)
		return
	}
	if !strings.Contains(recs[0], secretsPath) || !strings.Contains(recs[0], " err=") {
		t.Errorf("%s: the warning does not name the file %q and carry the error as err: %q", label, secretsPath, recs[0])
	}
	if n := strings.Count(logs, s087d42PerRepoMsg); n > 1 {
		t.Errorf("%s: per-repository token warnings = %d, want at most 1", label, n)
	}
}

// s087d42Compare runs `overlay compare` (default repository gentoo) runs times
// over a world configured with repos and an unreadable secrets file.
func s087d42Compare(t *testing.T, repos []string, runs int) ([]s086Result, string) {
	t.Helper()
	fx := s086World(t)
	s087d42OutputFlags(t)
	s087d42BlankTokens(t, repos)
	s087d42WriteConfig(t, fx, repos)
	secretsPath := s087d42UnreadableSecrets(t)
	out := make([]s086Result, 0, runs)
	for range runs {
		out = append(out, s086Run(t, nil, false))
	}
	return out, secretsPath
}

// s087d42Autoupdate resolves the ::gentoo provider the way one `overlay
// autoupdate` run does (resolveGentooProvider reads the configured
// repositories' tokens and the GitHub token), runs times, and returns each
// run's log stream.
func s087d42Autoupdate(t *testing.T, repos []string, runs int) ([]string, string) {
	t.Helper()
	fx := realignSetup(t, true, true)
	s087d42OutputFlags(t)
	s087d42BlankTokens(t, repos)
	secretsPath := s087d42UnreadableSecrets(t)
	cfg := &config.Config{Repositories: map[string]*config.RepoConfig{}}
	for _, r := range repos {
		cfg.Repositories[r] = &config.RepoConfig{Provider: "local", Path: fx.gentooPath}
	}
	out := make([]string, 0, runs)
	for range runs {
		buf := &s087d42Buffer{}
		log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
			Level: slog.LevelDebug,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if len(groups) == 0 && a.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return a
			},
		}))
		prov, err := resolveGentooProvider(t.Context(), log, cfg)
		if err != nil {
			t.Fatalf("resolveGentooProvider: %v\nlogs:\n%s", err, buf.String())
		}
		if prov != nil {
			_ = prov.Close()
		}
		out = append(out, buf.String())
	}
	return out, secretsPath
}

// TestS087_4_2_CompareWarnsAgainOnTheNextRun is the hostile half against a
// collapse: "once per run" is not "once per process". Each of two compare runs
// in one process warns exactly once.
func TestS087_4_2_CompareWarnsAgainOnTheNextRun(t *testing.T) {
	res, secretsPath := s087d42Compare(t, s087d42ThreeRepos, 2)
	for i, r := range res {
		if r.code != 0 {
			t.Errorf("run %d: exit code = %d, want 0\nlogs:\n%s", i+1, r.code, r.logs)
		}
		s087d42ExactlyOne(t, fmt.Sprintf("run %d", i+1), r.logs, secretsPath)
	}
}

// TestS087_4_2_CompareWarnsOnceWhateverTheRepositoryCount is the hostile half
// against a split: one compare run warns exactly once, with one configured
// repository and with three, the GitHub token lookup included.
func TestS087_4_2_CompareWarnsOnceWhateverTheRepositoryCount(t *testing.T) {
	for _, repos := range [][]string{{"gentoo"}, s087d42ThreeRepos} {
		t.Run(fmt.Sprintf("%d repositories", len(repos)), func(t *testing.T) {
			res, secretsPath := s087d42Compare(t, repos, 1)
			if res[0].code != 0 {
				t.Errorf("exit code = %d, want 0\nlogs:\n%s", res[0].code, res[0].logs)
			}
			s087d42ExactlyOne(t, "compare", res[0].logs, secretsPath)
		})
	}
}

// TestS087_4_2_AutoupdateWarnsAgainOnTheNextRun: as for compare, each of two
// autoupdate provider resolutions in one process warns exactly once.
func TestS087_4_2_AutoupdateWarnsAgainOnTheNextRun(t *testing.T) {
	runs, secretsPath := s087d42Autoupdate(t, s087d42ThreeRepos, 2)
	for i, logs := range runs {
		s087d42ExactlyOne(t, fmt.Sprintf("run %d", i+1), logs, secretsPath)
	}
}

// TestS087_4_2_AutoupdateWarnsOnceWhateverTheRepositoryCount: one autoupdate
// run warns exactly once, with one configured repository and with three.
func TestS087_4_2_AutoupdateWarnsOnceWhateverTheRepositoryCount(t *testing.T) {
	for _, repos := range [][]string{{"gentoo"}, s087d42ThreeRepos} {
		t.Run(fmt.Sprintf("%d repositories", len(repos)), func(t *testing.T) {
			logs, secretsPath := s087d42Autoupdate(t, repos, 1)
			s087d42ExactlyOne(t, "autoupdate", logs[0], secretsPath)
		})
	}
}
