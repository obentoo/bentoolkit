module github.com/obentoo/bentoolkit

go 1.26.0

// Pinned so the CI runner does not land on the unpatched 1.26.0. Every
// golang.org/x release from late August 2026 on declares `go 1.26.0`, which
// forced the directive above from `go 1.26` to `go 1.26.0` -- and setup-go
// reads a patch-qualified `go` directive as an exact version, which would have
// frozen CI eight patch releases behind. It prefers `toolchain` when present,
// so this line is what the runner installs. Bump it on each Go patch release.
toolchain go1.26.8

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/PuerkitoBio/goquery v1.13.0
	github.com/antchfx/htmlquery v1.3.6
	github.com/antchfx/xpath v1.3.8
	github.com/aymanbagabas/go-udiff v0.4.1
	github.com/charmbracelet/bubbles v1.0.0
	github.com/charmbracelet/bubbletea v1.3.10
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/charmbracelet/x/ansi v0.11.8
	github.com/charmbracelet/x/exp/teatest v0.0.0-20260920004010-53e2afe73ae5
	github.com/charmbracelet/x/term v0.2.2
	github.com/chromedp/cdproto v0.0.0-20260912003405-686a5c723acc
	github.com/chromedp/chromedp v0.16.0
	github.com/fatih/color v1.19.0
	github.com/leanovate/gopter v0.2.11
	github.com/muesli/termenv v0.16.0
	github.com/mxschmitt/playwright-go v0.6201.1
	github.com/sony/gobreaker v1.0.0
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	go.uber.org/goleak v1.3.0
	golang.org/x/time v0.16.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/andybalholm/cascadia v1.3.5 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/exp/golden v0.0.0-20260920004010-53e2afe73ae5 // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/deckarep/golang-set/v2 v2.9.0 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/go-json-experiment/json v0.0.0-20260820222146-c27c302e5fc3 // indirect
	github.com/go-stack/stack v1.8.1 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.30 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v1.2.0 // indirect
	go.mongodb.org/mongo-driver v1.17.10 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/telemetry v0.0.0-20260921160320-bdcd072333a6 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/tools v0.50.0 // indirect
	golang.org/x/vuln v1.8.0 // indirect
)

tool golang.org/x/vuln/cmd/govulncheck
