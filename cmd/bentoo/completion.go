package main

import (
	"os"

	"github.com/spf13/cobra"
)

// newCompletionCmd builds `completion`.
func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion scripts",
		Long: `Generate shell completion scripts for bentoo.

To load completions:

Bash:
  $ source <(bentoo completion bash)
  # To load completions for each session, execute once:
  # Linux:
  $ bentoo completion bash > /etc/bash_completion.d/bentoo
  # macOS:
  $ bentoo completion bash > $(brew --prefix)/etc/bash_completion.d/bentoo

Zsh:
  # If shell completion is not already enabled in your environment,
  # you will need to enable it. You can execute the following once:
  $ echo "autoload -U compinit; compinit" >> ~/.zshrc
  # To load completions for each session, execute once:
  $ bentoo completion zsh > "${fpath[1]}/_bentoo"
  # You will need to start a new shell for this setup to take effect.

Fish:
  $ bentoo completion fish | source
  # To load completions for each session, execute once:
  $ bentoo completion fish > ~/.config/fish/completions/bentoo.fish

PowerShell:
  PS> bentoo completion powershell | Out-String | Invoke-Expression
  # To load completions for every new session, run:
  PS> bentoo completion powershell > bentoo.ps1
  # and source this file from your PowerShell profile.
`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		// The completions are generated from THIS command's own root, not from the
		// package-level rootCmd. Since newRootCmd builds a fresh tree per call, a
		// global reference here would make an in-process run emit completions for a
		// tree it is not part of — and it would also make rootCmd's initialiser
		// depend on itself, which Go rejects as an initialisation cycle.
		Run: func(cmd *cobra.Command, args []string) {
			switch args[0] {
			case "bash":
				cmd.Root().GenBashCompletion(os.Stdout)
			case "zsh":
				cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				cmd.Root().GenFishCompletion(os.Stdout, true)
			case "powershell":
				cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
			}
		},
	}
	return cmd
}
