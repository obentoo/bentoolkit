package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/i18n"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// i18nDomain names bentoo's gettext catalog: <dir>/<lang>/LC_MESSAGES/bentoolkit.mo.
const i18nDomain = "bentoolkit"

// usageLabels returns the English headings of cobra's default usage template,
// each marked for extraction. TestUsageLabelsInCobraTemplate fails when a
// cobra upgrade renames one, instead of leaving it untranslated in silence.
func usageLabels() []string {
	return []string{
		i18n.N("Usage:"),
		i18n.N("Aliases:"),
		i18n.N("Examples:"),
		i18n.N("Available Commands:"),
		i18n.N("Additional Commands:"),
		i18n.N("Flags:"),
		i18n.N("Global Flags:"),
		i18n.N("Additional help topics:"),
		i18n.N(`Use "{{.CommandPath}} [command] --help" for more information about a command.`),
	}
}

// localizeCommandTree replaces the help text of root and every command under
// it — Short, Long, flag usages, cobra's usage headings and its implicit help
// command and --help flag — with the loaded catalog's translations. The text
// is declared with i18n.N when the tree is built, which happens before main
// loads the catalog, so it is translated here, once, before Execute. With no
// catalog loaded every lookup returns the English text and nothing changes.
func localizeCommandTree(root *cobra.Command) {
	root.InitDefaultHelpCmd()
	if help, _, err := root.Find([]string{"help"}); err == nil && help != root && help.Name() == "help" {
		help.Short = i18n.N("Help about any command")
		help.Long = fmt.Sprintf(i18n.T("Help provides help for any command in the application.\nSimply type %s help [path to command] for full details."), root.DisplayName())
	}

	// Longest first: "Flags:" is inside "Global Flags:", and replacing it
	// first would leave "Global <translation>" for no label to match.
	labels := usageLabels()
	slices.SortStableFunc(labels, func(a, b string) int { return len(b) - len(a) })
	tpl := root.UsageTemplate()
	for _, label := range labels {
		tpl = strings.ReplaceAll(tpl, label, i18n.T(label))
	}
	root.SetUsageTemplate(tpl)

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.Short = i18n.T(c.Short)
		c.Long = i18n.T(c.Long)
		c.InitDefaultHelpFlag()
		if h := c.Flags().Lookup("help"); h != nil {
			h.Usage = fmt.Sprintf(i18n.T("help for %s"), c.DisplayName())
		}
		translate := func(f *pflag.Flag) { f.Usage = i18n.T(f.Usage) }
		c.LocalNonPersistentFlags().VisitAll(translate)
		c.PersistentFlags().VisitAll(translate)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}
