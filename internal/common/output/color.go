package output

import (
	"fmt"
	"os"

	"github.com/fatih/color"
)

var (
	// Status colors
	Added     = color.New(color.FgGreen)
	Modified  = color.New(color.FgYellow)
	Deleted   = color.New(color.FgRed)
	Renamed   = color.New(color.FgCyan)
	Untracked = color.New(color.FgMagenta)

	// Message colors
	Success = color.New(color.FgGreen)
	Warning = color.New(color.FgYellow)
	Error   = color.New(color.FgRed)
	Info    = color.New(color.FgCyan)
	Dim     = color.New(color.Faint)

	// Structural colors
	Header  = color.New(color.FgWhite, color.Bold)
	Package = color.New(color.FgBlue, color.Bold)
)

// NoColor disables color output
func NoColor() {
	color.NoColor = true
}

// ForceColor enables color output even when not a TTY
func ForceColor() {
	color.NoColor = false
}

// IsTerminal returns true if stdout is a terminal
func IsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// StatusColor returns the appropriate color for a git status
func StatusColor(status string) *color.Color {
	switch status {
	case "Added":
		return Added
	case "Modified":
		return Modified
	case "Deleted":
		return Deleted
	case "Renamed":
		return Renamed
	case "Untracked":
		return Untracked
	default:
		return color.New(color.Reset)
	}
}

// PrintSuccess prints a success message
func PrintSuccess(format string, args ...interface{}) {
	Success.Printf("✓ "+format+"\n", args...)
}

// PrintError prints an error message
func PrintError(format string, args ...interface{}) {
	Error.Fprintf(os.Stderr, "✗ "+format+"\n", args...)
}

// PrintWarning prints a warning message
func PrintWarning(format string, args ...interface{}) {
	Warning.Printf("⚠ "+format+"\n", args...)
}

// PrintInfo prints an info message
func PrintInfo(format string, args ...interface{}) {
	Info.Printf("→ "+format+"\n", args...)
}

// Sprintf returns a colored string without printing
func Sprintf(c *color.Color, format string, args ...interface{}) string {
	return c.Sprintf(format, args...)
}

// Sprint returns a colored string without printing
func Sprint(c *color.Color, a ...interface{}) string {
	return c.Sprint(a...)
}

// Printf prints with color
func Printf(c *color.Color, format string, args ...interface{}) {
	c.Printf(format, args...)
}

// Println prints with color and newline
func Println(c *color.Color, a ...interface{}) {
	c.Println(a...)
}

// FormatStatus formats a status string with appropriate color
func FormatStatus(status string) string {
	c := StatusColor(status)
	return c.Sprintf("[%s]", status)
}

// FormatPackage formats a package name with color
func FormatPackage(category, pkg string) string {
	if category != "" {
		return Package.Sprintf("%s/%s", category, pkg)
	}
	return Package.Sprint(pkg)
}

// Box prints a boxed message
func Box(title, content string) {
	fmt.Println()
	Header.Println("┌─ " + title + " ─")
	fmt.Println("│")
	fmt.Println("│  " + content)
	fmt.Println("│")
	Header.Println("└────────────────")
	fmt.Println()
}
