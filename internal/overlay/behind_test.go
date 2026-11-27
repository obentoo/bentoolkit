package overlay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/git"
)

// rangeCounts answers HEAD..upstream with behind and upstream..HEAD with ahead.
func rangeCounts(behind, ahead int) func(_ context.Context, from, to string) (int, error) {
	return func(_ context.Context, from, to string) (int, error) {
		if from == "HEAD" {
			return behind, nil
		}
		return ahead, nil
	}
}

func bufferLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func TestCheckUpstreamBehindIsRefused(t *testing.T) {
	mock := git.NewMockGitRunner("/overlay")
	mock.CountRangeFunc = rangeCounts(6, 0)

	status := CheckUpstream(context.Background(), mock, UpstreamCheckOptions{})

	if !status.IsBehind() || status.Behind != 6 || status.Upstream != "origin/master" || status.Branch != "master" {
		t.Fatalf("status = %+v, want 6 behind origin/master on master", status)
	}
	if status.Fetched {
		t.Fatal("Fetched = true without Fetch")
	}
	err := status.Refusal()
	if !errors.Is(err, ErrCheckoutBehind) {
		t.Fatalf("Refusal() = %v, want ErrCheckoutBehind", err)
	}
	for _, want := range []string{"6 commit(s) behind origin/master", "bentoo overlay pull", "as of the last fetch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Refusal() = %q, missing %q", err, want)
		}
	}
}

func TestCheckUpstreamDivergedCountsAsBehind(t *testing.T) {
	mock := git.NewMockGitRunner("/overlay")
	mock.CountRangeFunc = rangeCounts(2, 3)

	status := CheckUpstream(context.Background(), mock, UpstreamCheckOptions{})

	if !status.IsBehind() || status.Ahead != 3 {
		t.Fatalf("status = %+v, want behind 2 and ahead 3", status)
	}
	if err := status.Refusal(); err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("Refusal() = %v, want a diverged refusal", err)
	}
}

func TestCheckUpstreamUpToDateOrAheadIsNotBehind(t *testing.T) {
	for _, ahead := range []int{0, 4} {
		mock := git.NewMockGitRunner("/overlay")
		mock.CountRangeFunc = rangeCounts(0, ahead)

		status := CheckUpstream(context.Background(), mock, UpstreamCheckOptions{})

		if !status.Checked || status.IsBehind() {
			t.Fatalf("ahead %d: status = %+v, want checked and not behind", ahead, status)
		}
		if err := status.Refusal(); err != nil {
			t.Fatalf("ahead %d: Refusal() = %v, want nil", ahead, err)
		}
	}
}

func TestCheckUpstreamSkipsWithoutBranchOrUpstream(t *testing.T) {
	cases := map[string]func(m *git.MockGitRunner){
		"detached HEAD": func(m *git.MockGitRunner) {
			m.CurrentBranchFunc = func(context.Context) (string, error) { return "", git.ErrDetachedHead }
		},
		"not a repository": func(m *git.MockGitRunner) {
			m.CurrentBranchFunc = func(context.Context) (string, error) { return "", git.ErrGitCommand }
		},
		"no upstream": func(m *git.MockGitRunner) {
			m.UpstreamFunc = func(context.Context) (string, error) { return "", git.ErrNoUpstream }
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			mock := git.NewMockGitRunner("/overlay")
			setup(mock)
			counted, fetched := false, false
			mock.CountRangeFunc = func(context.Context, string, string) (int, error) { counted = true; return 9, nil }
			mock.FetchFunc = func(context.Context, string) error { fetched = true; return nil }
			log, buf := bufferLogger()

			status := CheckUpstream(context.Background(), mock, UpstreamCheckOptions{Fetch: true, Logger: log})

			if status.Checked || status.IsBehind() || status.Refusal() != nil {
				t.Fatalf("status = %+v, want a skipped check", status)
			}
			if counted || fetched {
				t.Fatalf("counted=%v fetched=%v, want neither on a skipped check", counted, fetched)
			}
			if !strings.Contains(buf.String(), "level=DEBUG") || strings.Contains(buf.String(), "level=WARN") {
				t.Fatalf("log = %q, want one DEBUG line and no WARN", buf.String())
			}
		})
	}
}

func TestCheckUpstreamFetchesTheConfiguredRemoteWithABound(t *testing.T) {
	mock := git.NewMockGitRunner("/overlay")
	var gotRemote string
	var hadDeadline bool
	mock.FetchFunc = func(ctx context.Context, remote string) error {
		gotRemote = remote
		_, hadDeadline = ctx.Deadline()
		return nil
	}
	mock.CountRangeFunc = rangeCounts(1, 0)

	status := CheckUpstream(context.Background(), mock, UpstreamCheckOptions{Fetch: true, Remote: "upstream", FetchTimeout: time.Second})

	if gotRemote != "upstream" || !hadDeadline {
		t.Fatalf("fetch remote=%q deadline=%v, want upstream with a deadline", gotRemote, hadDeadline)
	}
	if !status.Fetched || !strings.Contains(status.Refusal().Error(), "just fetched") {
		t.Fatalf("status = %+v, want a fetched behind status", status)
	}
}

func TestCheckUpstreamFetchFailureWarnsAndContinues(t *testing.T) {
	mock := git.NewMockGitRunner("/overlay")
	mock.FetchFunc = func(context.Context, string) error { return errors.New("network is unreachable") }
	mock.CountRangeFunc = rangeCounts(3, 0)
	log, buf := bufferLogger()

	status := CheckUpstream(context.Background(), mock, UpstreamCheckOptions{Fetch: true, Logger: log})

	if !status.IsBehind() || status.Behind != 3 || status.Fetched {
		t.Fatalf("status = %+v, want 3 behind against the local ref, not fetched", status)
	}
	if got := strings.Count(buf.String(), "level=WARN"); got != 1 {
		t.Fatalf("log has %d WARN lines, want 1: %q", got, buf.String())
	}
	if !strings.Contains(buf.String(), "network is unreachable") {
		t.Fatalf("log = %q, want the fetch error", buf.String())
	}
}

func TestCheckUpstreamDefaultsTheRemoteToOrigin(t *testing.T) {
	mock := git.NewMockGitRunner("/overlay")
	var gotRemote string
	mock.FetchFunc = func(_ context.Context, remote string) error { gotRemote = remote; return nil }

	CheckUpstream(context.Background(), mock, UpstreamCheckOptions{Fetch: true})

	if gotRemote != "origin" {
		t.Fatalf("fetched %q, want origin", gotRemote)
	}
}

func TestCheckUpstreamCountFailureSkipsWithAWarning(t *testing.T) {
	mock := git.NewMockGitRunner("/overlay")
	mock.CountRangeFunc = func(context.Context, string, string) (int, error) { return 0, git.ErrGitCommand }
	log, buf := bufferLogger()

	status := CheckUpstream(context.Background(), mock, UpstreamCheckOptions{Logger: log})

	if status.Checked || status.Refusal() != nil {
		t.Fatalf("status = %+v, want a skipped check", status)
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("log = %q, want a WARN", buf.String())
	}
}

func TestIsCheckoutRoot(t *testing.T) {
	plain := t.TempDir()
	if IsCheckoutRoot(plain) {
		t.Fatal("a directory with no .git is a checkout root")
	}
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsCheckoutRoot(repo) || !IsCheckoutRoot(worktree) {
		t.Fatal("a .git directory or a worktree's .git file is not a checkout root")
	}
}
