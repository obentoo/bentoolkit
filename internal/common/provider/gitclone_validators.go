package provider

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// allowedRepoURLSchemes is the set of URL schemes accepted for a git remote.
// Anything outside this set (e.g. "file", "javascript", "data") is rejected to
// prevent local-file disclosure and pseudo-scheme injection. (R2.1)
var allowedRepoURLSchemes = map[string]bool{
	"http":  true,
	"https": true,
	"git":   true,
	"ssh":   true,
}

// forbiddenBranchRunes is the set of single characters that must never appear
// in a branch name. These are git ref metacharacters and shell-dangerous
// characters; rejecting them keeps us strictly no more permissive than
// "git check-ref-format". (R2.2, AD-9)
const forbiddenBranchRunes = "~^:?*[\\"

// rtlOverride is the Unicode "Right-to-Left Override" code point (U+202E).
// It can be used to visually disguise a branch name, so it is rejected.
const rtlOverride = '‮'

// ValidateRepoURL parses raw with net/url and rejects any repository URL whose
// scheme is outside the allowed set {http, https, git, ssh} or whose host is
// empty. The scheme comparison is case-insensitive.
//
// A URL whose trimmed form begins with "-" is rejected outright (before any
// parsing): git would otherwise treat such a positional argument as an option,
// enabling flag-injection (e.g. "--upload-pack=...").
//
// The scp-like SSH shorthand "user@host:path" (which net/url cannot parse) is
// treated as a valid SSH remote: it has no scheme and no shell metacharacters
// of concern here, and the design explicitly allows the "ssh" transport.
//
// On failure it returns an error wrapping ErrInvalidRepoURL. (R2.1)
func ValidateRepoURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%w: empty URL", ErrInvalidRepoURL)
	}

	if strings.HasPrefix(strings.TrimSpace(raw), "-") {
		return fmt.Errorf("%w: %q has a leading '-' (flag-injection risk)", ErrInvalidRepoURL, raw)
	}

	u, err := url.Parse(raw)
	if err != nil {
		// net/url cannot parse the scp-like SSH shorthand "user@host:path".
		// Accept that specific shape; reject everything else that fails to
		// parse.
		if isSCPLikeSSHURL(raw) {
			return nil
		}
		return fmt.Errorf("%w: %s: %v", ErrInvalidRepoURL, raw, err)
	}

	scheme := strings.ToLower(u.Scheme)

	// A URL with no scheme that still looks like the scp-like SSH shorthand
	// is accepted as an SSH remote.
	if scheme == "" && isSCPLikeSSHURL(raw) {
		return nil
	}

	if !allowedRepoURLSchemes[scheme] {
		return fmt.Errorf("%w: scheme %q is not allowed (allowed: http, https, git, ssh)", ErrInvalidRepoURL, u.Scheme)
	}

	if u.Host == "" {
		return fmt.Errorf("%w: %s: empty host", ErrInvalidRepoURL, raw)
	}

	return nil
}

// isSCPLikeSSHURL reports whether raw uses the scp-like SSH shorthand, i.e.
// "user@host:path" with no "://" scheme separator. Examples:
//
//	git@github.com:org/repo.git
//
// This shape is a valid git SSH remote even though net/url cannot parse it.
func isSCPLikeSSHURL(raw string) bool {
	if strings.Contains(raw, "://") {
		return false
	}
	at := strings.Index(raw, "@")
	colon := strings.Index(raw, ":")
	// Require an "@" before a ":" and a non-empty user, host, and path.
	if at <= 0 || colon <= at+1 || colon == len(raw)-1 {
		return false
	}
	return true
}

// ValidateBranch validates a git branch name against a conservative subset of
// "git check-ref-format". It rejects b when it:
//   - is empty,
//   - contains whitespace,
//   - contains an ASCII control character,
//   - contains the ".." sequence,
//   - contains the "@{" sequence,
//   - has a leading "-" (which enables git flag-injection),
//   - contains any of the metacharacters ~ ^ : ? * [ \,
//   - contains a Unicode right-to-left override (U+202E),
//   - contains a NUL byte,
//   - starts with "/", ends with "/", or contains "//" (empty path component),
//   - ends with ".",
//   - is exactly "@", or
//   - has a "/"-separated component that starts with "." or ends with ".lock".
//
// On failure it returns an error wrapping ErrInvalidBranch. The rule set is
// intentionally never more permissive than git: any branch this function
// accepts also passes "git check-ref-format --branch". (R2.2, AD-9)
func ValidateBranch(b string) error {
	if b == "" {
		return fmt.Errorf("%w: branch name is empty", ErrInvalidBranch)
	}

	if strings.HasPrefix(b, "-") {
		return fmt.Errorf("%w: branch name %q has a leading '-' (flag injection risk)", ErrInvalidBranch, b)
	}

	if strings.Contains(b, "..") {
		return fmt.Errorf("%w: branch name %q contains \"..\"", ErrInvalidBranch, b)
	}

	if strings.Contains(b, "@{") {
		return fmt.Errorf("%w: branch name %q contains \"@{\"", ErrInvalidBranch, b)
	}

	// A bare "@" is reserved by git and is not a valid ref name.
	if b == "@" {
		return fmt.Errorf("%w: branch name cannot be the single character %q", ErrInvalidBranch, b)
	}

	// A ref name cannot begin or end with a "/", nor contain a "//" — each of
	// these introduces an empty path component, which git rejects.
	if strings.HasPrefix(b, "/") {
		return fmt.Errorf("%w: branch name %q has a leading '/'", ErrInvalidBranch, b)
	}
	if strings.HasSuffix(b, "/") {
		return fmt.Errorf("%w: branch name %q has a trailing '/'", ErrInvalidBranch, b)
	}
	if strings.Contains(b, "//") {
		return fmt.Errorf("%w: branch name %q contains \"//\" (empty path component)", ErrInvalidBranch, b)
	}

	// A ref name cannot end with a ".".
	if strings.HasSuffix(b, ".") {
		return fmt.Errorf("%w: branch name %q ends with '.'", ErrInvalidBranch, b)
	}

	// Each "/"-separated component must not start with "." nor end with
	// ".lock"; git reserves both shapes.
	for _, component := range strings.Split(b, "/") {
		if strings.HasPrefix(component, ".") {
			return fmt.Errorf("%w: branch name %q has a component %q starting with '.'", ErrInvalidBranch, b, component)
		}
		if strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("%w: branch name %q has a component %q ending with \".lock\"", ErrInvalidBranch, b, component)
		}
	}

	for _, r := range b {
		switch {
		case r == 0:
			return fmt.Errorf("%w: branch name contains a NUL byte", ErrInvalidBranch)
		case r == rtlOverride:
			return fmt.Errorf("%w: branch name contains a Unicode RTL override (U+202E)", ErrInvalidBranch)
		case unicode.IsControl(r):
			return fmt.Errorf("%w: branch name %q contains a control character", ErrInvalidBranch, b)
		case unicode.IsSpace(r):
			return fmt.Errorf("%w: branch name %q contains whitespace", ErrInvalidBranch, b)
		case strings.ContainsRune(forbiddenBranchRunes, r):
			return fmt.Errorf("%w: branch name %q contains a forbidden character %q", ErrInvalidBranch, b, r)
		}
	}

	return nil
}
