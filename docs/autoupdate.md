# Bentoolkit autoupdate

Back to the [README](../README.md).

### Autoupdate System

The autoupdate system automates version tracking by fetching upstream sources and comparing them against the overlay's current versions.

#### Schema Configuration (`packages.toml`)

Place a `packages.toml` file in the root of your overlay. Each entry — a
*record* — defines how to extract the version for a package:

```toml
["app-misc/hello"]
url = "https://api.github.com/repos/owner/hello/releases/latest"
parser = "json"
path = "tag_name"
comments = """
hello — GitHub release tag "vX.Y.Z" (the "v" is stripped before comparison).
"""
# END

["dev-libs/mylib"]
url = "https://example.com/releases"
parser = "regex"
pattern = 'mylib-([0-9.]+)\.tar\.gz'
comments = """
mylib — the download index; the pattern is anchored on the tarball name so a
changelog mention of an older version cannot win the first match.
"""
# END

["app-text/myapp"]
url = "https://example.com/downloads"
parser = "html"
selector = "a.release-tag"
comments = """
myapp — the download page's release badge; there is no JSON endpoint.
"""
# END
```

#### The record model

Two conventions hold the file together as it grows past a few hundred entries.
Both are checked by `bentoo overlay autoupdate --lint`.

**Every record ends with a `# END` line.** TOML has no block delimiter, and a
bare `[END]` table would not be one — it would parse as a package named `END`,
and repeated once per record, as a duplicate-table error that stops the whole
file from loading. A comment on the record's last line is the closest valid
equivalent, and it makes the boundary between two records explicit.

**Documentation lives in the `comments` field, never in a floating `#` line.**
This is not only tidiness. A comment sitting between two records belongs to
neither, so nothing says which one it describes; and comments do not survive a
rewrite — `bentoo overlay analyze --save` re-encodes the whole registry, which
used to erase every doc comment in it. As a field the text is data: it has an
owner, and it comes back out.

Write it as a TOML multi-line string starting with the package name, as the last
field of the record, and keep `[` off the start of any line inside it (the
raw-text editors that flip `enabled` scan for `[section]` headers and would read
such a line as one).

The one exception is the **file header**: the comment block before the first
record. It documents the model itself rather than any single package, so it can
live inside no record, and `--lint` leaves it alone. The exemption ends at the
first `[section]` header — a comment after that, between records or trailing the
last one, is reported as before.

##### Field order

Fields run bookkeeping → source → extraction → post-processing → transport →
classification → auxiliary substitution → doc. Omit what you do not need; never
invent a key — `PackageConfig` in
[`internal/autoupdate/registry/config.go`](../internal/autoupdate/registry/config.go) is the sole
authority on what parses, and a key it does not declare **fails the load**,
naming the record and the key. That is deliberate: `serie` instead of `series`
used to disable the release-line filter silently, which is exactly the failure
`series` exists to prevent.

The order below is not a style preference — it is the practice measured across
the overlay's records, encoded as `CanonicalFieldOrder` in
[`internal/autoupdate/registry/lint.go`](../internal/autoupdate/registry/lint.go). `--lint` reports a
record that deviates and `--lint --fix` reorders it, so this block and the
linter cannot disagree.

```toml
["category/package"]                # header: quoted, exactly as in the overlay; printable characters only
enabled = false                     # ONLY when false. Absent = enabled.
hold = true                         # ONLY when true. See "enabled vs hold".
track = "commit"                    # omit for tag/version tracking
url = "https://…"                   # REQUIRED — the endpoint being probed
mirrors = ["https://…"]             # same content as url, tried in order when it fails
parser = "json"                     # REQUIRED — json | regex | html | script
path = "tag_name"                   # REQUIRED for parser=json
pattern = 'name-([0-9.]+)\.tar\.xz' # REQUIRED for parser=regex (1 capture group)
selector = "a.release-tag"          # REQUIRED for parser=html (or xpath)
script = "@vendor.js"               # REQUIRED for parser=script (scripts/vendor.js)
transform = [['^v', ""]]            # ordered regex substitutions on the result
select = "max"                      # first (default) | max | last
suffix = "_pre"                     # pre-release channel marker
suffix_when = '^26\.8\.'            # …applied only to a matching version
commit_sha_path = "[0].sha"         # REQUIRED with track="commit"
commit_message_path = "commit.message"
commit_version_pattern = 'sdk-([0-9.]+)'
base_from = "file"                  # where the base lives: file | tag | commit_message | none
base_url = "https://raw.…/VERSION"  # REQUIRED with base_from="file"
base_pattern = '^([0-9][0-9.]*)-devel'  # …1 capture group, the base version
base_tag_pattern = 'vulkan-sdk-([0-9.]+)'  # REQUIRED with base_from="tag"
headers = { "User-Agent" = "bentoo-autoupdate" }
timeout = 60                        # seconds, only for reliably slow hosts
meta = { fetch_url = "https://…" }  # authenticated fetch; NEVER a secret
type = "bin"                        # ONLY to override the -bin/RESTRICT heuristic
series = '^1\.28\.'                 # REQUIRED when the dir holds two release lines
aux_var = "MY_BUILD"                # free-text ebuild var kept in sync…
aux_pattern = 'esr-bb([0-9]+)'      # …always paired with aux_var
aux_url = "https://…/{version}/x"   # …read aux_pattern here instead of url
requires = { "dev-lang/dart" = { pattern = '…"{version}"…"([^"]+)"', pin = "~" } }  # pins moved with the bump
comments = """…"""                  # REQUIRED — the doc, always last
# END
```

There is no `binary` key. It was retired: nothing ever read it, and `type`
classifies. `--lint --fix` migrates a record still carrying it.

`meta` is **not** documentation-only, whatever an older comment may have said.
The applier reads a typed `fetch_*` sub-schema out of it for gated downloads,
and `--lint` validates it, because a typo in `fetch_serial_env` used to disable
the download without a word. The rule about secrets stands: reference an env
var, never the value.

