module github.com/obentoo/bentoolkit

go 1.27.0

// Pinned so the CI runner does not land on an unpatched release. Every
// golang.org/x release from late August 2026 on declares `go 1.26.0`, which
// forced the directive above from `go 1.26` to `go 1.26.0` -- and setup-go
// reads a patch-qualified `go` directive as an exact version, which would have
// frozen CI eight patch releases behind. It prefers `toolchain` when present,
// so this line is what the runner installs. Bump it on each Go patch release.
toolchain go1.27.3

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/PuerkitoBio/goquery v1.13.0
	github.com/antchfx/htmlquery v1.3.7
	github.com/antchfx/xpath v1.3.9
	github.com/aymanbagabas/go-udiff v0.4.1
	github.com/charmbracelet/bubbles v1.0.0
	github.com/charmbracelet/bubbletea v1.3.10
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/charmbracelet/x/ansi v0.11.9
	github.com/charmbracelet/x/exp/teatest v0.0.0-20261018003744-cb897cb4ed72
	github.com/charmbracelet/x/term v0.2.2
	github.com/chromedp/cdproto v0.158.4
	github.com/chromedp/chromedp v0.21.0
	github.com/fatih/color v1.19.0
	github.com/godbus/dbus/v5 v5.2.4
	github.com/leanovate/gopter v0.2.11
	github.com/muesli/termenv v0.16.0
	github.com/sony/gobreaker v1.0.0
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	go.uber.org/goleak v1.3.0
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/time v0.16.0
)

require (
	github.com/andybalholm/cascadia v1.3.5 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/exp/golden v0.0.0-20260920004010-53e2afe73ae5 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/go-json-experiment/json v0.0.0-20260820222146-c27c302e5fc3 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-colorable v0.1.16 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.31 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v1.2.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261012234344-3c2f9838340e // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
)

tool golang.org/x/vuln/cmd/govulncheck
