package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/i18n"
	"github.com/obentoo/bentoolkit/internal/common/i18n/i18ntest"
	"github.com/spf13/cobra"
)

// TestUsageLabelsInCobraTemplate fails when a cobra upgrade renames a heading
// of its default usage template: the replacement in localizeCommandTree would
// then match nothing and leave that heading in English without a word.
func TestUsageLabelsInCobraTemplate(t *testing.T) {
	tpl := (&cobra.Command{Use: "x"}).UsageTemplate()
	for _, label := range usageLabels() {
		if !strings.Contains(tpl, label) {
			t.Errorf("cobra's usage template no longer contains %q", label)
		}
	}
}

func helpOutput(t *testing.T, root *cobra.Command, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return buf.String()
}

// Not parallel: it loads the process-wide catalog, and t.Setenv refuses a
// parallel test anyway. Parallel tests resume only after it has finished and
// its cleanup has unloaded the catalog.
func TestLocalizeCommandTree(t *testing.T) {
	dir := i18ntest.WriteCatalog(t, t.TempDir(), "pt_BR", i18nDomain, map[string]string{
		"Show commit history":                                "Mostrar histórico de commits",
		"Show the commit history of the overlay repository.": "Mostrar o histórico de commits do repositório do overlay.",
		"Number of commits to show":                          "Número de commits a mostrar",
		"Disable colored output":                             "Desativar a saída colorida",
		"Usage:":                                             "Uso:",
		"Flags:":                                             "Opções:",
		"Global Flags:":                                      "Opções globais:",
		"Available Commands:":                                "Subcomandos disponíveis:",
		"help for %s":                                        "ajuda para %s",
		"Help about any command":                             "Ajuda sobre qualquer subcomando",
		`Use "{{.CommandPath}} [command] --help" for more information about a command.`: `Use "{{.CommandPath}} [cmd] --help" para mais informações.`,
	})
	t.Setenv("BENTOO_LOCALEDIR", dir)
	t.Setenv("LANGUAGE", "")
	t.Setenv("LC_ALL", "pt_BR.UTF-8")
	t.Cleanup(i18n.Reset)
	if lang, err := i18n.Init(i18nDomain); err != nil || lang != "pt_BR" {
		t.Fatalf("Init = %q, %v", lang, err)
	}

	root := newRootCmd()
	localizeCommandTree(root)

	out := helpOutput(t, root, "overlay", "log", "--help")
	for _, want := range []string{
		"Mostrar o histórico de commits do repositório do overlay.",
		"Uso:",
		"Opções:",
		"Opções globais:",
		"Número de commits a mostrar",
		"ajuda para log",
		"Desativar a saída colorida", // a persistent flag of the root, inherited
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay log --help lacks %q:\n%s", want, out)
		}
	}

	out = helpOutput(t, root, "overlay", "--help")
	for _, want := range []string{"Subcomandos disponíveis:", "Mostrar histórico de commits", `Use "bentoo overlay [cmd] --help" para mais informações.`} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay --help lacks %q:\n%s", want, out)
		}
	}

	out = helpOutput(t, root, "--help")
	if !strings.Contains(out, "Ajuda sobre qualquer subcomando") {
		t.Errorf("the implicit help command is not translated:\n%s", out)
	}
	if !strings.Contains(out, "A collection of tools for managing Bentoo Linux overlay and packages.") {
		t.Errorf("an untranslated message must stay in English:\n%s", out)
	}
}

// TestLocalizeCommandTreeWithoutCatalog pins the default: no catalog, so the
// help text is byte-for-byte what an unlocalized tree prints.
func TestLocalizeCommandTreeWithoutCatalog(t *testing.T) {
	i18n.Reset()
	want := helpOutput(t, newRootCmd(), "overlay", "commit", "--help")
	root := newRootCmd()
	localizeCommandTree(root)
	if got := helpOutput(t, root, "overlay", "commit", "--help"); got != want {
		t.Errorf("localizing with no catalog changed the help:\n--- want\n%s\n--- got\n%s", want, got)
	}
}