A record may only send variables named `BENTOO_FETCH_*`: `fetch_serial_env` and
every variable in `fetch_form_env` must begin with that prefix and use only
letters, digits and underscore, or the record is refused before anything is
resolved — by the sweep, by `bentoo distfile` and
by `--lint`, with the same message. The rule reads the name only, so the error
is the same whether the secret exists or not. It is a namespace, not a binding:
a record sends its `BENTOO_FETCH_*` values to its own `fetch_url`, and any record
may name any `BENTOO_FETCH_*` variable. So when you review a `packages.toml`
change that sets or changes `fetch_url`, `fetch_serial_env` or `fetch_form_env`,
check that the `fetch_url` is a host you trust with those values.

| Key | Required | Meaning |
|---|---|---|
| `fetch_url` | **yes** — it is the trigger | The form action / endpoint. Without it the whole block is inert, which is why `--lint` refuses a `fetch_*` key beside a missing or blank one. Must be an absolute `http(s)://` URL with a fixed host. May carry `{id}` (see the id lookup below) — in the path or query only, so an upstream value can never choose the host |
| `fetch_filename` | **yes** | Destination name, `{version}` substituted. Must equal the basename the ebuild's `SRC_URI` expects, or the Manifest will not match |
| `fetch_method` | no — defaults to `post` | `post` or `get` |
| `fetch_body` | no — defaults to `form` | `form` (urlencoded) or `json`. In `json` the values of `fetch_form` become a JSON object: exactly the literals `true` and `false` become booleans, **everything else stays a string** (so a postcode is not silently turned into a number) |
| `fetch_form` | no | The other fields, always written urlencoded (`platform=linux&submit=Go`) whichever encoding is sent. A repeated key is refused under `fetch_body = "json"`, since a JSON object holds each key once |
| `fetch_response` | no — defaults to `file` | `file` (the reply IS the distfile) or `url` (the reply is the address to download from) |
| `fetch_content_type` | no | Content type the **finished** file must carry, e.g. `application/zip` |
| `fetch_min_bytes` | no | Smallest believable size for the finished file. Checked on the bytes actually written, so a truncated transfer fails too |
| `fetch_serial_env` | no — **but only together with** `fetch_serial_field` | Name of the env var holding the serial. Must begin with `BENTOO_FETCH_` and use only letters, digits and underscore; an unprefixed name refuses the record, naming the `BENTOO_FETCH_` name to rename it to, and any other character refuses it as not a variable name |
| `fetch_serial_field` | no — **but only together with** `fetch_serial_env` | Form field the serial is submitted in |
| `fetch_id_url` | no — **but only together with** `fetch_id_pattern` | Where the vendor publishes the per-release download id (`{version}` substituted). Same rule as `fetch_url`: absolute `http(s)://`, `{version}` in the path or query only |
| `fetch_id_pattern` | no — **but only together with** `fetch_id_url` | Regex over that body with **1 capture group**, the id. `{version}` is substituted **quoted**, so `21.1` matches `21.1` and not `2101` |
| `fetch_form_env` | no | Form fields whose VALUES come from the [secrets](configuration.md#secrets) chain, written `field=VARIABLE_NAME` and urlencoded like `fetch_form`. Refused with `fetch_method = "get"`, and refused for a field `fetch_form` or `fetch_serial_field` already claims. Every variable must begin with `BENTOO_FETCH_` and use only letters, digits and underscore; the refusal lists each other name with its field |
| `fetch_timeout` | no — defaults to 300 | Seconds for the whole download |

##### When the form asks for a person, not a credential

A vendor may gate the download behind a *registration* form: name, e-mail,
telephone, address. None of that can go in `fetch_form` — `packages.toml` lives
in the overlay, and the overlay is public. `fetch_form_env` states the field
names and, for each, the **name of the variable** holding the value:

```toml
fetch_form_env = "firstname=BENTOO_FETCH_BMD_FIRSTNAME&lastname=BENTOO_FETCH_BMD_LASTNAME&email=BENTOO_FETCH_BMD_EMAIL&phone=BENTOO_FETCH_BMD_PHONE&street=BENTOO_FETCH_BMD_STREET&city=BENTOO_FETCH_BMD_CITY&state=BENTOO_FETCH_BMD_STATE&zip=BENTOO_FETCH_BMD_ZIP"
```

Each value is resolved at fetch time through the same chain as the serial —
environment variable, then `~/.config/bentoo/secrets`, then `/etc/bentoo/secrets`
— so every operator sends their own details and the record carries none. All of
them are resolved **before** the first request goes out, so a missing variable
is reported naming both the variable and the field it belongs to, rather than
halfway through a submission the vendor has already recorded.

Resolved values are removed from any error text the download produces, the
serial always and the rest once they are at least four characters long: a
two-character value identifies nobody on its own, and substituting it would
blank out fragments of the very message you need.

The combination with `fetch_method = "get"` is **refused**, not discouraged: a
GET puts every field in the query string, where the vendor's access log, every
proxy in between and your own shell history all keep it.

##### When the reply is a URL and not the file

Some endpoints answer the form with a short `text/plain` body holding a signed
CDN address, not with the archive. Written to disk as-is that becomes a
few-hundred-byte "`.zip`" that `pkgdev` digests without complaint — **a green
Manifest for a file that is a sentence**. `fetch_response = "url"` is what turns
that reply into a second request for the real file.

Independently of the setting, the finished response is guarded: a textual
content type (`text/*`, `application/json`, `application/xml`) is never a
distfile and is refused, naming `fetch_response` as the fix. Signed URLs are
short-lived — three hours on the endpoint this was built against — so the two
legs always run back to back; a refusal from the CDN says so rather than
blaming the serial.

##### When the endpoint id changes every release

A vendor that mints a fresh download id per release makes a hardcoded
`fetch_url` correct until the next bump, and then quietly serves the *previous*
version's installer under the new version's name. `fetch_id_url` +
`fetch_id_pattern` resolve the id at fetch time and substitute it into `{id}`:

```toml
# One line: TOML inline tables do not wrap, and `--lint` reads this record model.
meta = { fetch_url = "https://vendor.example/api/register/us/download/{id}", fetch_body = "json", fetch_response = "url", fetch_content_type = "application/zip", fetch_min_bytes = "1000000000", fetch_filename = "Example_{version}_Linux.zip", fetch_form = "product=Example&platform=Linux&policy=true", fetch_id_url = "https://vendor.example/api/support/us/downloads.json", fetch_id_pattern = '"Linux":\[\{"downloadId":"([^"]+)","downloadTitle":"Example {version}"' }
```

Read it in order: look the id up in the catalogue, `POST` the form as JSON to the
endpoint that id names, treat the reply as the URL of the real file, and refuse
anything that comes back which is not a zip of at least a gigabyte.

A redirect is also refused on the form leg, and deliberately: a `301`/`302`/`303`
makes every HTTP client reissue the `POST` as a `GET` **and drop the body**, so
the vendor sees a request carrying none of the form. The old error blamed the
serial for that, which was wrong in both fact and remedy; `307`/`308` preserve
the body and are followed normally.

The serial pair is optional because not every gated download is gated by a
*credential*: a vendor may hand the file to whoever submits the form. Declaring
**one half alone is an error**, and deliberately so — it describes a request
that cannot be built (an env var with no field to carry it, or a field with no
value to put in it), and letting it pass would submit the form without the
credential it was configured to carry. A gated endpoint answers that with its
login page, which is a 200-response body, not an error.

A record carrying these keys is also what makes
[`bentoo distfile fetch`](distfiles.md#fetch-a-gated-distfile) work for the *user* of the
package, not just for the maintainer's sweep.

##### Rules that are not obvious from the field list

- **Regex values** (`pattern`, `aux_pattern`, `commit_version_pattern`,
  `base_pattern`, and the left side of every `transform` rule) use TOML
  **literal** strings `'…'`. A basic string `"…"` rejects `\.` and `\d`
  outright. Replacements use basic strings.
- **Where the base version comes from** (`track = "commit"` only). The
  `_p<date>`/`_pre<date>` suffix is derived from the current ebuild, but the
  `X.Y.Z` in front of it needs a source, and `base_from` names it:
  - `"file"` — fetch `base_url`, apply `base_pattern`. **Prefer this.** One
    request, no window, no pagination; use it whenever upstream versions itself
    in-tree (`crates/zed/Cargo.toml`, mesa's `VERSION`, `meson.build`,
    `CMakeLists.txt`). Go anchors `^`/`$` to the whole body, so a version
    declared mid-file needs `(?m)`.
  - `"tag"` — fetch a tag listing (`…/git/refs/tags` on GitHub,
    `…/repository/tags` on GitLab) and take the highest version matching
    `base_tag_pattern`. Use it when the scheme **the ebuild uses** exists only
    as tags: glslang and spirv-\* version themselves `2026.3`/`1.5.5` in-tree
    while the overlay tracks `vulkan-sdk-X.Y.Z.W`. Always anchor the pattern to
    one tag family — these repos carry four or more at once, and an unfiltered
    ranking picks `khronos-master-20141209` for vulkan-loader.
  - `"commit_message"` — the older scan via `commit_version_pattern`. Correct
    only when upstream announces releases in commit titles **and** commits
    slowly enough that the bump stays inside the fetch window. That window is
    measured in commits, not days: `per_page=50` covers ten months of
    Vulkan-Headers but 1.3 days of zed.
  - `"none"` — the upstream publishes no version at all: no usable tag, nothing
    in-tree, nothing in the commit titles. The base is a constant you chose
    (conventionally `0`) and only the snapshot suffix moves. It resolves nothing
    at check time, so `base_url`, `base_pattern`, `base_tag_pattern` and
    `commit_version_pattern` must all be absent — declaring one alongside it is
    a contradiction, not dead weight.

    Say it out loud rather than leaving `base_from` off. The two read
    identically to the checker but not to a human, and `--lint` cannot tell
    "nobody declared the source" from "there is no source to declare" unless the
    second says so: `sci-ml/ik_llama-cpp` (one tag, `t0002`, a prerelease a year
    behind an active HEAD) and `sys-apps/asus-ec-sensors` (one stale `v0.1.0`,
    board support landing as plain commits) were the only two records the rule
    reported across 411, and both were right all along.
  - Absent — the legacy form of `"none"`, kept working for registries written
    before the field existed. It behaves identically; it just cannot say whether
    that was the intent, which is why `--lint` reports it.

  A declared source that resolves nothing is now a **check failure**, not a
  fallback. Six of the seven entries that carried a `commit_version_pattern`
  matched nothing — the pattern had been copied to sibling repos that never
  write it — and their bases froze up to seven releases behind while the
  `_p<date>` kept advancing, so the versions looked alive and were not.
  Pick the source that matches the scheme **the ebuild** uses: spirv-tools
  publishes `v2026.3` in `CHANGES`, but the overlay versions it on the
  `vulkan-sdk` scheme, so that file is the wrong source even though it parses.
- **`enabled` vs `hold`.** `enabled` is *bookkeeping the checker flips on its
  own*: it writes `enabled = false` when the ebuild vanishes from the overlay,
  and **deletes that line** when it reappears. The deletion is the point —
  enabled is the default, spelled by the key's absence, so writing
  `enabled = true` would state nothing the file did not already say and `--lint`
  reports it as redundant. `hold` is a *maintainer decision* ("present, but
  never auto-bump") that reconciliation never touches. A package needing manual
  work each release (patchset, pinned SHA, bootstrap compiler) takes `hold` —
  `enabled = false` would be silently reverted. Both skip the fetch entirely.
- **User-Agent** is required by `api.github.com` and `crates.io` — always the
  literal `"bentoo-autoupdate"`. A browser UA is a last resort for a
  Cloudflare-fronted host, and the reason belongs in `comments`.
- **Regex returns capture group 1 of the FIRST match** on the raw body; anchor
  it so a page listing several releases cannot yield an older one, or use
  `select = "max"`.
- **Verify before committing.** Probe the real endpoint with
  `bentoo overlay autoupdate --check <category/package> --force`; never
  hand-write a record from a guessed URL shape.

#### Several release lines of one package (`series`)

An overlay routinely carries more than one ebuild per package, and **one entry
cannot track them all**: the scan takes the directory's highest version, so the
other lines are never bumped.

The `:slot` key suffix already covers the case where the lines are separate
SLOTs — see [Multi-slot packages](#multi-slot-packages). `series` covers the
other half: lines that **share a SLOT** and differ by version. `libreoffice`
keeps the stable 26.2 series beside the testing 26.8 one, both `SLOT=0`;
`zed-bin` keeps 1.13.1 stable beside 1.14.1_pre.

What the absence of it costs is worth stating plainly. With `zed-bin-1.13.1` and
`zed-bin-1.14.1_pre` both present and one entry tracking the stable channel, the
scan returns `1.14.1_pre` as "current" — so every stable release below `1.14.1`
compares *older* and reports "up to date". The stable line stops being updated,
and the silence looks like success.

Give each line its own entry, distinguished by an `@label` in the key, and let
`series` say which versions belong to it:

```toml
["app-office/libreoffice@stable"]
url = "https://downloadarchive.documentfoundation.org/libreoffice/old/"
parser = "regex"
pattern = 'href="([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)/"'
select = "max"
series = '^26\.2\.'
comments = """…"""
# END

["app-office/libreoffice@testing"]
url = "https://downloadarchive.documentfoundation.org/libreoffice/old/"
parser = "regex"
pattern = 'href="([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)/"'
select = "max"
series = '^26\.8\.'
suffix = "_pre"
comments = """…"""
# END
```

`series` narrows **both ends** of the comparison: which ebuild counts as the
entry's current version, and which upstream candidates survive selection. An
extraction path that yields a single value (first match, `script`, fallback,
LLM) *fails* when the version falls outside the series, rather than comparing it
against an ebuild the entry does not track.

The `@label` is identity only — it makes the key unique and never reaches a
filesystem path or a `SLOT=` lookup. `@` rather than `:` because `:` already
means SLOT; both may appear (`net-libs/webkit-gtk:4.1@lts`). Two entries for one
package must differ by slot **or** series: a label alone filters nothing, and
`--lint` rejects it.

Note that with `series` the `suffix_when` below becomes unnecessary — the series
already delimits the line, so a plain `suffix = "_pre"` says the rest.

#### Pre-release channels (`suffix`)

Upstream numbering rarely says a release is a pre-release. LibreOffice publishes
`26.8.0.1` in its **testing** channel with a version string indistinguishable
from a stable one, so the bare value lands in the overlay as if it were a
finished release — and a bump silently drops the `_pre` the ebuild carried.

`suffix` declares the truth, and with it the ordering. Gentoo sorts `_pre` below
the bare version, so `26.8.0.1_pre` stays *older* than the eventual `26.8.0.1`
and the bump fires exactly when upstream promotes the release:

```toml
["app-office/libreoffice"]
url = "https://downloadarchive.documentfoundation.org/libreoffice/old/"
parser = "regex"
pattern = 'href="([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)/"'
select = "max"
suffix = "_pre"
suffix_when = '^26\.8\.'
comments = """
libreoffice — old/ lists the stable 26.2 line and the testing 26.8 one in one
index, and select=max always returns the latter, so suffix_when marks that line
(and only it) as _pre. The ebuild strips it back out for SRC_URI via
MY_PV="${MY_PV/_pre/}". When 26.8 is promoted to stable, drop suffix_when or
point it at the next development line.
"""
# END
```

`suffix_when` is what makes one endpoint serving several release lines
workable. Omit it when the probed URL **is** the pre-release channel: then every
version it yields is a pre-release and the suffix applies unconditionally.

The suffix is applied after `transform` and before comparison, so `select = "max"`
orders the values that will actually become the PV. It is idempotent — a version
upstream already marked (`2.0.0_rc1`) is left alone — and it cannot be combined
with `track = "commit"`, whose `_p<date>` snapshot suffix comes from the current
ebuild instead.

Valid values are the Gentoo suffixes, optionally numbered: `_alpha`, `_beta`,
`_pre`, `_rc`, `_p`. The ebuild must be able to strip the suffix when building
`SRC_URI`, since upstream's filenames do not carry it.

**Supported parsers:**

| Parser | Required fields | Description |
|--------|----------------|-------------|
| `json` | `path` | JSON path to the version field (e.g. `tag_name`, `data.version`, `[0].name`): field names joined by `.`, array indexes as `[N]` with plain digits. A malformed path (`a..b`, `a[0]b`, `a[+1]`, a stray `]`) is refused when the registry loads and reported by `--lint`; the same rule applies to `commit_sha_path` and `versions_path`, where a leading `[*]` takes every item of an array |
| `regex` | `pattern` | Regex with one capture group matching the version |
| `html` | `selector` or `xpath` | CSS selector or XPath to the element containing the version |
| `script` | `script` | JavaScript evaluated against the rendered page (inline, or `@file.js` from `.autoupdate/scripts/`); its string result is the version. Needs a binary built with `-tags chromedp` and a Chrome or Chromium executable on `PATH` |

> **Regex parser caveat:** `regex` returns the **first** match in the response
> body, not the highest version. On a page that lists several releases (e.g. a
> directory listing), an unanchored pattern can capture an *older* version and
> cause the check to silently report "up to date". Prefer a JSON API endpoint
> that exposes the latest version directly, or anchor the pattern tightly to the
> single element that always holds the newest release.

> **Non-comparable versions:** before comparing, the extracted value is
> normalized (whitespace trimmed, a leading `v`/`version-`/etc. prefix
> stripped). If the result is still not a well-formed Gentoo-style version
> (e.g. an upstream tag like `INKSCAPE_1_4_4`, or `latest`), the check reports a
> **warning** and skips the package instead of treating it as "up to date" —
> this prevents a bad parser config from silently masking a real update. Fix the
> schema so it extracts a bare version string (e.g. add a `regex` that captures
> the digits, or point `path` at a cleaner field).

**Optional fields:**

| Field | Description |
|-------|-------------|
| `mirrors` | List of URLs serving the same content as `url`, tried in order when the one before fails and before `fallback_url`. Each is probed with the whole record (parser, `script`, `series`, `select`…) with `url` swapped. Credential headers are never sent to a mirror. A mirror that repeats `url` or an earlier mirror is refused, comparing scheme and host case-insensitively and ignoring one trailing `/`. Only the version fetch uses them: `base_url`, `commit_sha_path`, `aux_pattern` and `track = "commit"` still read `url`. A failure counts as a network failure (no registry repair offered) only when every source failed in transport. |
| `requires` | Packages this record pins at a version upstream publishes beside its own, as one inline table keyed by `category/package` (no version, slot or label): `requires = { "dev-lang/dart" = { pattern = '"version": "{version}",\s+"dart_sdk_version": "([^"]+)"', pin = "~" } }`. `pattern` has exactly one capture group; `{version}` is replaced by the detected version, which anchors the capture to the same release object. An optional `url` (with `{version}` only in the path or query; no credential header is sent) is read instead of the record's own. `pin` is `~`, `=` or `>=`. `--check` records the captured version and reports it as `present`, `pending` or `missing` (`waits for …`). `--apply` rewrites every atom of that package carrying that operator in the new ebuild — other operators and comments are left alone, and an ebuild with no such atom fails the bump — and waits, keeping the pending entry, while neither the overlay nor ::gentoo (`BENTOO_GENTOO_REPO`) holds the version. `--apply all` applies a required pending bump before the bump that needs it. |
| `aux_url` | Where `aux_pattern` reads `aux_var`'s value when it is not on the version page (a `latest.txt`, the release's `Cargo.lock` or `package.json`, a tag's commit). `{version}` is replaced by the detected upstream version and may appear only in the path or query. Credential headers are not sent to it. Requires `aux_var` and `aux_pattern`. |
| `fallback_url` | Secondary URL to try if the primary fails. It keeps the record's `timeout`, `series`, `suffix`, `suffix_when` and non-credential headers |
| `fallback_parser` | Parser type for the fallback URL |
| `fallback_pattern` | Pattern/path for the fallback parser |
| `llm_prompt` | Instruction used to extract the version via an LLM. Consumed by `bentoo overlay analyze`, and by `bentoo overlay autoupdate --check` when an `llm.provider` is configured (the LLM is tried after the primary/fallback parsers). When no provider is configured, `--check` logs a Warn and skips LLM extraction. |
| `headers` | Custom HTTP headers. `${VAR}` is expanded only for allow-listed auth headers and allow-listed variables, and each credential is sent only to the hosts it is bound to (a mismatch fails that package's check) — see [Headers and environment variables](behaviour.md#headers-and-environment-variables). Example: `Authorization = "Bearer ${BENTOO_MY_TOKEN}"` |
| `timeout` | Per-operation budget (seconds) for **this** package — the total time spent fetching its version across all retry attempts. Use it for a reliably slow host so it gets extra retry headroom without slowing the whole batch. Absent/`0` uses the global budget derived from `autoupdate.http_timeout`. See [Timeouts](behaviour.md#timeouts). |
| `type` | `"bin"` for a binary package (manifest-only testing), `"source"` for a source-built one. Only to **override** the auto-detection, which already reads the ebuild (`RESTRICT="bindist"`, a `-bin` suffix, a binary `SRC_URI`). Replaces the retired `binary` key; `--lint --fix` migrates a record still carrying it. |
| `series` | Regex restricting the entry to one release line — which ebuild counts as current, and which upstream candidates are eligible. For a package whose parallel ebuilds share a SLOT; see [Several release lines](#several-release-lines-of-one-package-series). |
| `suffix` | Gentoo pre-release suffix (`_alpha`, `_beta`, `_pre`, `_rc`, `_p`, each optionally numbered) appended to the detected version — see [Pre-release channels](#pre-release-channels-suffix). |
| `suffix_when` | Regex gating `suffix`: the suffix is appended only to a version matching it. Omit when the probed URL *is* the pre-release channel. |
| `comments` | The record's documentation, as its last field — see [The record model](#the-record-model). |
| `revision` | The `-rN` suffix to write on a freshly bumped ebuild. Only for multi-slot packages that use the revision to tell their SLOTs apart — see [Multi-slot packages](#multi-slot-packages). Absent/`0` writes a plain PV, which is what an ordinary package wants. |

#### Multi-slot packages

Some packages ship several SLOTs out of one directory, distinguished by the
revision suffix rather than by the version. `net-libs/webkit-gtk` is the
canonical case: `-r410`/`-r411` are SLOT `4.1` and `-r600`/`-r601` are SLOT `6`,
all sharing the same PV series.

A single entry cannot express that. Taking the directory's highest version picks
whichever slot happens to be ahead, so one slot is bumped forever and the other
never is — and naming the destination from the bare upstream PV aims it at the
*other* slot's filename.

Give each slot its own entry by suffixing the key with `:slot`, and declare the
slot's base revision:

```toml
["net-libs/webkit-gtk:4.1"]
url = "https://www.webkitgtk.org/releases/"
parser = "regex"
pattern = 'webkitgtk-(2\.52\.[0-9]+)\.tar\.xz'
select = "max"
revision = 410

["net-libs/webkit-gtk:6"]
url = "https://www.webkitgtk.org/releases/"
parser = "regex"
pattern = 'webkitgtk-(2\.52\.[0-9]+)\.tar\.xz'
select = "max"
revision = 600
```

Each entry then resolves its current version by reading every ebuild's `SLOT=`
and considering only its own slot, keeps its own pending and cache records, and
writes its bump as `<pv>-r<revision>`.

Two things to know:

- **`revision` is the slot's base, not the source ebuild's.** Bumping
  `webkit-gtk-2.52.3-r411` yields `webkit-gtk-2.52.5-r410`, matching ::gentoo:
  `r411` was a revbump *within* the old PV, and a PV change resets it. Set
  `revision` to the value the slot's first ebuild of a new version carries.
- **Omit `revision` for a slot whose ebuilds carry no suffix.** If your overlay's
  SLOT 6 ebuild is `webkit-gtk-2.52.5.ebuild` rather than `-r600`, leave the
  field out for that entry so the plain PV is written.

The `:slot` suffix is part of the entry's identity only — it never appears in a
filesystem path. If a bump would ever land on an ebuild that already exists, the
apply fails with `destination ebuild already exists` and touches nothing.

#### Supported LLM Providers

The `analyze` command uses an LLM for schema generation. `bentoo overlay autoupdate --check` also uses the LLM to extract a version when an `llm.provider` is configured and a package sets `llm_prompt` (tried after the primary and fallback parsers); with no provider configured it logs a Warn and skips LLM extraction.

| Provider | Config value | API key env var | Notes |
|----------|-------------|-----------------|-------|
| Anthropic Claude (HTTP API) | `claude` | `ANTHROPIC_API_KEY` | Default model: `claude-haiku-4-5` |
| Claude Code (local CLI) | `claude-code` | `ANTHROPIC_API_KEY` (bare mode) | Drives the local `claude` CLI headlessly. Default model: `sonnet` alias. Hybrid auth via `llm.bare`; honors `llm.max_budget_usd`. Degrades to a Warn + fallback when the CLI is missing or unauthenticated. |
| OpenAI | `openai` | `OPENAI_API_KEY` | Default model: `gpt-4o-mini` |
| Ollama (local) | `ollama` | *(none)* | Default model: `llama3`, runs locally |

Configure in `~/.config/bentoo/config.yaml`:

```yaml
llm:
  provider: claude
  api_key_env: ANTHROPIC_API_KEY
  model: claude-haiku-4-5
```

The Claude endpoint can be overridden via `CLAUDE_API_ENDPOINT` environment variable (useful for testing or proxies).

##### `claude-code` provider (local CLI)

The `claude-code` provider drives your locally-installed `claude` CLI (Claude Code) headlessly instead of calling the HTTP API, reusing your existing Claude Code login or an API key:

```yaml
llm:
  provider: claude-code
  api_key_env: ANTHROPIC_API_KEY   # used in bare mode
  model: sonnet                    # optional; defaults to the `sonnet` alias (latest Sonnet)
  bare: auto                       # auto | true | false
  max_budget_usd: 0.50             # optional per-call spend cap
```

Authentication is hybrid, selected by `llm.bare`:

- `auto` (default): resolve `api_key_env` **once** through the secrets chain (env → user file → system file); if that yields a non-empty key, run `claude --bare` with it, otherwise use the CLI's logged-in session (subscription). The single resolved value drives both the bare-mode choice and the credential handed to the child `claude`.
- `true` / `false`: force bare (`--bare` + key) or login/subscription mode respectively, regardless of key presence.

> **Cost note.** `sonnet` in login/subscription mode is billed per call (a large page context of ~74k tokens is roughly $0.09+/call). The cheap path is `--bare` + an API key. Set a conservative `max_budget_usd` when running `--check` across many packages. If the `claude` CLI is missing or not authenticated, both `analyze` and `--check` log a Warn and fall back (heuristic schema / skip extraction) — they never fail because of the LLM.

#### Files named for the old version

A bump renames the ebuild and nothing in `files/`. When the ebuild builds a
`${FILESDIR}` path from a version variable (`${P}`, `${PV}`, `${PF}`, `${MY_P}`,
`${MY_PV}`) and `files/` holds a file named for the version being left behind,
`--check` and `--apply` print a stage line naming the file and the name the new
ebuild will look for. It does not rename: the previous ebuild may still use
the old name, and a copy the new ebuild never reads is litter. Like the
::gentoo advisory, it never blocks the bump.

#### The md5-cache after a bump

When the overlay keeps `metadata/md5-cache`, a successful `--apply` runs
`egencache --update` for the bumped package against the checkout (through
`--repositories-configuration`, never the synced copy Portage reads). The new
version gets its entry, and versions whose ebuild is gone, `--clean` included,
lose theirs. It needs `egencache` and `::gentoo` at `BENTOO_GENTOO_REPO` (default
`/var/db/repos/gentoo`). A failure is printed on a `Cache:` line and never
undoes the bump; that includes a missing entry after `egencache` exited 0,
which it does when it fails on a version.

#### When ::gentoo ships the version being bumped

A bump copies the overlay's own ebuild to the new version; it never reads
::gentoo. So when ::gentoo ships the exact version being left behind and its
copy differs from ours, `--check` and `--apply` print a stage line naming both
files, for example:

```text
::gentoo also ships 26.3.0 and its copy differs — the bump carried OUR 26.3.0 forward without re-reading it; diff /var/db/repos/gentoo/media-libs/mesa/mesa-26.3.0.ebuild …
```

It is advisory: it never blocks or changes the bump. It stays silent when
::gentoo does not ship that version (most of the overlay is ahead by design) and
when the two copies are identical. The tree is read from `/var/db/repos/gentoo`;
set `BENTOO_GENTOO_REPO` to another path, or to an empty value to switch the
check off.

#### What `--apply` prints

On a terminal, `--apply` draws a live region;
[Live output](behaviour.md#live-output) says when it does and how to turn it
off. The region keeps to the terminal:

- Every row is cut to the terminal width minus one column and ends in `…` when
  cut. The region is redrawn when the terminal is resized, so it never wraps.
- When the region would be taller than the terminal minus one row, the oldest
  finished packages fold into one line, `… N earlier package(s) finished`;
  if that is not enough, the running packages' output tails lose their oldest
  lines. The header and the row of each running package are never dropped.
  Log lines bentoo routes into the region fold the same way, oldest first,
  into `… N earlier log line(s)`, after the finished packages and before any
  tail is cut; the run summary and a confirmation prompt are never dropped.
- A failed package's `✗` row shows only the first line of its error. When the
  failure left a repair bundle (see
  [Repairing a failed bump](#repairing-a-failed-bump)), the row ends with
  `(repair: bentoo overlay autoupdate --repair <cat/pkg>)`;
  on a narrow terminal the error text is what gets cut, so the hint stays
  readable.
- A build gate's `ebuild` streams its output into its package's tail instead
  of writing to the terminal. Only the compile step's `sudo`/`doas` password
  prompt still gets the terminal.

When the run ends, the region is released and every finished package's
`✓`/`✗` row is printed once below it, oldest first, neither folded nor cut.
A `Ctrl-C` or an interrupted run releases the region the same way, so its rows
are printed once too; the package still running at that moment is reported in
the Apply Result.

Without the live region (plain mode), each package event is a
`[<cat/pkg>] …` line on stderr, as before. A build gate's output is not
streamed there; it is captured for the build verdict and the failure.

##### The output of a failed manifest run

When the staged Manifest step failed, the Apply Result prints the error on
one line, then the output of each failed `pkgdev manifest` run once, under its
label: `first manifest run`, then `re-check after LLM fix` when the manifest
fixer ran and the check after it failed too.

```text
    first manifest run output:
      … 212 earlier line(s) omitted
      <the last 40 lines>
      Full log: ~/.config/bentoo/autoupdate/logs/dev-go/govulncheck/1.9.0/first-manifest-run.log
```

At most the last 40 lines are shown. Blank lines and `wget` dot-progress rows
are dropped before counting, and the `… N earlier line(s) omitted` line
appears only when lines were cut. The run's log, with every resolved secret
masked, is written in mode 0600 to
`<logs>/<category>/<package>/<version>/first-manifest-run.log` (or
`re-check-after-llm-fix.log`), where `<logs>` is the `logs` directory of the
autoupdate state (`$XDG_CONFIG_HOME/bentoo/autoupdate/logs`, else
`~/.config/bentoo/autoupdate/logs`). A later run of the same version replaces
it. No flag or config key moves it. While the run is going, its output is kept
in a hidden `.childrun-spill-*.log` file in `<logs>`, removed when the run
ends; a spill older than one hour, left by a run that was killed, is removed
when the next `--apply` starts.

The Manifest step of an apply that does not stage its tree still prints the
run's output inside the error, with no log file.

##### What the LLM agents are doing

The manifest fixer (`llm-fix`), the build fixer (`llm-build-fix`) and the bump
reviewer (`review`) run `claude` with `--output-format stream-json --verbose`
and report as they go. Each tool call adds a line to the package's tail:

```text
llm-fix: Read <staged package dir>/govulncheck-1.9.0.ebuild
llm-fix: Bash pkgdev
llm-fix: WebFetch go.dev
llm-fix: WebFetch(proxy.golang.org) denied
```

The target is the file path, else the first word of the command (leading
`NAME=value` assignments are skipped), else the URL's host; it is masked for
known secrets and capped at 160 characters. Each tool the agent was refused
adds a `<stage>: <tool> denied` line when its run ends. The model's text is
never shown. The lines are best-effort: a burst faster than the region takes
them can lose some, and bentoo then logs one warning with the package, the
stage and how many were dropped. No line is added once the agent's run has
returned. In plain mode they are `[<cat/pkg>] …` lines on stderr.

The whole event stream is kept in
`<logs>/<category>/<package>/<version>/<stage>-agent.jsonl` (mode 0600), with
every known secret masked, JSON-escaped forms included. bentoo never writes it
through a symlink. The registry fixer still uses plain JSON output and reports
no lines.

#### Repairing a failed bump

When `--apply` fails a package after staging it, the staged tree is kept and a
repair bundle, `.bentoo-repair.json`, is written at its root (mode 0600). The
Apply Result then prints the next step:

```text
    Repair:  bentoo overlay autoupdate --repair dev-go/govulncheck
```

The bundle (schema 1) records the package, the current and candidate versions,
the failed stage (`manifest`, `llm-fix`, `llm-build-fix`, `re-check`,
`gate:<name>`, `adopt`, …), the failure text (passed through the same secret
redaction as the log, last 16 KiB), the tools the unattended agent was refused,
the staged tree, the candidate ebuild, the upstream URLs and its creation
time. The staged sweep (`bentoo overlay staged`) keeps any tree that carries a
bundle. After a `--repair` that did not pass, the bundle carries a
`repaired_at` mark, and a later `--apply` or `--check` of that package does not
restage over the tree: it refuses the package with `staged tree kept for
repair: bentoo overlay autoupdate --repair <cat/pkg>, or delete <bundle> to
give the repair up`, leaving the tree, its bundle and the pending entry as they
were. A bundle without the mark, as a plain failed `--apply` writes it, is
restaged over as before. That mark is the only part of the bundle `--apply`
and `--check` read; everything else in it is for `--repair` and for you.

To give a repair up, delete the tree's `.bentoo-repair.json`. With the bundle
gone, the next `--apply` restages the package from scratch, and the staged
sweep treats the tree like any other.

`--repair <cat/pkg>` cannot be combined with another mode and needs a terminal
on stdin and stdout and `claude` on `PATH`. It refuses, with exit 1 and before
asking anything, when no bundle exists (it lists the packages that have one),
when the bundle does not parse or names a tree that is gone or is not the
directory it sits in, and when the package's pending candidate is not the
bundle's version. It takes the overlay lock for the whole run, so a concurrent
`--apply` reports the lock as held.

It prints the bundle and asks `Start an interactive claude session in <dir>?
[y/N]`. `--yes` never answers this question; anything but `y` or `yes` exits 0
and changes nothing. On `y`, `claude` runs in the foreground in the staged
package directory, seeded with the bundle:

- permission mode `default`: every command, fetch and edit is asked for;
  `--dangerously-skip-permissions`, `bypassPermissions`, `acceptEdits` and
  `dontAsk` are never passed;
- deny rules for editing anything under the published overlay and for reading
  or editing the secrets files; bentoo adds deny rules only, never allow rules,
  and your own claude settings, plugins and `CLAUDE.md` apply;
- the environment is yours minus every `BENTOO_*` variable, `GITHUB_TOKEN`,
  `GH_TOKEN`, `GITLAB_TOKEN` and the variables named by `llm.api_key_env` and
  the search provider's `api_key_env`;
- Ctrl+C and SIGQUIT belong to the session; bentoo is not stopped by them.

When the session exits, with any status, bentoo re-validates the edited tree
exactly as `--apply` would — manifest, then the gates, at no less than the
depth recorded for the tree, with `--compile` when the bundle failed at the
compile gate or `--compile` was given — with no LLM agent, and promotes it on
success: the bundle is removed and the pending entry is completed. A failed
re-validation rewrites the bundle with the new stage, the failure text and the
`repaired_at` mark, and exits 1. Two cases skip the re-validation, leave the
tree as the session left it and mark the bundle, exit 1: bentoo received
SIGTERM or SIGHUP during the session (run `--repair` again), or the run was
interrupted before it.

Promotion publishes only the ebuild and its Manifest. A repair that changes
`files/` or `metadata.xml` in the staged tree is refused, naming the first
differing path: carry that change into the overlay by hand, then run
`--repair` again.

#### Vendor tarballs

A Go ebuild in the overlay usually fetches two distfiles: upstream's source
archive and a `<name>-vendor.tar.xz` holding the `go mod vendor` tree, which
the overlay serves from its own host. Upstream never publishes the second one,
so a bump that carries `${P}` into its name points at a URL that does not
exist yet, and the staged Manifest fails on a 404. The vendor step builds that
tarball before the Manifest is written.

**When it runs.** For a staged bump whose ebuild inherits `go-module` and whose
`SRC_URI` holds exactly two distfiles: one source archive, and one
`<name>-vendor.tar.xz` fetched from a host listed in `autoupdate.vendor.hosts`
(default `distfiles.obentoo.org`; see
[Configuration Options](configuration.md#configuration-options)). The name
must start with a letter or a digit. `SRC_URI` is read from the md5-cache entry
`egencache` writes for the staged tree, so eclass and variable expansion has
already happened. An entry that cannot be read is logged as a WARN and the bump
continues the old way; any other ebuild is left alone.

A HEAD request to the vendor URL then decides what happens:

| Answer | What the step does |
|--------|--------------------|
| `404` | Generates the tarball into the run's private distdir, so `pkgdev manifest` digests a local file instead of fetching it. The task's stage reads `vendor` meanwhile. |
| `200`, and the name carries the new version | Nothing: the tarball is already served. |
| `200`, and the name is pinned to another version | Compares the served tarball's `vendor/modules.txt` with one regenerated from the new source. If they differ the package fails with `… does not match the vendor tree of PV X; point the ebuild's vendor distfile at ${P}-vendor.tar.xz` — that ebuild edit stays manual. If the served tarball cannot be downloaded, the comparison is skipped with a WARN. |
| `5xx`, a timeout or a transport error | The probe failed: a WARN names the package, version and URL, and nothing is generated. Whether the tarball is served is unknown, so the manifest failure that follows is returned as is and the LLM manifest fixer is not invoked. |
| anything else (e.g. `403`) | Not owned: the bump takes the old path, LLM manifest fixer included. |

**How the tarball is built.** The source archive is downloaded into the
distdir when it is not there yet (each distfile download has its own 30-minute
limit), unpacked, and `go mod vendor` runs in it with
`GOFLAGS=-modcacherw GOTOOLCHAIN=local`. The vendor tree is packed with the
recipe of the overlay's `scripts/go-vendor.sh`: entries sorted by name, owner
and group `0`, mtime `0`, `XZ_OPT=-9T0`. The same source and the same host
tools give the same bytes. It needs network access to the Go module proxy and
is stopped after 10 minutes. A failure here fails the package, and the LLM
manifest fixer is never run for a failure the vendor step owns: no ebuild edit
can produce a tarball.

**`--check`** runs the validation gates on the generated tarball and never
uploads it. The package's validation row carries the note
`vendor tarball generated locally, not uploaded`, which the JSON report also
holds in the row's `notes`.

**`--apply`** publishes the tarball before the bump is promoted, because a
Manifest that digests a tarball nobody can download breaks every user's
fetch.

- **With `autoupdate.vendor.upload_command`**, the command runs as an argv list,
  without a shell, with `{path}` and `{file}` replaced once, and is stopped
  after 5 minutes. Credentials come from the command's own environment or
  profile, never from bentoo. INFO lines name only the program and the URL; the
  full argv and the command's output are logged at debug level only (argv
  values are masked in the output), and never reach the error. bentoo then
  downloads the URL with `Cache-Control: no-cache`, up to 3 times 2 seconds
  apart, and its SHA512 must equal the generated tarball's. Otherwise the
  package fails and nothing is published; a different digest means the
  uploaded object must be removed or replaced before the next run.
- **Without it**, nothing is published. The tarball is kept at
  `<staged tree>/.bentoo-vendor/<file>`, and the Apply Result reads:

  ```text
      Status:  Upload pending
      Action:  upload <path> to <uri>
  ```

  The pending entry is marked failed with that reason and the exit status
  counts it as a failure. Upload the file, then run `--apply` again: the URL is
  served by then, so the tarball is fetched, not regenerated.

A retained staged tree whose record says it digested a tarball that was never
published is not promoted as it is: the next `--apply` validates it again
(`vendor tarball generated but not published; re-validating`).

**A CDN can keep answering 404 after the upload.** Cloudflare in front of
`distfiles.obentoo.org` was measured caching a GET 404 for a year
(`max-age=31536000`) and ignoring `no-cache`, so a tarball that was requested
before it existed stays unreachable. When the URL still answers 404 but the
origin, reached with a cache-busting query string, serves the right bytes, the
error says to purge that URL from the CDN cache and re-run `--apply`. A cache
rule that does not cache 404s avoids the purge.

`--timeout` (see [Timeouts](behaviour.md#timeouts)) also bounds the HEAD probe
of an `--apply` run.

Out of scope: npm and cargo vendoring, packages that need another generator
first, and the ebuild edit for a stale pinned tarball.

#### Example Autoupdate Workflow

```bash
# 1. Analyze a new package to generate its schema
bentoo overlay analyze www-client/myapp
# → Outputs suggested packages.toml entry

# 2. Add the schema to packages.toml
# ... edit packages.toml ...

# 3. Run autoupdate to check for new versions
bentoo overlay autoupdate www-client/myapp
# → Fetches upstream, applies version bump if found

# 4. Review and commit
bentoo overlay status
bentoo overlay add www-client/myapp/
bentoo overlay commit
# → "up(www-client/myapp-1.0 -> 1.1)"
```
