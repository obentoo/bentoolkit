package main

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestFailureDetail_FoldsTheOptionInfosIntoACount: a CMake project declares
// hundreds of options; the failed row keeps the finding that failed it.
func TestFailureDetail_FoldsTheOptionInfosIntoACount(t *testing.T) {
	gate := validate.GateResult{Gate: validate.GateOptions, Findings: []validate.Finding{
		{Gate: validate.GateOptions, Severity: validate.SeverityWarning, Detail: "passes -DWITH_GOLD= but no CMake file mentions it"},
		{Gate: validate.GateOptions, Severity: validate.SeverityInfo, Detail: "upstream declares WITH_A"},
		{Gate: validate.GateOptions, Severity: validate.SeverityInfo, Detail: "upstream declares WITH_B"},
	}}
	got := failureDetail(validate.EbuildResult{Gates: []validate.GateResult{gate}})
	if !strings.Contains(got, "WITH_GOLD") || strings.Contains(got, "WITH_A") || !strings.Contains(got, "2 option(s) upstream declares") {
		t.Errorf("detail = %q; want the warning and a count of the two infos", got)
	}
}
