package overlay

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// DefaultCompareConcurrency is the number of packages CompareWithProvider
// processes in parallel when CompareOptions.Concurrency is not set (<= 0).
const DefaultCompareConcurrency = 10

// CompareResult represents the result of comparing a package between overlays
type CompareResult struct {
	Category      string
	Package       string
	LocalVersion  string // Version in Bentoo overlay
	RemoteVersion string // Version in Gentoo repository
	Status        CompareStatus
}

// CompareStatus indicates the comparison result
type CompareStatus int

const (
	// StatusUpToDate means local version equals remote version
	StatusUpToDate CompareStatus = iota
	// StatusOutdated means local version is older than remote
	StatusOutdated
	// StatusNewer means local version is newer than remote
	StatusNewer
	// StatusNotInRemote means package doesn't exist in remote
	StatusNotInRemote
	// StatusError means an error occurred during comparison
	StatusError
)

// String returns a human-readable status
func (s CompareStatus) String() string {
	switch s {
	case StatusUpToDate:
		return "up-to-date"
	case StatusOutdated:
		return "outdated"
	case StatusNewer:
		return "newer"
	case StatusNotInRemote:
		return "not-in-remote"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// CompareOptions configures the comparison behavior
type CompareOptions struct {
	// OnlyOutdated filters results to only show outdated packages
	OnlyOutdated bool
	// IncludeSynced includes packages that have the same version (up-to-date)
	// When true, StatusUpToDate packages are included in results
	// This is independent of OnlyOutdated - both can be combined
	IncludeSynced bool
	// IncludeNotInRemote includes packages that don't exist in remote
	IncludeNotInRemote bool
	// ProgressCallback, when non-nil, is invoked once per package as that
	// package's comparison completes. done is the cumulative count of packages
	// finished so far and total is the number of packages in the batch.
	// Because CompareWithProvider runs packages concurrently, the callback may
	// fire from multiple goroutines and the per-invocation order is not
	// deterministic; done is sourced from an atomic counter, so the value
	// observed by any single invocation is monotone non-decreasing.
	ProgressCallback func(done, total uint64)
	// Concurrency bounds the number of packages CompareWithProvider processes
	// in parallel. A value <= 0 is treated as DefaultCompareConcurrency.
	Concurrency int
	// Ctx is the parent context for the comparison. It originates in cmd/
	// (signal.NotifyContext), so cancelling it aborts an in-flight comparison.
	// When nil it is treated as context.Background() by the consumer.
	Ctx context.Context
}

// CompareReport contains the full comparison report
type CompareReport struct {
	TotalPackages    int
	ComparedPackages int
	OutdatedCount    int
	NewerCount       int
	UpToDateCount    int
	NotInRemoteCount int
	ErrorCount       int
	Results          []CompareResult
}

// githubProviderAdapter adapts a *github.Client to the provider.Provider interface,
// allowing Compare() to delegate to CompareWithProvider().
type githubProviderAdapter struct {
	client *github.Client
}

// GetPackageVersions returns all ebuild versions for a package via the GitHub client.
// Maps github.ErrNotFound to provider.ErrNotFound for interface compatibility.
func (a *githubProviderAdapter) GetPackageVersions(category, pkg string) ([]string, error) {
	versions, err := a.client.GetPackageVersions(category, pkg)
	if err == github.ErrNotFound {
		return nil, provider.ErrNotFound
	}
	return versions, err
}

// GetName returns the provider name.
func (a *githubProviderAdapter) GetName() string { return "github" }

// SupportsAPI returns true since GitHub uses an API.
func (a *githubProviderAdapter) SupportsAPI() bool { return true }

// Close is a no-op for the GitHub adapter (no resources to release).
func (a *githubProviderAdapter) Close() error { return nil }

// Compare compares local packages against a remote GitHub repository
func Compare(localPackages []PackageInfo, client *github.Client, opts CompareOptions) (*CompareReport, error) {
	return CompareWithProvider(localPackages, &githubProviderAdapter{client: client}, opts)
}

// CompareWithProvider compares local packages against an upstream repository using any Provider.
//
// Packages are compared concurrently, bounded by opts.Concurrency (a value <= 0
// is treated as DefaultCompareConcurrency). The semaphore is acquired with a
// context-cancellable select: when opts.Ctx is cancelled the remaining packages
// are not dispatched and the comparison returns the partial report together
// with the context error, so a SIGINT aborts a long scan. All writes to the
// shared report are mutex-guarded, and results are sorted by category/package
// before returning so the output is deterministic regardless of completion
// order.
func CompareWithProvider(localPackages []PackageInfo, prov provider.Provider, opts CompareOptions) (*CompareReport, error) {
	report := &CompareReport{
		TotalPackages: len(localPackages),
		Results:       []CompareResult{},
	}

	// A nil opts.Ctx is treated as context.Background() (additive field, R3.3).
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background() // SAFE: opts.Ctx is an additive field; nil means "no cancellation requested"
	}

	// Sanitize the concurrency limit: a non-positive value means "use the
	// default" so a zero-valued CompareOptions still behaves sensibly.
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultCompareConcurrency
	}

	var (
		sem      = make(chan struct{}, concurrency)
		wg       sync.WaitGroup
		mu       sync.Mutex
		progress atomic.Uint64
		total    = uint64(len(localPackages))
		// cancelled records whether the context fired while packages were
		// still being dispatched, so the partial report is returned with the
		// context error (preserving the T9 early-cancellation contract).
		cancelled bool
	)

	for _, pkg := range localPackages {
		// A select with both cases ready picks at random, so check the context
		// deterministically first: a context cancelled before (or during) the
		// call must stop dispatch on EVERY iteration, not just roughly half.
		if ctx.Err() != nil {
			cancelled = true
			break
		}
		// Cancellable semaphore acquisition: also stop dispatching if the
		// caller's context is cancelled while waiting for a free slot.
		select {
		case <-ctx.Done():
			cancelled = true
		case sem <- struct{}{}:
		}
		if cancelled {
			break
		}

		wg.Add(1)
		go func(p PackageInfo) {
			defer wg.Done()
			defer func() { <-sem }()

			result := comparePackageWithProvider(p, prov)

			// Filter based on options using switch for clarity.
			include := false
			switch result.Status {
			case StatusOutdated:
				include = true // Always include outdated (primary use case)
			case StatusUpToDate:
				include = opts.IncludeSynced
			case StatusNewer:
				include = !opts.OnlyOutdated // Include if not filtering to outdated only
			case StatusNotInRemote:
				include = opts.IncludeNotInRemote
			case StatusError:
				include = true // Always include errors for visibility
			}

			mu.Lock()
			report.ComparedPackages++
			switch result.Status {
			case StatusOutdated:
				report.OutdatedCount++
			case StatusNewer:
				report.NewerCount++
			case StatusUpToDate:
				report.UpToDateCount++
			case StatusNotInRemote:
				report.NotInRemoteCount++
			case StatusError:
				report.ErrorCount++
			}
			if include {
				report.Results = append(report.Results, result)
			}
			mu.Unlock()

			if opts.ProgressCallback != nil {
				opts.ProgressCallback(progress.Add(1), total)
			}
		}(pkg)
	}

	// Join every worker before touching the shared report so it is fully
	// populated and safe to read.
	wg.Wait()

	// Sort results by category/package for deterministic output.
	sortCompareResults(report.Results)

	if cancelled {
		return report, ctx.Err()
	}
	return report, nil
}

