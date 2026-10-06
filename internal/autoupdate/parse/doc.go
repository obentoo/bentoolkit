// Package parse extracts versions from upstream content: JSON paths, regular
// expressions and HTML/XPath, version transforms and series selection,
// version-history extraction, and the validation of an extracted version
// against a registry record. It imports registry and ebuilds (story 061).
//
// The headless-browser ("script") parser stays in internal/autoupdate: only the
// checker drives it, and its build-tag-selected backend is pinned there.
package parse
