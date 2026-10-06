package main

import (
	"regexp"
	"strings"
	"testing"
)

// Story 068, sub-task 4.1 — R4.1-R4.4: the README states the BENTOO_FETCH_
// rule where each key is documented, shows only prefixed names in examples,
// states what reviewing fetch_url means, lists BENTOO_FETCH_* among the header
// variables never expanded, and the CHANGELOG carries the BREAKING migration.

// s068DocSection returns the lines from heading up to the next heading of the
// same or a higher level, skipping ``` fences: a "# comment" inside a code
// block is not a heading (sectionFrom stops at one, which cuts both the
// Secrets and the record-model sections short).
func s068DocSection(t *testing.T, doc, heading string) string {
	t.Helper()
	level := strings.Count(strings.SplitN(heading, " ", 2)[0], "#")
	var out []string
	in, fenced := false, false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
		}
		if !fenced && strings.HasPrefix(line, "#") {
			hashes := len(line) - len(strings.TrimLeft(line, "#"))
			isHeading := strings.HasPrefix(line[hashes:], " ")
			if in && isHeading && hashes <= level {
				break
			}
			if line == heading {
				in = true
			}
		}
		if in {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		t.Fatalf("heading %q not found", heading)
	}
	return strings.Join(out, "\n")
}

// s068TableRow returns the first table row in section whose first cell is the
// backticked key.
func s068TableRow(section, key string) string {
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "| `"+key+"`") {
			return line
		}
	}
	return ""
}

func s068Paragraphs(section string) []string { return strings.Split(section, "\n\n") }

func TestREADME_DocumentsAuthFetchSecretScope(t *testing.T) {
	readme := readDocSet(t)
	secretsSec := s068DocSection(t, readme, "### Secrets")
	model := s068DocSection(t, readme, "#### The record model")
	headersSec := s068DocSection(t, readme, "### Headers and environment variables")

	t.Run("R4.1: the secrets section states the rule and lists fetch_form_env", func(t *testing.T) {
		if !strings.Contains(secretsSec, "BENTOO_FETCH_") {
			t.Error("the Secrets section does not state the BENTOO_FETCH_ requirement")
		}
		found := false
		for _, line := range strings.Split(secretsSec, "\n") {
			if strings.HasPrefix(line, "|") && strings.Contains(line, "`fetch_form_env`") {
				found = true
			}
		}
		if !found {
			t.Error("the secrets table has no row for `fetch_form_env`")
		}
	})

	t.Run("R4.1: the fetch_serial_env and fetch_form_env rows state the rule", func(t *testing.T) {
		for _, key := range []string{"fetch_serial_env", "fetch_form_env"} {
			row := s068TableRow(model, key)
			if row == "" {
				t.Errorf("the record-model table has no `%s` row", key)
				continue
			}
			if !strings.Contains(row, "BENTOO_FETCH_") {
				t.Errorf("the `%s` row does not state the BENTOO_FETCH_ requirement: %s", key, row)
			}
		}
	})

	t.Run("R4.1: every example names only BENTOO_FETCH_ variables", func(t *testing.T) {
		assign := regexp.MustCompile(`(fetch_serial_env|fetch_form_env)\s*=\s*"([^"]*)"`)
		matches := assign.FindAllStringSubmatch(readme, -1)
		if len(matches) == 0 {
			t.Fatal("README carries no fetch_serial_env / fetch_form_env example at all")
		}
		for _, m := range matches {
			var names []string
			if m[1] == "fetch_serial_env" {
				names = []string{m[2]}
			} else {
				for _, pair := range strings.Split(m[2], "&") {
					if _, v, ok := strings.Cut(pair, "="); ok {
						names = append(names, v)
					}
				}
			}
			for _, n := range names {
				if !strings.HasPrefix(n, "BENTOO_FETCH_") {
					t.Errorf("example %s names %q, which the parser refuses", m[0], n)
				}
			}
		}
		eg := regexp.MustCompile("e\\.g\\. `([A-Za-z0-9_]+)`")
		for _, line := range strings.Split(secretsSec, "\n") {
			if !strings.HasPrefix(line, "|") || !strings.Contains(line, "fetch_") {
				continue
			}
			for _, m := range eg.FindAllStringSubmatch(line, -1) {
				if !strings.HasPrefix(m[1], "BENTOO_FETCH_") {
					t.Errorf("secrets-table example %q is not a BENTOO_FETCH_ name: %s", m[1], line)
				}
			}
		}
	})

	t.Run("R4.2: reviewing these keys means checking fetch_url", func(t *testing.T) {
		for _, p := range s068Paragraphs(model) {
			if strings.Contains(strings.ToLower(p), "review") && strings.Contains(p, "BENTOO_FETCH_") &&
				strings.Contains(p, "`fetch_url`") && strings.Contains(p, "`fetch_serial_env`") &&
				strings.Contains(p, "`fetch_form_env`") {
				return
			}
		}
		t.Error("no record-model paragraph says that a record sends its BENTOO_FETCH_* values to its own `fetch_url`, so reviewing a change to `fetch_url`, `fetch_serial_env` or `fetch_form_env` means checking that url")
	})

	t.Run("R4.3: BENTOO_FETCH_* is among the never-expanded header variables", func(t *testing.T) {
		for _, p := range s068Paragraphs(headersSec) {
			if strings.Contains(p, "BENTOO_NTFY_TOKEN") && strings.Contains(p, "BENTOO_SMTP_PASSWORD") {
				if !strings.Contains(p, "BENTOO_FETCH_") {
					t.Errorf("the never-expanded list does not include BENTOO_FETCH_*: %q", p)
				}
				return
			}
		}
		t.Error("the Headers section has no paragraph listing the never-expanded variables")
	})

	t.Run("R4.4: CHANGELOG [Unreleased] ### Security carries the BREAKING migration", func(t *testing.T) {
		changelog := readRepoDoc(t, "CHANGELOG.md")
		security := s068DocSection(t, shippingSection(changelog, "0.32.0"), "### Security")
		for _, bullet := range strings.Split(security, "\n- ") {
			lower := strings.ToLower(bullet)
			if strings.Contains(bullet, "BREAKING") && strings.Contains(bullet, "BENTOO_FETCH_") &&
				strings.Contains(bullet, "`fetch_serial_env`") && strings.Contains(bullet, "`fetch_form_env`") &&
				strings.Contains(lower, "rename") && strings.Contains(lower, "header") {
				return
			}
		}
		t.Error("no [0.32.0] ### Security bullet marks the BENTOO_FETCH_ refusal (`fetch_serial_env`, `fetch_form_env`) and the header reservation as BREAKING with the rename migration")
	})
}
