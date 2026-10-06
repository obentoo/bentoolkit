# Bentoolkit notices

Back to the [README](../README.md).

### Notice Commands

Every notice lives in two places: a GLEP 42 news item in the overlay, read
offline by portage and `eselect news`, and a YAML file in the site repository,
from which the notices feed and pages are built. `bentoo notice` writes both
from one input, with one ID (`<published>-<name>`), so they cannot drift.

```bash
# A security notice; the body is read from a file
bentoo notice new --type security --severity critical \
  --name foo-cve --published 2026-09-28 --title "foo 1.2 heap overflow" \
  --summary "A crafted archive overflows a heap buffer in foo before 1.2.3." \
  --affects 'dev-libs/foo:1 >=1.0,<1.2.3' \
  --body-file body.txt

# Revise it later: the current text opens in your editor
bentoo notice revise 2026-09-28-foo-cve --severity warning
```

- **`--affects`** is `<category>/<package>[:<slot>][ <range>[,<range>...]]`, a
  range being `<`, `<=`, `=`, `>=` or `>` followed by a Gentoo version. Repeat
  the flag for several packages; `security` and `release` notices need at
  least one. The ranges of one entry combine with AND. A news item's
  `Display-If-Installed` headers combine with OR, so an entry with several
  ranges is written there as the bare package, with a warning: the news item
  then targets every installed version, while the feed stays precise.
- **The body** comes from `--body-file`, or from `$VISUAL`, else `$EDITOR`,
  opened on a temporary file. Type it anywhere in that file. The file's
  instructions end with a scissors line,
  `# ------------------------ >8 ------------------------`: above it, lines
  starting with `#` are the instructions and are dropped, and any other text
  is kept; below it, everything is kept as written, `#` lines included, so a
  root prompt such as `# emerge --sync` survives. An empty body aborts without
  writing anything. The editor command is split into words and run directly,
  never through a shell.
- **`notice.site_path`** in the configuration points at the site repository.
  Without it only the news item is written, and the site YAML is printed for
  you to save by hand.
- **`--published`** defaults to today in UTC and cannot be in the future;
  `--author` defaults to the configured git user.
- **`revise`** bumps the news item's `Revision` and sets the site file's
  `updated` to now, keeping the ID and the publication date. `--severity`,
  `--title`, `--summary` and `--affects` replace those fields. When neither the
  text nor a field changed, nothing is written.
- **No git operation.** Both commands print every path they wrote and the
  `git add`/`git commit` to run in each repository; review, commit and push
  are yours.