// sortCompareResults sorts compare results in place by category then package.
func sortCompareResults(results []CompareResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].Category != results[j].Category {
			return results[i].Category < results[j].Category
		}
		return results[i].Package < results[j].Package
	})
}

// comparePackageWithProvider compares a single package using a Provider
func comparePackageWithProvider(pkg PackageInfo, prov provider.Provider) CompareResult {
	result := CompareResult{
		Category:     pkg.Category,
		Package:      pkg.Package,
		LocalVersion: pkg.LatestVersion,
	}

	// Fetch remote versions
	remoteVersions, err := prov.GetPackageVersions(pkg.Category, pkg.Package)
	if err != nil {
		if err == provider.ErrNotFound {
			result.Status = StatusNotInRemote
			return result
		}
		result.Status = StatusError
		return result
	}

	if len(remoteVersions) == 0 {
		result.Status = StatusNotInRemote
		return result
	}

	// Find latest remote version (ignoring live/9999 ebuilds)
	remoteLatest := FindLatestVersionFiltered(remoteVersions, true)
	result.RemoteVersion = remoteLatest

	// If remote only has live versions, consider up-to-date
	if remoteLatest == "" {
		result.Status = StatusUpToDate
		result.RemoteVersion = "9999 (live only)"
		return result
	}

	// Compare versions
	cmp := ebuild.CompareVersions(pkg.LatestVersion, remoteLatest)
	switch {
	case cmp < 0:
		result.Status = StatusOutdated
	case cmp > 0:
		result.Status = StatusNewer
	default:
		result.Status = StatusUpToDate
	}

	return result
}

