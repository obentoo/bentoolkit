// Package registry is the packages.toml registry: it loads and saves the file,
// validates each record (including its meta.fetch_* spec, judged by the fetch
// package's own parser), lints and repairs it, renders a record, prunes and
// removes records, and migrates auto-disabled entries.
//
// It imports fetch and ebuilds and nothing above them; the parsers, the LLM
// fixers and the update core build on it (story 061).
package registry
