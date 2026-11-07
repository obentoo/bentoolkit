package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/targets"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/spf13/cobra"
)

// newTargetsCmd builds `overlay targets`.
func newTargetsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "targets",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Find packages whose best version lacks a required Python, Lua or Ruby implementation",
		Long: `Read the implementations the overlay requires from .autoupdate/targets.toml and
report every package whose best visible version, across the overlay and ::gentoo,
does not offer them. On a system whose PYTHON_SINGLE_TARGET holds only a required
implementation, such a version fails emerge with "REQUIRED_USE flag constraints are
unsatisfied".

A row naming an overlay version means ::gentoo overtook the overlay's mirror: the
version Portage picks is ::gentoo's, and the fix is to bump the mirror. Masks are not
read, and only versions keyworded amd64 or ~amd64 count as visible.

Exit status: 0 when no package has a gap, 1 when one has or the sweep failed.`,
		Example: `  bentoo overlay targets
  bentoo overlay targets --all     # every ::gentoo package, not only those the overlay carries
  bentoo overlay targets --json`,
		RunE: runTargets,
	}
	cmd.Flags().Bool("all", false, "Sweep every package of ::gentoo, not only the packages the overlay carries")
	cmd.Flags().Bool("json", false, "Write the findings to stdout as one JSON array")
	return cmd
}

func runTargets(cmd *cobra.Command, _ []string) error {
	ctx := commandContext(cmd)
	log := logging.FromContext(ctx)

	appCtx, err := loadAppContext(cmd)
	if err != nil {
		log.Error("loading config: failed", "err", err)
		return exitWith(1)
	}
	policy, err := targets.LoadPolicy(appCtx.OverlayPath)
	if errors.Is(err, targets.ErrNoPolicy) {
		uiInfo(err.Error())
		return nil
	}
	if err != nil {
		log.Error("loading the target policy: failed", "err", err)
		return exitWith(1)
	}
	repo, err := targets.RepoName(appCtx.OverlayPath)
	if err != nil {
		log.Error("reading the overlay name: failed", "err", err)
		return exitWith(1)
	}

	all, _ := cmd.Flags().GetBool("all")
	asJSON, _ := cmd.Flags().GetBool("json")
	findings, err := targets.Sweep(ctx, policy, targets.SweepOptions{
		Overlay:     appCtx.OverlayPath,
		OverlayRepo: repo,
		Gentoo:      gentooRepoPath(),
		All:         all,
	})
	if err != nil {
		log.Error("sweeping the target policy: failed", "err", err)
		return exitWith(1)
	}
	log.Info("target sweep finished", "overlay", appCtx.OverlayPath, "all", all, "findings", len(findings))

	out := cmd.OutOrStdout()
	if asJSON {
		if findings == nil {
			findings = []targets.Finding{}
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(findings); err != nil {
			log.Error("writing the findings: failed", "err", err)
			return exitWith(1)
		}
	} else {
		_, _ = fmt.Fprint(out, formatTargetFindings(findings))
	}
	if len(findings) > 0 {
		return exitWith(1)
	}
	return nil
}

// formatTargetFindings renders one block per package, plain text.
func formatTargetFindings(findings []targets.Finding) string {
	if len(findings) == 0 {
		return "Every package offers the required implementations.\n"
	}
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "%s-%s::%s\n", f.Atom, f.Version, f.Repo)
		for _, g := range f.Gaps {
			fmt.Fprintf(&b, "    %s\n", g)
		}
		if f.Unreadable != "" {
			fmt.Fprintf(&b, "    offer unreadable: %s\n", f.Unreadable)
		}
		if f.OverlayVersion != "" {
			fmt.Fprintf(&b, "    overtaken: the overlay holds %s; bump its mirror to %s\n", f.OverlayVersion, f.Version)
		}
	}
	fmt.Fprintf(&b, "%d package(s) lack a required implementation.\n", len(findings))
	return b.String()
}