// FormatReport formats a comparison report for terminal output
// When synced packages are included, displays them in a separate section with status indicators
func FormatReport(report *CompareReport) string {
	var sb strings.Builder

	if len(report.Results) == 0 {
		sb.WriteString(output.Sprintf(output.Success, "All packages are up-to-date!"))
		return sb.String()
	}

	// Separate results by status
	var outdated, synced, other []CompareResult
	for _, r := range report.Results {
		switch r.Status {
		case StatusOutdated:
			outdated = append(outdated, r)
		case StatusUpToDate:
			synced = append(synced, r)
		default:
			other = append(other, r)
		}
	}

	// Format outdated section if any
	if len(outdated) > 0 {
		sb.WriteString(formatResultSection(outdated, "Outdated Packages (Bentoo < Gentoo)", output.Warning))
	}

	// Format synced section if any
	if len(synced) > 0 {
		sb.WriteString(formatResultSection(synced, "Up-to-Date Packages", output.Success))
	}

	// Format other results (newer, not-in-remote, errors) if any
	if len(other) > 0 {
		sb.WriteString(formatResultSection(other, "Other Packages", output.Info))
	}

	// Summary
	sb.WriteString("\n")
	if len(outdated) > 0 {
		fmt.Fprintf(&sb, "Outdated: %s | ",
			output.Sprint(output.Warning, fmt.Sprintf("%d", len(outdated))))
	}
	if len(synced) > 0 {
		fmt.Fprintf(&sb, "Up-to-date: %s | ",
			output.Sprint(output.Success, fmt.Sprintf("%d", len(synced))))
	}
	if len(other) > 0 {
		fmt.Fprintf(&sb, "Other: %s | ",
			output.Sprint(output.Info, fmt.Sprintf("%d", len(other))))
	}
	fmt.Fprintf(&sb, "Total: %d\n", len(report.Results))

	return sb.String()
}

// formatResultSection formats a section of results with a header and table
func formatResultSection(results []CompareResult, title string, headerColor *color.Color) string {
	var sb strings.Builder
	w := calculateColumnWidths(results)

	// Header
	sb.WriteString(output.Sprintf(headerColor, "\n%s:\n", title))
	sb.WriteString(formatTableLineWithStatus(w.pkg, w.cat, w.local, w.remote, w.status, "top"))
	sb.WriteString(formatTableRowWithStatus(w.pkg, w.cat, w.local, w.remote, w.status,
		"Package", "Category", "Bentoo Version", "Gentoo Version", "Status", true, nil))
	sb.WriteString(formatTableLineWithStatus(w.pkg, w.cat, w.local, w.remote, w.status, "mid"))

	// Data rows
	for _, r := range results {
		pkg := truncateString(r.Package, w.pkg)
		cat := truncateString(r.Category, w.cat)
		local := truncateString(r.LocalVersion, w.local)
		remote := truncateString(r.RemoteVersion, w.remote)
		status := truncateString(r.Status.String(), w.status)
		statusColor := getStatusColor(r.Status)
		sb.WriteString(formatTableRowWithStatus(w.pkg, w.cat, w.local, w.remote, w.status,
			pkg, cat, local, remote, status, false, statusColor))
	}

	sb.WriteString(formatTableLineWithStatus(w.pkg, w.cat, w.local, w.remote, w.status, "bottom"))

	return sb.String()
}

// columnWidths holds the calculated column widths for table formatting.
type columnWidths struct {
	pkg, cat, local, remote, status int
}

// calculateColumnWidths computes the optimal column widths for a set of compare results.
// It uses minimum widths based on header labels and caps maximum widths for readability.
func calculateColumnWidths(results []CompareResult) columnWidths {
	w := columnWidths{
		pkg:    7,  // "Package"
		cat:    8,  // "Category"
		local:  14, // "Bentoo Version"
		remote: 14, // "Gentoo Version"
		status: 13, // "Status"
	}

	for _, r := range results {
		if len(r.Package) > w.pkg {
			w.pkg = len(r.Package)
		}
		if len(r.Category) > w.cat {
			w.cat = len(r.Category)
		}
		if len(r.LocalVersion) > w.local {
			w.local = len(r.LocalVersion)
		}
		if len(r.RemoteVersion) > w.remote {
			w.remote = len(r.RemoteVersion)
		}
		if len(r.Status.String()) > w.status {
			w.status = len(r.Status.String())
		}
	}

	// Cap widths for readability
	if w.pkg > 30 {
		w.pkg = 30
	}
	if w.cat > 20 {
		w.cat = 20
	}

	return w
}

