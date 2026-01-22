package overlay

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

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
	// ProgressCallback is called for each package processed
	ProgressCallback func(current, total int, pkg string)
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

// Compare compares local packages against a remote GitHub repository
func Compare(localPackages []PackageInfo, client *github.Client, opts CompareOptions) (*CompareReport, error) {
	report := &CompareReport{
		TotalPackages: len(localPackages),
		Results:       []CompareResult{},
	}

	for i, pkg := range localPackages {
		// Progress callback
		if opts.ProgressCallback != nil {
			opts.ProgressCallback(i+1, len(localPackages), pkg.FullName())
		}

		result := comparePackage(pkg, client)
		report.ComparedPackages++

		// Update counters
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

		// Filter based on options using switch for clarity
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
		if !include {
			continue
		}

		report.Results = append(report.Results, result)
	}

	// Sort results by category/package
	sort.Slice(report.Results, func(i, j int) bool {
		if report.Results[i].Category != report.Results[j].Category {
			return report.Results[i].Category < report.Results[j].Category
		}
		return report.Results[i].Package < report.Results[j].Package
	})

	return report, nil
}

// CompareWithProvider compares local packages against an upstream repository using any Provider
func CompareWithProvider(localPackages []PackageInfo, prov provider.Provider, opts CompareOptions) (*CompareReport, error) {
	report := &CompareReport{
		TotalPackages: len(localPackages),
		Results:       []CompareResult{},
	}

	for i, pkg := range localPackages {
		// Progress callback
		if opts.ProgressCallback != nil {
			opts.ProgressCallback(i+1, len(localPackages), pkg.FullName())
		}

		result := comparePackageWithProvider(pkg, prov)
		report.ComparedPackages++

		// Update counters
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

		// Filter based on options using switch for clarity
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
		if !include {
			continue
		}

		report.Results = append(report.Results, result)
	}

	// Sort results by category/package
	sort.Slice(report.Results, func(i, j int) bool {
		if report.Results[i].Category != report.Results[j].Category {
			return report.Results[i].Category < report.Results[j].Category
		}
		return report.Results[i].Package < report.Results[j].Package
	})

	return report, nil
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

// comparePackage compares a single package against the remote repository
// Deprecated: Use comparePackageWithProvider instead
func comparePackage(pkg PackageInfo, client *github.Client) CompareResult {
	result := CompareResult{
		Category:     pkg.Category,
		Package:      pkg.Package,
		LocalVersion: pkg.LatestVersion,
	}

	// Fetch remote versions
	remoteVersions, err := client.GetPackageVersions(pkg.Category, pkg.Package)
	if err != nil {
		if err == github.ErrNotFound {
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
		sb.WriteString(fmt.Sprintf("Outdated: %s | ",
			output.Sprint(output.Warning, fmt.Sprintf("%d", len(outdated)))))
	}
	if len(synced) > 0 {
		sb.WriteString(fmt.Sprintf("Up-to-date: %s | ",
			output.Sprint(output.Success, fmt.Sprintf("%d", len(synced)))))
	}
	if len(other) > 0 {
		sb.WriteString(fmt.Sprintf("Other: %s | ",
			output.Sprint(output.Info, fmt.Sprintf("%d", len(other)))))
	}
	sb.WriteString(fmt.Sprintf("Total: %d\n", len(report.Results)))

	return sb.String()
}

// formatResultSection formats a section of results with a header and table
func formatResultSection(results []CompareResult, title string, headerColor *color.Color) string {
	var sb strings.Builder

	// Calculate column widths
	maxPkgLen := 7     // "Package"
	maxCatLen := 8     // "Category"
	maxLocalLen := 14  // "Bentoo Version"
	maxRemoteLen := 14 // "Gentoo Version"
	maxStatusLen := 13 // "Status"

	for _, r := range results {
		if len(r.Package) > maxPkgLen {
			maxPkgLen = len(r.Package)
		}
		if len(r.Category) > maxCatLen {
			maxCatLen = len(r.Category)
		}
		if len(r.LocalVersion) > maxLocalLen {
			maxLocalLen = len(r.LocalVersion)
		}
		if len(r.RemoteVersion) > maxRemoteLen {
			maxRemoteLen = len(r.RemoteVersion)
		}
		if len(r.Status.String()) > maxStatusLen {
			maxStatusLen = len(r.Status.String())
		}
	}

	// Cap widths for readability
	if maxPkgLen > 30 {
		maxPkgLen = 30
	}
	if maxCatLen > 20 {
		maxCatLen = 20
	}

	// Header
	sb.WriteString(output.Sprintf(headerColor, "\n%s:\n", title))
	sb.WriteString(formatTableLineWithStatus(maxPkgLen, maxCatLen, maxLocalLen, maxRemoteLen, maxStatusLen, "top"))
	sb.WriteString(formatTableRowWithStatus(maxPkgLen, maxCatLen, maxLocalLen, maxRemoteLen, maxStatusLen,
		"Package", "Category", "Bentoo Version", "Gentoo Version", "Status", true, nil))
	sb.WriteString(formatTableLineWithStatus(maxPkgLen, maxCatLen, maxLocalLen, maxRemoteLen, maxStatusLen, "mid"))

	// Data rows
	for _, r := range results {
		pkg := truncateString(r.Package, maxPkgLen)
		cat := truncateString(r.Category, maxCatLen)
		local := truncateString(r.LocalVersion, maxLocalLen)
		remote := truncateString(r.RemoteVersion, maxRemoteLen)
		status := truncateString(r.Status.String(), maxStatusLen)
		statusColor := getStatusColor(r.Status)
		sb.WriteString(formatTableRowWithStatus(maxPkgLen, maxCatLen, maxLocalLen, maxRemoteLen, maxStatusLen,
			pkg, cat, local, remote, status, false, statusColor))
	}

	sb.WriteString(formatTableLineWithStatus(maxPkgLen, maxCatLen, maxLocalLen, maxRemoteLen, maxStatusLen, "bottom"))

	return sb.String()
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
	statusStr := status
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

	sb.WriteString(fmt.Sprintf("Scanned: %d packages\n", report.TotalPackages))
	sb.WriteString(fmt.Sprintf("Compared: %d packages (exist in both repos)\n", report.ComparedPackages-report.NotInRemoteCount))

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
