# Bentoolkit development

Back to the [README](../README.md).

### Security Audit

```bash
# Run module verification and vulnerability check
make audit

# Install govulncheck if not available
go install golang.org/x/vuln/cmd/govulncheck@latest
```

### Project Structure

```
bentoolkit/
├── cmd/bentoo/                 # CLI commands
│   ├── main.go                 # Entry point
│   ├── overlay_add.go          # overlay add command
│   ├── overlay_analyze.go      # overlay analyze command (LLM schema generation)
│   ├── overlay_autoupdate.go   # overlay autoupdate command
│   ├── overlay_commit.go       # overlay commit command
│   ├── overlay_compare.go      # overlay compare command
│   ├── overlay_diff.go         # overlay diff command
│   ├── overlay_init.go         # overlay init command
│   ├── overlay_log.go          # overlay log command
│   ├── overlay_manifest.go     # overlay manifest command
│   ├── overlay_prune.go        # overlay prune command
│   ├── overlay_pull.go         # overlay pull command (alias: sync)
│   ├── overlay_push.go         # overlay push command
│   ├── overlay_rename.go       # overlay rename command
│   └── overlay_status.go       # overlay status command
├── internal/
│   ├── autoupdate/             # Autoupdate subsystem
│   │   ├── llm.go              # LLM provider interface and Claude client
│   │   ├── openai.go           # OpenAI client
│   │   ├── ollama.go           # Ollama (local) client
│   │   ├── httpclient.go       # HTTP client with retry and circuit breaker
│   │   ├── rate_limiter.go     # Rate limiter (LLM + HTTP, LRU eviction)
│   │   ├── config.go           # packages.toml schema configuration
│   │   ├── checker.go          # Version checking orchestration
│   │   ├── analyzer.go         # Schema analysis
│   │   ├── applier.go          # Version update applicator
│   │   ├── parser.go           # Parser implementations (json/regex/html)
│   │   └── cache.go            # Analysis result caching
│   └── common/
│       ├── config/             # Configuration loading (~/.config/bentoo/config.yaml)
│       ├── ebuild/             # Ebuild parsing and version comparison
│       ├── git/                # Git operations wrapper
│       ├── github/             # GitHub API client (legacy)
│       ├── logger/             # Structured logging
│       ├── output/             # Terminal output helpers
│       ├── version/            # Version utilities
│       └── provider/           # Repository providers
│           ├── interface.go    # Provider interface
│           ├── factory.go      # Provider factory
│           ├── github.go       # GitHub API provider
│           ├── gitlab.go       # GitLab API provider
│           └── gitclone.go     # Git clone provider
├── Makefile                    # Build targets
└── README.md
```
