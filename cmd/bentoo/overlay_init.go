package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/i18n"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/spf13/cobra"
)

// newInitCmd builds `overlay init`.
func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: i18n.N("Initialize bentoo configuration"),
		Long: i18n.N(`Initialize bentoo configuration interactively.
Creates a config file with overlay path and git settings.`),
		RunE: runInit,
	}
	return cmd
}

func runInit(cmd *cobra.Command, args []string) error {
	log := logging.FromContext(commandContext(cmd))
	reader := bufio.NewReader(os.Stdin)

	// Check if config already exists
	existingPath, _ := config.FindConfigPath()
	if _, err := os.Stat(existingPath); err == nil {
		uiWarn(fmt.Sprintf(i18n.T("Config already exists at: %s"), existingPath))
		fmt.Print(i18n.T("Overwrite? [y/N]: "))
		input, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(input)) != "y" {
			uiInfo(i18n.T("Aborted."))
			return nil
		}
	}

	cfg := &config.Config{}

	// Get overlay path
	fmt.Println()
	uiInfo(i18n.T("Bentoo Overlay Configuration"))
	fmt.Println()

	defaultOverlayPath := "/var/db/repos/bentoo"
	fmt.Printf(i18n.T("Overlay path [%s]: "), defaultOverlayPath)
	overlayPath, _ := reader.ReadString('\n')
	overlayPath = strings.TrimSpace(overlayPath)
	if overlayPath == "" {
		overlayPath = defaultOverlayPath
	}

	// Expand ~ if present
	if strings.HasPrefix(overlayPath, "~") {
		home, _ := os.UserHomeDir()
		overlayPath = filepath.Join(home, overlayPath[1:])
	}

	// Validate path exists
	if _, err := os.Stat(overlayPath); os.IsNotExist(err) {
		uiWarn(fmt.Sprintf(i18n.T("Path does not exist: %s"), overlayPath))
		fmt.Print(i18n.T("Create it? [y/N]: "))
		input, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(input)) == "y" {
			if err := os.MkdirAll(overlayPath, 0o750); err != nil {
				log.Error("Failed to create directory", "path", overlayPath, "err", err)
				return exitWith(1)
			}
			uiInfo(fmt.Sprintf(i18n.T("Created directory: %s"), overlayPath))
		}
	}

	cfg.Overlay.Path = overlayPath

	// Get remote name
	fmt.Print(i18n.T("Git remote name [origin]: "))
	remote, _ := reader.ReadString('\n')
	remote = strings.TrimSpace(remote)
	if remote == "" {
		remote = "origin"
	}
	cfg.Overlay.Remote = remote

	// Check if git user is configured globally
	user, email, err := cfg.GetGitUser()
	if err != nil {
		fmt.Println()
		uiWarn(i18n.T("Git user not configured in ~/.gitconfig"))
		uiInfo(i18n.T("You can configure it in bentoo or run:"))
		uiInfo(i18n.T("  git config --global user.name \"Your Name\""))
		uiInfo(i18n.T("  git config --global user.email \"your@email.com\""))
		fmt.Println()

		fmt.Print(i18n.T("Git user name: "))
		user, _ = reader.ReadString('\n')
		cfg.Git.User = strings.TrimSpace(user)

		fmt.Print(i18n.T("Git email: "))
		email, _ = reader.ReadString('\n')
		cfg.Git.Email = strings.TrimSpace(email)
	} else {
		uiInfo(fmt.Sprintf(i18n.T("Using git config: %s <%s>"), user, email))
	}

	// Save config
	configPath, _ := config.DefaultConfigPath()
	if err := cfg.SaveTo(configPath); err != nil {
		log.Error("Failed to save config", "err", err)
		return exitWith(1)
	}

	fmt.Println()
	uiInfo(fmt.Sprintf(i18n.T("Configuration saved to: %s"), configPath))
	fmt.Println()
	uiInfo(i18n.T("You can now use:"))
	uiInfo(i18n.T("  bentoo overlay status  - View pending changes"))
	uiInfo(i18n.T("  bentoo overlay add     - Stage changes"))
	uiInfo(i18n.T("  bentoo overlay commit  - Commit with auto-generated message"))
	uiInfo(i18n.T("  bentoo overlay push    - Push to remote"))
	return nil
}
