package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/spf13/cobra"
)

// This file is `bentoo distfile fetch`: the user-facing half of the
// authenticated download that internal/autoupdate has performed for the
// maintainer since 0.28.
//
// # Why a top-level command and not one more `overlay` subcommand
//
// Everything under `overlay` acts ON the overlay — it adds, commits, compares,
// manifests, prunes. This one reads two files out of the overlay (the registry
// and the ebuild, to learn the version) and then writes into DISTDIR, which is
// Portage's directory and not the overlay's. The person running it is not
// maintaining anything: they are installing a package whose distfile no mirror
// is allowed to carry, and `emerge` has just stopped and told them so.
//
// # Why it exists at all
//
// Before it, the download the overlay already knew how to perform was reachable
// only from the sweep, so the ebuild's pkg_nofetch could offer nothing but the
// manual route: open the vendor page, log in, save the file under exactly the
// right name, move it into DISTDIR. Every step of that is a chance to land a
// file whose name or content the Manifest will reject.
//
// The manual instructions do not go away, and must stay FIRST in any
// pkg_nofetch that mentions this command: bentoolkit is not a dependency of the
// packages it fetches for, and whoever does not have it installed may not be
// left without a route.

// distfileFetchDistdir is --distdir: where the file is written. Empty means the
// host's own DISTDIR, which is the answer this command wants in almost every
// invocation — `emerge` will look there and nowhere else.
var distfileFetchDistdir string

// distfileFetchVersion is --version: which version's distfile to fetch. Empty
// means the version the overlay currently carries.
var distfileFetchVersion string

// newDistfileCmd builds `distfile` and registers its subcommands.
func newDistfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "distfile",
		Short: "Work with distfiles Portage cannot download by itself",
		Long: `Commands for the source archives 'emerge' cannot fetch on its own.

A vendor that gates its download behind a registration form — or behind nothing
more than a POST no mirror may replay — leaves Portage with a SRC_URI it cannot
follow. The overlay records how that download is performed; these commands
perform it.`,
	}
	cmd.AddCommand(newDistfileFetchCmd())
	return cmd
}

// newDistfileFetchCmd builds `distfile fetch`.
func newDistfileFetchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fetch <category/package>",
		Short: "Download a gated distfile into DISTDIR, ready for emerge",
		Long: `Perform the download the overlay records for a package, and write the file
into DISTDIR under the exact name its Manifest expects.

This is the same download 'bentoo overlay autoupdate' performs before it
regenerates a Manifest — same request, same guards, same file name — so what
lands here is what the Manifest was computed against.

Which version: the one the overlay currently carries, unless --version names
another. Pass the version 'emerge' asked for when you are installing anything
other than the newest ebuild.

Where it lands: the host's own DISTDIR, as reported by 'portageq distdir', so
'emerge' finds it without being told. --distdir overrides that. The directory
has to be writable by you — on most systems DISTDIR is group-writable by the
'portage' group, and the run stops with the directory named when it is not.

A serial, when the record configures one, is read at runtime from the
environment variable the record names (or from the secrets file) and is never
written to the overlay, the logs or this command's output. The variable's NAME
is printed; its value never is.

Exit codes:
  0  the file was downloaded and written
  1  the run could not be honoured: no such record, an ambiguous atom, a
     package that needs no authenticated fetch, an unwritable DISTDIR, a
     missing serial, or a download the vendor refused

Examples:
  # Fetch the distfile for the version the overlay carries
  bentoo distfile fetch app-misc/example

  # Fetch the one a specific ebuild needs
  bentoo distfile fetch app-misc/example --version 3.70.5

  # Write it somewhere else (it will not be found by emerge there)
  bentoo distfile fetch app-misc/example --distdir /tmp/dl`,
		Args: cobra.ExactArgs(1),
		Run:  runDistfileFetch,
	}
	cmd.Flags().StringVar(&distfileFetchDistdir, "distdir", "",
		"Directory the file is written into (default: the host's own DISTDIR, as reported by portageq distdir)")
	cmd.Flags().StringVar(&distfileFetchVersion, "version", "",
		"Version whose distfile to fetch (default: the version the overlay currently carries)")
	return cmd
}

// runDistfileFetch is the cobra half: signals, config, DISTDIR. The decision
// this command exists for lives in internal/autoupdate, reached through the one
// exported call below.
func runDistfileFetch(cmd *cobra.Command, args []string) {
	ctx, stop := signalContext(cmd.Context())
	defer stop()

	appCtx, err := loadAppContext()
	if err != nil {
		output.PrintError("loading config: %v", err)
		osExit(1)
		return
	}

	// Resolve rather than Locate: this directory is about to be WRITTEN to, and
	// Resolve is the rung that proves that before the download starts instead of
	// after tens of megabytes have been transferred. Nothing is passed as the
	// configured rung, so the precedence is --distdir, then the host's own
	// answer, then Portage's documented default.
	dir, err := distfiles.Resolve(distfileFetchDistdir, "")
	if err != nil {
		output.PrintError("%v", err)
		osExit(1)
		return
	}

	res, err := autoupdate.FetchAuthDistfile(ctx, autoupdate.AuthDistfileRequest{
		OverlayPath: appCtx.OverlayPath,
		Package:     args[0],
		Version:     distfileFetchVersion,
		DestDir:     dir.Path,
	})
	if err != nil {
		reportDistfileFetchError(args[0], err)
		osExit(1)
		return
	}

	output.PrintSuccess("%s-%s: wrote %s", res.Package, res.Version, res.Path)
	if res.SerialEnv != "" {
		fmt.Printf("  serial read from $%s\n", res.SerialEnv)
	}
	fmt.Println("  emerge can now install it without downloading anything.")
}

// reportDistfileFetchError prints the failure with the remedy that belongs to
// it. The sentinels are classified rather than printed alone because the three
// request-shaped ones are not download failures at all, and reading "fetch
// failed" over a package that simply needs no fetch sends the operator looking
// for a network problem that is not there.
func reportDistfileFetchError(requested string, err error) {
	output.PrintError("%v", err)

	switch {
	case errors.Is(err, autoupdate.ErrNoAuthFetch):
		fmt.Fprintln(os.Stderr, "  Nothing to do: emerge fetches this package's distfile from a mirror by itself.")
	case errors.Is(err, autoupdate.ErrPackageNotInRegistry):
		fmt.Fprintf(os.Stderr, "  The overlay tracks no record named %s, so there is no download recorded for it.\n", requested)
	case errors.Is(err, autoupdate.ErrAuthFetchSecretMissing):
		fmt.Fprintln(os.Stderr, "  The serial is read at runtime and never stored in the overlay: export it, or add it to the secrets file named above.")
	}
}
