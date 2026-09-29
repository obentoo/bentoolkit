package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/obentoo/bentoolkit/internal/notice"
	"github.com/spf13/cobra"
)

// This file is `bentoo notice`: authoring the overlay's GLEP 42 news item and
// the site's notice file from one input, with one ID (story 071). The logic
// lives in internal/notice; this file parses flags, supplies the terminal and
// the clock, and prints. It performs no git operation (R6.2).

// newNoticeCmd builds `notice` and its two subcommands.
func newNoticeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notice",
		Short: "Author bentoo notices (news item and site feed entry)",
		Long: `Write a notice to the overlay as a GLEP 42 news item and, when notice.site_path
is configured, to the site repository as the YAML file the notices feed is
built from. Both files share one ID. No git operation is performed: review,
commit and push each repository yourself.`,
	}
	cmd.AddCommand(newNoticeNewCmd(), newNoticeReviseCmd())
	return cmd
}

func newNoticeNewCmd() *cobra.Command {
	var in notice.Input
	var bodyFile string
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Write a new notice to the overlay and the site",
		Long: `Write a new notice. The ID is <published>-<name>. The body comes from
--body-file, or from $VISUAL / $EDITOR when it is absent.

--affects takes "<category>/<package>[:<slot>][ <range>[,<range>...]]", a range
being one of <, <=, =, >=, > followed by a version, for example
--affects 'dev-libs/foo:1 >=1.0,<1.2.3'. Repeat it for several packages.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The flags parsed, so any error from here on is about their
			// values or the files, and the usage block would bury the one
			// line that names the flag to fix.
			cmd.SilenceUsage = true
			return runNoticeNew(cmd, in, bodyFile)
		},
	}
	f := cmd.Flags()
	f.StringVar(&in.Type, "type", "", "Notice type: security, release, news or announcement")
	f.StringVar(&in.Severity, "severity", "", "Severity: info, warning or critical")
	f.StringVar(&in.Title, "title", "", "Title, at most 50 characters")
	f.StringVar(&in.Summary, "summary", "", "One-line summary, at most 300 characters")
	f.StringVar(&in.Name, "name", "", "Short name of the ID: at most 20 characters of [a-z0-9+_-]")
	// StringArray, not StringSlice: a slice flag would split `>=1.0,<1.2.3`
	// at its commas.
	f.StringArrayVar(&in.Affects, "affects", nil, "Affected package and version ranges (repeatable; required for security and release)")
	f.StringVar(&in.Published, "published", "", "Publication date, YYYY-MM-DD (default: today in UTC)")
	f.StringVar(&in.Author, "author", "", "Author, \"Name <email>\" (default: the configured git user)")
	f.StringVar(&bodyFile, "body-file", "", "Read the body from this file instead of opening an editor")
	return cmd
}

func runNoticeNew(cmd *cobra.Command, in notice.Input, bodyFile string) error {
	app, err := loadAppContext()
	if err != nil {
		return err
	}
	sitePath, err := app.Config.GetNoticeSitePath()
	if err != nil {
		return err
	}
	if in.Author == "" {
		user, addr, err := app.Config.GetGitUser()
		if err != nil {
			return fmt.Errorf("--author not given and no git user configured: %w", err)
		}
		in.Author = fmt.Sprintf("%s <%s>", user, addr)
	}

	// Validate every flag before the editor opens, so no text is written for
	// a notice that would then be refused.
	probe := in
	probe.Body = "-"
	if _, err := notice.New(probe, time.Now()); err != nil {
		return err
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()
	in.Body, err = notice.ReadBody(ctx, bodyFile, os.Getenv, notice.TerminalRunner)
	if err != nil {
		return err
	}
	n, err := notice.New(in, time.Now())
	if err != nil {
		return err
	}
	res, err := notice.Publish(ctx, n, app.OverlayPath, sitePath)
	if err != nil {
		return err
	}
	printNoticeResult(cmd.OutOrStdout(), cmd.ErrOrStderr(), res, n.ID, app.OverlayPath, sitePath, "Wrote")
	return nil
}

func newNoticeReviseCmd() *cobra.Command {
	var changes notice.Changes
	var bodyFile string
	cmd := &cobra.Command{
		Use:   "revise <id>",
		Short: "Revise a published notice in both repositories",
		Long: `Open the notice's current text in $VISUAL / $EDITOR (or read it from
--body-file) and rewrite the news item and the site file together. The news
item's Revision goes up by one and the site file's updated becomes now;
the ID and the publication date stay.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true // as in notice new
			if !cmd.Flags().Changed("affects") {
				changes.Affects = nil
			}
			return runNoticeRevise(cmd, args[0], changes, bodyFile)
		},
	}
	f := cmd.Flags()
	f.StringVar(&changes.Severity, "severity", "", "New severity: info, warning or critical")
	f.StringVar(&changes.Title, "title", "", "New title, at most 50 characters")
	f.StringVar(&changes.Summary, "summary", "", "New one-line summary, at most 300 characters")
	f.StringArrayVar(&changes.Affects, "affects", nil, "Replace the affected packages (repeatable; same syntax as notice new)")
	f.StringVar(&bodyFile, "body-file", "", "Take the new text from this file instead of opening an editor")
	return cmd
}

