package main

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// A --check row whose gates ran on a locally generated vendor tarball says so,
// even when its reason is the plan's and is therefore not repeated; a row
// without the note prints none.
func TestCheckRowShowsVendorNote(t *testing.T) {
	entry := validationPlanEntry{Package: "dev-go/govulncheck", Version: "1.9.0", Depth: "compile", Reason: "class: patch"}
	gates := []validate.GateResult{{Gate: validate.GateCompile, Outcome: validate.OutcomePass, Reason: "compiled"}}
	noted := validate.EbuildResult{Package: entry.Package, Version: entry.Version, Depth: "compile",
		DepthReason: entry.Reason, Gates: gates, Notes: []string{autoupdate.VendorGeneratedNote}}
	plain := noted
	plain.Package, plain.Notes = "dev-go/other", nil
	plainEntry := entry
	plainEntry.Package = "dev-go/other"

	rows := []report.ValidationRow{validationRowFacts(entry, noted), validationRowFacts(plainEntry, plain)}
	r := report.AutoupdateCheck{
		Scanned: []report.PackageResult{{Package: entry.Package}, {Package: plainEntry.Package}},
		Plan:    []report.PlanEntry{{Package: entry.Package}, {Package: plainEntry.Package}},
		Results: rows,
	}
	// The sections are the content all three render modes share, so a note
	// carried there reaches every one of them.
	details := map[string]string{}
	for _, sec := range finishedRun(r).Sections(report.SectionOptions{}) {
		for _, row := range sec.Rows.Rows {
			if len(row.Cells) > 0 {
				details[row.Cells[0]] += row.Detail
			}
		}
	}
	if got := details["dev-go/govulncheck"]; !strings.Contains(got, autoupdate.VendorGeneratedNote) {
		t.Fatalf("the dev-go/govulncheck row detail %q does not carry the vendor note", got)
	}
	if got := details["dev-go/other"]; strings.Contains(got, autoupdate.VendorGeneratedNote) {
		t.Fatalf("a row without the note prints it: %q", got)
	}
}