// getStatusColor returns the appropriate color for a CompareStatus
func getStatusColor(status CompareStatus) *color.Color {
	switch status {
	case StatusUpToDate:
		return output.Success
	case StatusOutdated:
		return output.Warning
	case StatusNewer:
		return output.Info
	case StatusNotInRemote:
		return output.Dim
	case StatusError:
		return output.Error
	default:
		return nil
	}
}

// formatTableLineWithStatus creates a horizontal table line with status column
func formatTableLineWithStatus(pkgW, catW, localW, remoteW, statusW int, position string) string {
	var left, mid, right, horiz string

	switch position {
	case "top":
		left, mid, right, horiz = "┌", "┬", "┐", "─"
	case "mid":
		left, mid, right, horiz = "├", "┼", "┤", "─"
	case "bottom":
		left, mid, right, horiz = "└", "┴", "┘", "─"
	}

	return fmt.Sprintf("%s%s%s%s%s%s%s%s%s%s%s\n",
		left, strings.Repeat(horiz, pkgW+2),
		mid, strings.Repeat(horiz, catW+2),
		mid, strings.Repeat(horiz, localW+2),
		mid, strings.Repeat(horiz, remoteW+2),
		mid, strings.Repeat(horiz, statusW+2), right)
}

// formatTableRowWithStatus creates a table row with status column
func formatTableRowWithStatus(pkgW, catW, localW, remoteW, statusW int, pkg, cat, local, remote, status string, header bool, statusColor *color.Color) string {
	if header {
		format := "│ %-*s │ %-*s │ %-*s │ %-*s │ %-*s │\n"
		row := fmt.Sprintf(format, pkgW, pkg, catW, cat, localW, local, remoteW, remote, statusW, status)
		return output.Sprint(output.Header, row)
	}

	// Format status with color if provided
	var statusStr string
	if statusColor != nil {
		statusStr = output.Sprintf(statusColor, "%-*s", statusW, status)
	} else {
		statusStr = fmt.Sprintf("%-*s", statusW, status)
	}

	// Build row with colored status
	return fmt.Sprintf("│ %-*s │ %-*s │ %-*s │ %-*s │ %s │\n",
		pkgW, pkg, catW, cat, localW, local, remoteW, remote, statusStr)
}

// formatTableLine creates a horizontal table line
func formatTableLine(pkgW, catW, localW, remoteW int, position string) string {
	var left, mid, right, horiz string

	switch position {
	case "top":
		left, mid, right, horiz = "┌", "┬", "┐", "─"
	case "mid":
		left, mid, right, horiz = "├", "┼", "┤", "─"
	case "bottom":
		left, mid, right, horiz = "└", "┴", "┘", "─"
	}

	return fmt.Sprintf("%s%s%s%s%s%s%s%s%s\n",
		left, strings.Repeat(horiz, pkgW+2),
		mid, strings.Repeat(horiz, catW+2),
		mid, strings.Repeat(horiz, localW+2),
		mid, strings.Repeat(horiz, remoteW+2), right)
}

// formatTableRow creates a table row
func formatTableRow(pkgW, catW, localW, remoteW int, pkg, cat, local, remote string, header bool) string {
	format := "│ %-*s │ %-*s │ %-*s │ %-*s │\n"
	row := fmt.Sprintf(format, pkgW, pkg, catW, cat, localW, local, remoteW, remote)

	if header {
		return output.Sprint(output.Header, row)
	}
	return row
}

// truncateString truncates a string to maxLen with ellipsis
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-1] + "…"
}

// FormatSummary formats a brief summary of the comparison
func FormatSummary(report *CompareReport) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Scanned: %d packages\n", report.TotalPackages)
	fmt.Fprintf(&sb, "Compared: %d packages (exist in both repos)\n", report.ComparedPackages-report.NotInRemoteCount)

	if report.OutdatedCount > 0 {
		sb.WriteString(output.Sprintf(output.Warning, "Outdated: %d\n", report.OutdatedCount))
	}
	if report.NewerCount > 0 {
		sb.WriteString(output.Sprintf(output.Info, "Newer in Bentoo: %d\n", report.NewerCount))
	}
	if report.UpToDateCount > 0 {
		sb.WriteString(output.Sprintf(output.Success, "Up-to-date: %d\n", report.UpToDateCount))
	}

	return sb.String()
}