func runNoticeRevise(cmd *cobra.Command, id string, changes notice.Changes, bodyFile string) error {
	app, err := loadAppContext()
	if err != nil {
		return err
	}
	sitePath, err := app.Config.GetNoticeSitePath()
	if err != nil {
		return err
	}
	edit := notice.NewBodyEditor(os.Getenv, notice.TerminalRunner)
	if bodyFile != "" {
		edit = func(ctx context.Context, _ string) (string, error) {
			return notice.ReadBody(ctx, bodyFile, os.Getenv, notice.TerminalRunner)
		}
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()
	res, err := notice.Revise(ctx, id, changes, app.OverlayPath, sitePath, edit, time.Now())
	if err != nil {
		return err
	}
	printNoticeResult(cmd.OutOrStdout(), cmd.ErrOrStderr(), res, id, app.OverlayPath, sitePath, "Revised")
	return nil
}

// printNoticeResult prints what was written and what the operator does next.
// The next steps are printed, never run (R6.2).
func printNoticeResult(out, errOut io.Writer, res notice.Result, id, overlay, sitePath, verb string) {
	for _, w := range res.Warnings {
		fmt.Fprintf(errOut, "warning: %s\n", w)
	}
	for _, p := range res.Paths {
		fmt.Fprintf(out, "%s %s\n", verb, p)
	}
	siteFile := filepath.Join("src", "content", "notices", id+".yaml")
	// Only `new` has a site document to show without a site path: `revise`
	// reads the site file to build one, and with no site path it has none.
	printedYAML := sitePath == "" && len(res.YAML) > 0
	switch {
	case printedYAML:
		fmt.Fprintf(out, "\nnotice.site_path is not configured; save this as %s in the site repository:\n\n", siteFile)
		_, _ = out.Write(res.YAML)
	case sitePath == "":
		fmt.Fprintf(out, "\nnotice.site_path is not configured, so the site notice was not updated; revise %s in the site repository by hand.\n", siteFile)
	}

	newsDir := filepath.Join("metadata", "news", id)
	fmt.Fprintf(out, "\nNext steps (bentoo performs no git operation):\n")
	fmt.Fprintf(out, "  1. Review the files above.\n")
	fmt.Fprintf(out, "  2. git -C %s add %s && git -C %s commit\n", overlay, newsDir, overlay)
	switch {
	case sitePath != "":
		fmt.Fprintf(out, "  3. git -C %s add %s && git -C %s commit\n", sitePath, siteFile, sitePath)
	case printedYAML:
		fmt.Fprintf(out, "  3. Add and commit the YAML above in the site repository.\n")
	default:
		fmt.Fprintf(out, "  3. Update, add and commit %s in the site repository.\n", siteFile)
	}
	fmt.Fprintf(out, "  4. Push both repositories.\n")
}
