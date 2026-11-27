package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/i18n"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// overlayUpstreamStatus measures how far the overlay checkout is behind its
// upstream. A path that is not the top of a git checkout is skipped at DEBUG,
// so an overlay that only sits inside another repository is never measured
// against that repository's branch.
func overlayUpstreamStatus(ctx context.Context, log *slog.Logger, d *deps, cfg *config.Config, overlayPath string, fetch bool) overlay.UpstreamStatus {
	log = logging.OrDiscard(log)
	if !overlay.IsCheckoutRoot(overlayPath) {
		log.Debug("upstream check skipped: the overlay is not a git checkout", "path", overlayPath)
		return overlay.UpstreamStatus{}
	}
	remote := ""
	if cfg != nil {
		remote = cfg.Overlay.Remote
	}
	return overlay.CheckUpstream(ctx, d.overlayGit(overlayPath), overlay.UpstreamCheckOptions{
		Fetch:  fetch,
		Remote: remote,
		Logger: log,
	})
}

// enforceOverlayUpstream turns a status into the command's decision. A
// checkout that is not behind passes in silence. One that is behind is
// refused when the mode writes and allowBehind is false; otherwise it passes
// with a warning on stderr. readOnly reports the warn-only outcome, which
// tells a mode that writes as a side effect (--check) to skip those writes.
func enforceOverlayUpstream(log *slog.Logger, status overlay.UpstreamStatus, writes, allowBehind bool) (readOnly bool, err error) {
	if !status.IsBehind() {
		return false, nil
	}
	log = logging.OrDiscard(log)
	if allowBehind {
		log.Warn("writing from an overlay checkout behind its upstream: --allow-behind given",
			"branch", status.Branch, "upstream", status.Upstream, "behind", status.Behind, "ahead", status.Ahead)
		output.Warning.Fprintf(os.Stderr, i18n.TCount("--allow-behind given: running on a checkout %d commit(s) behind %s.", status.Behind)+"\n",
			status.Behind, status.Upstream)
		return false, nil
	}
	if writes {
		return false, fmt.Errorf("refusing to write from a stale checkout: %w; pass --allow-behind to write anyway", status.Refusal())
	}
	output.Warning.Fprintf(os.Stderr, i18n.TCount("The overlay checkout is %d commit(s) behind %s: this run reads a stale tree. Run 'bentoo overlay pull' first.", status.Behind)+"\n",
		status.Behind, status.Upstream)
	if !status.Fetched {
		output.Warning.Fprintln(os.Stderr, i18n.T("The count uses the remote-tracking ref as the last fetch left it, which may itself be stale; pass --fetch to refresh it."))
	}
	return true, nil
}

// upstreamGuardScope says whether the selected autoupdate mode reads the
// overlay (checked) and whether it writes to it or to its registry (writes).
// It mirrors the dispatch in runAutoupdate. --check is read-only here: it
// reports, and its side writes are skipped through behindReadOnly instead.
// --list reads only pending.json and --revive-list compares the registry's
// disabled entries with ::gentoo, so neither is checked.
func (ar *autoupdateRun) upstreamGuardScope() (checked, writes bool) {
	switch {
	case ar.opts.repair != "":
		return true, true
	case ar.opts.lint:
		return true, ar.opts.fix
	case ar.opts.markAutoDisabled:
		return true, true
	case ar.opts.check:
		return true, false
	case ar.opts.list:
		return false, false
	case ar.opts.apply != "", ar.opts.revive != "", ar.opts.clean:
		return true, true
	default:
		return false, false
	}
}

// guardOverlayUpstream is the autoupdate side of the upstream guard: a
// writing mode is refused from a checkout behind its upstream, a reading mode
// warns, and --check additionally runs without writing (behindReadOnly).
func (ar *autoupdateRun) guardOverlayUpstream(ctx context.Context, cfg *config.Config, overlayPath string) error {
	checked, writes := ar.upstreamGuardScope()
	if !checked {
		return nil
	}
	status := overlayUpstreamStatus(ctx, ar.log(), ar.deps, cfg, overlayPath, ar.opts.fetchUpstream)
	readOnly, err := enforceOverlayUpstream(ar.log(), status, writes, ar.opts.allowBehind)
	if err != nil {
		return err
	}
	if readOnly && ar.opts.check {
		ar.behindReadOnly = true
		output.Warning.Fprintln(os.Stderr, i18n.T("Nothing will be written to the overlay or packages.toml by this run; pass --allow-behind to write anyway."))
	}
	return nil
}
