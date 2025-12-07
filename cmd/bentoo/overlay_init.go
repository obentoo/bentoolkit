package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoo-tools/internal/common/config"
	"github.com/obentoo/bentoo-tools/internal/common/logger"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize bentoo configuration",
	Long: `Initialize bentoo configuration interactively.
Creates a config file with overlay path and git settings.`,
	Run: runInit,
}

func init() {
	overlayCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) {
	reader := bufio.NewReader(os.Stdin)

	// Check if config already exists
	existingPath, _ := config.FindConfigPath()
	if _, err := os.Stat(existingPath); err == nil {
		logger.Warn("Config already exists at: %s", existingPath)
		fmt.Print("Overwrite? [y/N]: ")
		input, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(input)) != "y" {
			logger.Info("Aborted.")
			return
		}
	}

	cfg := &config.Config{}

	// Get overlay path
	fmt.Println()
	logger.Info("Bentoo Overlay Configuration")
	fmt.Println()

	defaultOverlayPath := "/var/db/repos/bentoo"
	fmt.Printf("Overlay path [%s]: ", defaultOverlayPath)
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
		logger.Warn("Path does not exist: %s", overlayPath)
		fmt.Print("Create it? [y/N]: ")
		input, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(input)) == "y" {
			if err := os.MkdirAll(overlayPath, 0755); err != nil {
				logger.Error("Failed to create directory: %v", err)
				os.Exit(1)
			}
			logger.Info("Created directory: %s", overlayPath)
		}
	}

	cfg.Overlay.Path = overlayPath

	// Get remote name
	fmt.Print("Git remote name [origin]: ")
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
		logger.Warn("Git user not configured in ~/.gitconfig")
		logger.Info("You can configure it in bentoo or run:")
		logger.Info("  git config --global user.name \"Your Name\"")
		logger.Info("  git config --global user.email \"your@email.com\"")
		fmt.Println()

		fmt.Print("Git user name: ")
		user, _ = reader.ReadString('\n')
		cfg.Git.User = strings.TrimSpace(user)

		fmt.Print("Git email: ")
		email, _ = reader.ReadString('\n')
		cfg.Git.Email = strings.TrimSpace(email)
	} else {
		logger.Info("Using git config: %s <%s>", user, email)
	}

	// Save config
	configPath, _ := config.DefaultConfigPath()
	if err := cfg.SaveTo(configPath); err != nil {
		logger.Error("Failed to save config: %v", err)
		os.Exit(1)
	}

	fmt.Println()
	logger.Info("Configuration saved to: %s", configPath)
	fmt.Println()
	logger.Info("You can now use:")
	logger.Info("  bentoo overlay status  - View pending changes")
	logger.Info("  bentoo overlay add     - Stage changes")
	logger.Info("  bentoo overlay commit  - Commit with auto-generated message")
	logger.Info("  bentoo overlay push    - Push to remote")
}
