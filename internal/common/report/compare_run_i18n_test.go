package report

import (
	"slices"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/i18n"
	"github.com/obentoo/bentoolkit/internal/common/i18n/i18ntest"
)

// The adapter's fixed sentences: a run note and two Unanswered reasons. They
// live in cmd/bentoo, so this package cannot name their constants; the strings
// are repeated here, and TestCatalogsInSync keeps both copies in the catalog.
const (
	i18nTestNote   = "Nothing is deleted here: act on the recommendation to remove with 'bentoo overlay prune', which decides on content — every version the two trees share, plus the whole files/ tree — and never on the verdict alone."
	i18nTestReason = "versions differ; content check needs equal ones"
	i18nTestError  = "upstream lookup failed: dial tcp: connection refused"
)

func i18nTestRun() CompareRun {
	return CompareRun{
		Repository: "gentoo",
		Notes:      []string{i18nTestNote, "3 of the 5 packages compared were found to have no ::gentoo counterpart."},
		Unanswered: []CompareUnanswered{
			{Package: "llvm-core/llvm", Local: "23.1.5", Remote: "23.1.6", Reason: i18nTestReason},
			{Package: "dev-lang/rust", Local: "1.99.0", Remote: "1.99.0", Reason: i18nTestError},
		},
	}
}

func i18nTestText(sections []Section) string {
	var b strings.Builder
	for _, s := range sections {
		lines := append([]string{s.Title}, s.Lead...)
		lines = append(lines, s.Notes...)
		for _, r := range s.Rows.Rows {
			lines = append(lines, strings.Join(r.Cells, " | "))
		}
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// TestCompareRunTranslatesFixedSentences: the terminal report shows the run's
// fixed notes and the closed Unanswered vocabulary in the reader's language,
// while a composed note and an error's own text, which have no catalog entry,
// stay as written. An export (SectionOptions.English) stays English, and the
// payload is never rewritten by rendering it.
//
// Not parallel: it loads the process-wide catalog.
func TestCompareRunTranslatesFixedSentences(t *testing.T) {
	dir := i18ntest.WriteCatalog(t, t.TempDir(), "pt_BR", "bentoolkit", map[string]string{
		i18nTestNote:   "Nada é removido aqui.",
		i18nTestReason: "as versões diferem",
	})
	t.Setenv("BENTOO_LOCALEDIR", dir)
	t.Setenv("LANGUAGE", "")
	t.Setenv("LC_ALL", "pt_BR.UTF-8")
	t.Cleanup(i18n.Reset)
	if lang, err := i18n.Init("bentoolkit"); err != nil || lang != "pt_BR" {
		t.Fatalf("Init = %q, %v", lang, err)
	}

	run := i18nTestRun()
	before := slices.Clone(run.Notes)

	term := i18nTestText(run.Sections(SectionOptions{}))
	for _, want := range []string{
		"Nada é removido aqui.",
		"llvm-core/llvm | 23.1.5 | 23.1.6 | as versões diferem",
		"3 of the 5 packages compared were found to have no ::gentoo counterpart.",
		i18nTestError,
	} {
		if !strings.Contains(term, want) {
			t.Errorf("terminal report lacks %q:\n%s", want, term)
		}
	}

	export := i18nTestText(run.Sections(SectionOptions{English: true}))
	for _, want := range []string{i18nTestNote, i18nTestReason} {
		if !strings.Contains(export, want) {
			t.Errorf("an export must stay English, lacks %q:\n%s", want, export)
		}
	}
	if strings.Contains(export, "Nada é removido") {
		t.Errorf("an export carries a translation:\n%s", export)
	}

	if !slices.Equal(run.Notes, before) || run.Unanswered[0].Reason != i18nTestReason {
		t.Errorf("rendering rewrote the payload: notes %q, reason %q", run.Notes, run.Unanswered[0].Reason)
	}
}
