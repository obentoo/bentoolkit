# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **One report, three ways to read it — `overlay autoupdate --check` gains a view
  model, three renderers and two exports.** The check printed 348 lines to
  describe four pending updates. Over the story's fixture the same run is now 42.
  The volume was a symptom: every section formatted at the moment it printed, so
  nothing held "what this run found" as a value and nothing could be rendered
  twice, exported, counted or tested without re-running the command. Three table
  styles appeared on one screen because each was written independently, with
  nothing for them to agree *with*. There is now something to agree with, and a
  test proves the model holds no presentation: `internal/common/report` imports
  no terminal library and nothing from `internal/autoupdate`, and a parser fails
  the build on either.
- **A fourth tally column: `inconclusive`.** The third column used to answer two
  different questions — "this host cannot build the package" and "the operator
  configured depth none for it" — so a regression in the toolkit was
  indistinguishable from a policy the operator wrote themselves. The two are now
  separated from a *typed* cause that already existed, never by matching text in
  a human-readable sentence. Expect the inconclusive column to be large at first:
  most skips still arrive untagged, and "the toolkit did not establish why it
  could not answer" is a toolkit limitation. Each producer that learns to name
  its cause tightens the rule. Which packages are **proved** or **errored** is
  unchanged, and a test compares the counts against the previous implementation
  over every gate shape.
- **`ui.mode` — one global setting for how output looks.** Accepts `auto`,
  `plain`, `inline` and `fullscreen`, resolved as `--ui` > `BENTOO_UI` >
  `ui.mode` > `auto`. `overlay manifest` now inherits the same answer instead of
  deciding independently, so there is no longer a different switch per command.
  **With no `ui.mode` and no flags nothing changes**: `auto` yields inline on a
  terminal and plain off one, which is exactly what the old gate returned,
  including under `NO_COLOR` and `BENTOO_NO_TUI`.
- **`--ui=fullscreen` — an alternate-screen report, which never costs the
  scrollback.** The report is built before the program starts and printed on
  every exit path: normal completion, the quit key, an interrupt, and a panic.
  It is never read back from the TUI's state — a panic inside the view would
  otherwise take the report with it. An interrupted run is labelled incomplete
  and states how many planned packages it never reached, in every format.
- **`--export=<path>` — the complete report as a file.** Markdown for `.md`,
  JSON for `.json`, plain text otherwise. The export is always complete: every
  package, every reason in full, no shortening, regardless of `--all` or of what
  the terminal showed. That is enforced by the signature rather than by a branch
  — the export renderers take no display options at all. An unwritable path is
  reported and the terminal still gets its report.
- **`--all` — list the packages behind the up-to-date count.** It changes what is
  listed and nothing else; a parser fails the build if the flag is referenced
  anywhere that could narrow what the run acts on.

### Changed
- **Column widths are measured, not typed.** A column is as wide as its widest
  value in *this* run, measured in display cells — so a multi-byte character is
  not counted as several columns and an escape sequence is not counted at all.
  The four `%-45s` in the check path are gone. 45 was chosen once, by hand, and
  the correct width depends on the packages a particular run produced, which is
  knowable only after the run. The three `%-45s` outside the check path are
  deliberately untouched; `ColumnWidth` and `Shorten` are exported so whoever
  next touches them has the replacement at hand.
- **A reason is printed once.** For every skipped package the check printed the
  same ~230 characters twice, because the plan printed the reason and the result
  printed it again through a fallback, and neither function knew the other ran.
  The full text is always preserved in the model, so shortening is a rendering
  decision and never a loss of data — the exports carry the whole sentence.

### Fixed
- **`overlay autoupdate --check` no longer states itself twice.** A `--check
  --llm` run printed the heading `Version Check Results` twice, and
  `Validation Plan` twice with every package's bump and depth repeated under it.
  The report the previous release added did not replace the printers it was
  meant to replace; both survived, side by side, under byte-identical headings.
  The old printers are now deleted rather than merely disconnected, and what
  guards the rule from here on counts *producers in the source* — a test that
  counted headings in captured output could only ever fail while the second
  producer still existed.
- **`--ui`, `--all` and `--export` now work on every `--check`, not only on
  `--llm` runs.** They were accepted, ignored, and exited 0 — `--export` wrote
  no file and said nothing. The cause was one early return: rendering had been
  wired inside the branch that decides whether to spend an LLM, so a run that
  declined to validate also declined to draw. Those are unrelated questions.
  The gate still means "do not validate"; it no longer means "do not draw".
- **`overlay autoupdate --check <package>` builds and renders a report.** The
  single-package path returned before the batch path's report was ever built, so
  it inherited every flag that did nothing. It now renders the same report for
  the one package it scanned, and still returns before the registry
  reconciliation, which remains the batch path's alone.
- **A check that validated nothing no longer pads its output with empty
  sections.** A scan-only report stated "No pending update to validate", "No
  package was evaluated" and "0 package(s) evaluated: 0 proved, 0 errored, 0
  inconclusive, 0 skipped" — twelve of twenty-three lines reporting that the
  thing you did not ask for did not happen. Those three sections now omit
  themselves when there is no plan. A run whose packages were all excluded *by
  policy* still gets them: it planned them, and the plan is where the exclusion
  is explained.
- **`--check --quiet` over an empty registry is still silent.** `--quiet`
  reaches the logger and reaches neither the terminal styling nor stdout, so a
  run with no configured package is the one case it can silence today. Rendering
  a report there would have taken that away. The sentence stays on the logger,
  nothing is drawn, and a run *with* packages still prints its table under
  `--quiet` exactly as before — that asymmetry is preserved deliberately, not
  inherited by accident.
- **A batch check says when it auto-disables an orphaned package.** An entry
  whose ebuild has vanished from the overlay is written back to `packages.toml`
  as `enabled = false`, and the line that announced it went away with the old
  printer — leaving a hand-maintained file edited in silence. `--check <package>`
  never lost the notice; only the batch path had gone quiet.
- **The version check states the source/bin tally again**, as
  `Checked N source, M bin`. It also names packages that resolved to neither
  tier instead of folding them into a column: an ebuild that could not be read
  established nothing about how the package builds, and counting it as source
  would state a fact the scan never found.

### Deprecated
- **`--no-tui` — use `--ui=plain`.** Its behaviour has not changed: it is still
  honoured, it is exactly `--ui=plain`, it still outranks `--ui` and `BENTOO_UI`,
  and `NO_COLOR` and `BENTOO_NO_TUI` mean the same thing as passing it.

## [0.27.0] - 2026-08-22

### Added
- **`--depth=install` — the ladder gains a rung that validates `src_install`.**
  The staged validation ladder proved that a bumped package builds; it stopped
  one phase short of proving that the package ASSEMBLES. `src_install` is where
  a `doins` of a file upstream renamed dies, where an
  `emake DESTDIR="${D}" install` rule that stopped working surfaces, and where
  most of Portage's QA notices are produced. The gap was named three times in
  the protocol this ladder was built from and deferred on purpose; this closes
  it.

  The rung is accepted wherever a depth is configured — `--depth=install` on
  `overlay autoupdate`, `overlay validate` and `overlay compare`, every
  per-class config key, and every per-package override — and it costs a compile
  plus `src_install`, not two compiles: the phases cascade inside a single
  `ebuild` invocation.

  A pass STATES ITS OWN CEILING, as every rung of this ladder does. It names
  both things it did not cover: `qmerge`, which is the package manager's
  activity and stays out of this ladder permanently, and `src_test`, which this
  gate switches off. A compile pass keeps saying that `src_install` went
  uncovered, because at compile depth that is still true.

  **`src_test` is disabled inside the build child, so the verdict is a fact
  about the candidate rather than about the host.** `src_test` runs between
  compile and install, so on a machine configured with `FEATURES=test` the new
  rung would silently run upstream's suite and two machines would disagree
  about the same bump. The child receives exactly one `FEATURES` assignment —
  the inherited value with ` -test` appended, composed rather than added as a
  second entry — which subtracts that one feature and preserves `sandbox`,
  `network-sandbox`, `userpriv`, `ccache` and everything else the host set. The
  report says so, because it is a subtraction the operator did not ask for.

  A failed `src_install` reaches the same repair loop every other build gate
  reaches: the fixer is told `install`, the authoritative re-run runs the
  install gate, and a failure the MACHINE caused still refuses to spend an
  agent invocation.

- **A design system for the terminal UI, under `misc/design/design-system`.**
  Thirteen components — status lines, transitions, rules, boxes, groups, trees,
  tables, key/value blocks, gate lists, tallies, progress and prompts — plus the
  theme they draw with, and a gallery that renders the whole catalogue:
  `go run ./misc/design/design-system/gallery`.

  It exists because bentoo had TWO visual grammars that did not know about each
  other: `internal/common/output` on fatih/color, consumed by 24 command files,
  and `internal/common/tui` on lipgloss, consumed by one. Nothing made "success"
  the same decision in both, 305 direct `fmt.Print*` calls bypassed both, and
  the glyph inventory had drifted far enough to spell one symbol two ways.
  Every component was read out of an existing call site; none was invented.

  A render has THREE faces rather than two, because `NO_COLOR` is a statement
  about colour and not about UTF-8: `Plain` assumes nothing and is pure ASCII,
  `Unicode` drops the colour and keeps the glyphs, `Styled` has both. `Plain` is
  the zero value, so code that forgets to choose gets the face that is safe
  everywhere. `--json` is deliberately NOT a fourth face: a component is a typed
  value with json tags, so the machine-readable output cannot disagree with the
  human one about what happened.

  **Nothing is wired into any command.** `make build-all` still produces exactly
  the two release binaries, no existing output changes, and the gallery is not
  shipped. It is a proposal that compiles — wiring it in means changing output
  that scripts already consume, which is a decision to take per command rather
  than as a side effect of adding a package.

### Changed
- **`--compile` now states its ceiling in its own help.** The privileged gate
  still stops at `src_compile` and is unchanged; what is new is that it says so,
  and names `--depth=install` as the unprivileged path that goes further.
- **No shipped default moved.** `revision` and `patch` stay at `options`,
  `series` and `major` stay at `configure`. Adding a rung is a capability;
  promoting a default is a cost decision, and an upgrade must not make
  yesterday's sweep more expensive than the operator agreed to. `install` is
  reached only by asking for it.

## [0.26.1] - 2026-08-22

### Fixed
- **A privileged compile gate no longer dies before it reads the ebuild.** The
  gate escalates (`sudo ebuild <path> clean compile`), and running as root is
  exactly what makes Portage honour `FEATURES="userpriv userfetch"` — from that
  point the repository is read, and distfiles are fetched, by uid `portage`.
  Everything the gate handed it was built for the invoking operator alone: a
  staged tree at 0750/0600 and a private distdir at `os.MkdirTemp`'s 0700. uid
  `portage` could not traverse the staged tree at all, so every bump reaching a
  privileged compile died identically on

      !!! Permission Denied: <staged>/profiles/thirdpartymirrors

  before unpack, before `src_prepare`, before anything about the candidate had
  been exercised. The attribution gate called it the host's fault, correctly,
  and the bump still could not be applied.

  The directories this run OWNS are now opened to the `portage` group
  immediately before the privileged child is spawned: the staged tree, the
  directories leading down to it from the staging root, and the private distdir.
  Opening the tree without opening the path to it would have fixed nothing — uid
  `portage` is refused at the staging root and never reaches the tree whose
  modes were just corrected.

  The group, and not a wider mode: a candidate nobody has reviewed, plus
  whatever a fixer wrote into it, still does not belong in a world-readable
  directory, so group bits MIRROR the owner's and never exceed them. The staged
  tree is opened for reading only; the distdir also for writing, because that is
  what a fetch under `userfetch` does. Ancestors get traversal alone, never read.
  The published overlay and the host's own DISTDIR are deliberately untouched —
  neither is this gate's to re-permission.

  Doing it at spawn time rather than at creation time is what also covers the
  manifest step and the build fixer: a repair landing a fresh 0600 ebuild moments
  before the re-run would otherwise have reintroduced the same failure on the
  second attempt only. A host with no `portage` group is a no-op.

## [0.26.0] - 2026-08-20

### Added
- **`bentoo overlay staged clean` — a caller for the staged-tree sweep.** Story
  039 shipped the sweeper deliberately without one: a sweeper whose report nobody
  has read is not something to publish blind. This is that caller. It plans,
  prints every removal and every keep with the reason it stays, takes a
  confirmation, and only then removes. A plan that removes nothing says WHICH
  emptiness it is — nothing found, or everything present protected — and returns
  without asking.

  Three gates in trust order: `--yes` warns and proceeds; a non-interactive
  session prints the plan, removes nothing and names `--yes`; otherwise one
  prompt covers the whole plan. The executor is unreachable until a gate has
  passed, so a declined sweep is harmless by construction rather than by care.

  Measured on a real staging root rather than asserted: 41 trees,
  **21 removable, 20 kept**. Every one of the 20 was kept for the same reason —
  no validation record, outcome unknown.

- **A validation run now records what its gates said.** This is what makes the
  count above stop being zero. `validate.WriteStageRecord` had exactly one
  production caller, inside the applier; the `validate` package only read
  records. Since the retention rule keeps every recordless tree as "outcome
  unknown", **every tree an `overlay validate --depth` run left was permanently
  unremovable** — precisely the accumulation the sweeper was written to stop.
  A run above the options depth now leaves a record beside each tree it staged,
  carrying the gates it reported and the depth it selected.

  An interrupted run records nothing: gates that were stopped are not gates that
  answered, and recording them would turn Ctrl-C into a promotion one run later.
  A record that cannot be written never fails the run — the cost is one tree that
  stays.

- **A stage record names what produced it.** A record is not neutral metadata: it
  feeds the promotion reuse path, where a later `--apply` promotes a bump without
  running a gate again. Teaching a read-only command to write one would make
  `overlay validate` a producer of publication evidence. So records carry
  `produced_by`, and the reuse path requires the applier's own provenance. A
  record with no producer reads as applier-produced, so every record already on
  disk keeps its meaning.

### Fixed
- **A reuse REFUSAL no longer loses its reason.** The reviewer's re-decision
  reassigned the depth reason wholesale, discarding whichever of the five
  mismatch reasons the reuse path had computed. All five were affected; the
  promoting path escaped only because it returns before that line. An operator
  now learns why a retained tree was not used instead of seeing an ordinary slow
  apply.

- **The upstream tag prefix is stripped once, at the checker.** A GitHub-style
  tag ("v3.2.3") used to flow raw into the check display, `--list`, and
  `pending.json`. The applier and `Validate` stripped it defensively before
  acting, but the validation plan classified the raw value, could not read it
  as a Gentoo version, and charged a patch bump the deepest class — the plan
  priced hours of configure the run would then not spend, so the operator
  confirmed a cost that was not real (observed live: `dev-libs/imath`,
  `app-editors/vim`, `app-editors/vim-core` planned at configure for a patch
  bump). The checker now normalizes at its single convergence point via the new
  `NormalizeUpstreamVersion`, and the plan applies the same helper before
  classifying, so a pending entry written by an older binary is priced as the
  run will execute it.

### Reach, stated rather than implied
- Trees left by `overlay compare --depth` **stay kept as outcome-unknown**. It
  shares the staging root but was never taught to record. Named here rather than
  left to be discovered.
- There is **no lock** on the staging root, deliberately: the retention rule is
  expressed by the path, and a central index would put back the one file every
  worker writes through. A sweep running beside an `--apply` or a `--depth` can
  therefore remove a tree that run is using. Nothing detects it; the cost is
  bounded to that one run, which fails as an environment failure and publishes
  nothing. The command's help states this.

## [0.25.0] - 2026-08-20

### Added
- **A package that legitimately carries no Manifest is now build-tested.** With
  a thin tree a `git-r3` ebuild or a metapackage has no Manifest, because there
  is no distfile to digest — and the class is not exotic: `acct-group/*`,
  `acct-user/*`, every `virtual/*`, plus `net-dns/bind-tools` and
  `sys-kernel/linux-firmware` in ::bentoo. Manifest production answering with no
  content was a hard error, so that whole class had never been exercised by a
  build gate.

  The class is decided by Portage rather than by a heuristic, because a
  heuristic here is unsafe and the obvious probes do not work: Portage refuses
  to answer any question at all about an ebuild in a thin-manifest tree with no
  Manifest FILE. With an empty one present it answers correctly — an ebuild with
  no `SRC_URI` reaches the build phases, and one with `SRC_URI` is refused at
  digest verification **before any fetch is attempted**, measured with the URI
  pointing at a port where nothing listens. So the staged tree gets an empty
  Manifest and the gates run. Manifest production failing, or a Manifest that
  exists and cannot be read, are still hard errors.

  Portage refuses before any *phase marker* as well, which the first cut of this
  did not account for: the gate list came back entirely skipped with no cause
  recorded, and an unrecorded cause promotes — so the class reported its own
  misclassification as a pass. A gate of this class that never reached a phase is
  now blamed on the candidate, because the empty Manifest is a bet this tool
  placed. Only this class, and only where no cause was recorded: for an ordinary
  candidate a build that dies that early may have died of a flaky mirror, and
  blaming the ebuild there would withdraw a bump over a fact about the network.

- **A sweeper for staged trees that have left scope.** Staging replaced the tree
  of the package it was staging and nothing removed the others, so a `--depth`
  run over the whole overlay left one tree per package under the staging root,
  permanently. The sweeper keeps the trees whose gates FAILED — that is the
  artifact an operator still needs — and removes the rest. It recognises only
  what it produced, by a marker that must agree with the path it sits at, keeps
  and reports anything else, and refuses a staging root inside the published
  overlay through the same check staging already uses. No command calls it yet.

### Fixed
- **`overlay compare --realign --depth` now proves a candidate through the same
  prepared build `overlay validate --depth` runs.** Validation is not a wrapper
  around staging and the gate ladder: it is where the Manifest seam is resolved,
  the Manifest is written into the staged tree, the host is probed for whether
  it can build at all, and two policy fields of the build request are filled.
  The realign path reached the ladder by a second route and started from none of
  it. So its gates were skipped for want of a Manifest, a host missing a build
  dependency became a verdict against the ebuild, `require_isolation` never
  fired its refusal — the same bypass already closed for `overlay validate` —
  and a failed gate retained no log. The upper half now has a name and both
  entry points call it.

- **A candidate no gate measured is no longer promotable.** The promotion rule
  documents itself as existing to stop a bump satisfying "PASS or SKIPPED"
  vacuously, and it did not: the only refusal was a staging failure, so a
  Manifest that could not be produced left every deciding gate SKIPPED and the
  answer was still "promote". It now refuses that — but only where the skips are
  about the CANDIDATE. A host that simply lacks the build dependencies still
  promotes, because the machine says nothing about the bump and refusing there
  would stop `overlay autoupdate --apply` publishing on most workstations.

  The same rule reached a fourth reader. A retained tree's record re-implemented
  "PASS or SKIPPED promotes" without the vacuity clause, and `overlay autoupdate
  --check` writes that record before its manifest step — so a manifest failure
  recorded a proof of nothing and the next `--apply` promoted an ebuild nothing
  had read. That reader now refuses such a record.

- **A build gate reads the distdir it was given.** `DISTDIR` sat on the
  environment allow-list, which filters the PARENT's environment — so the entry
  meant "we let the invoking shell's value through, if it had one", and the
  value resolved by `--distdir` was never exported to the ebuild child at all.
  It is now set explicitly as a computed value and removed from the allow-list,
  so the parent cannot compete with it, and a passing gate states which distdir
  it read. Nothing resolved still means nothing set.

- **A package skipped by `overlay autoupdate --check` can no longer be reported
  without a reason.** `skipReason` cascades through three sources — the skipped
  gate's own reason, the resolved depth's reason, then the plan's — and all
  three are free-form strings nothing forces to be populated. When the cascade
  ran out it returned empty, and the caller printed `not validated ()`: an
  operator reads that empty parenthesis as "checked, nothing to say", which is
  the opposite of what a skip means. The cascade now ends by naming the silence
  instead, so an unexplained skip reports itself as one.

- **A package that declares no archive is no longer reported as a broken
  checkout.** Under thin manifests a Manifest holds DIST lines and nothing
  else, so a package with no distfile has no Manifest file at all. One of the
  two seams that read it already knew that; its sibling — the one naming the
  archives the option gate looks for — still reported the absence as a read
  failure. So a package of that class — every `acct-group/*`, `acct-user/*` and
  `virtual/*`, and `net-dns/bind-tools`, `app-eselect/eselect-nodejs` and
  `sys-kernel/linux-firmware` in ::bentoo — had its option gate decline with
  the distfile names "could not be produced": a fault about the checkout,
  sending an operator to repair a file that was never meant to exist. It now
  reports what is true: the caller's list names no distfile, so there was no
  archive to look for. Nothing new was written for that sentence; it has been
  there since the gate shipped, behind a fault report.

  The reach is narrower than it looks, and it is worth stating rather than
  leaving to be discovered: that seam is only wired for `--depth` ABOVE
  `options`, so the default read-only `overlay validate` never reached it and
  its reports are byte-identical before and after — measured over all 357
  packages of ::bentoo. What changes is a build-depth run, where the class used
  to read as a broken checkout. The outcome is unchanged and
  deliberately so — still a reported SKIP, because a package nothing was
  compared against has not been validated. A Manifest that exists and cannot be
  read is still an error, on both seams, with its sentence unchanged.

- **The applier now names the cause of its own candidate declines.** The
  promotion rule refuses a bump whose every deciding gate declined over the
  candidate, and reads that cause from a field rather than from prose. Three
  producers were taught to set it; the applier's own two — a staged tree that
  could not be prepared, a manifest step that failed — were not, and those are
  almost word for word the vacuity the rule exists to deny. Both now name the
  candidate. Measured rather than claimed: this changes no promotion outcome
  today, because the apply path already refuses both faults by a different
  route and the field cannot survive a retained tree's record on disk. What it
  ends is the applier being the one producer whose vacuities are
  indistinguishable from a host that merely cannot build. A host that cannot
  build still publishes, unchanged. Every skip producer left unstamped now says
  at its own site why its cause is genuinely unknown rather than merely
  unexamined.

- **A registry key with a `:slot` or `@label` suffix now stages a tree Portage
  can address.** Such a key has two roles and only one of them is a path
  Portage reads: the stage root is the retention identity and must keep the
  suffix, or slot 4.1 and slot 6 of one package collapse into a single tree,
  while everything INSIDE the staged repository — the package directory, the
  ebuild filename, `files/`, the Manifest — and every atom handed to `ebuild`
  or `emerge` must be free of it, because Portage accepts neither `:` nor `@`
  in a package name. One function served both roles and stripped nothing, so
  the content was written under the suffixed name while every consumer looked
  for the clean one. Measured in the field: `overlay autoupdate --apply all`
  failed 4 of 4 suffixed entries at the manifest step, which chdir'd into a
  directory that had never existed. That step was only the first gate to reach
  the divergence — repairing it alone would have carried the bump into the
  Manifest reads, then the ebuild path, then the emerge atom, one gate per
  apply. The content now derives from a second, suffix-stripping split that
  also refuses a key whose suffix survives it, so a future leak fails at the
  seam by name instead of as a ghost directory three gates later. The staged
  repository's NAME still derives from the full key, on the writer and on the
  recomputing fallback alike, so two release lines at one version cannot
  collide on a repository name. An unsuffixed key stages byte-identically.

  The failure it caused also stops burning the LLM fixer. A spawned command
  whose working directory does not exist is the machine's fault, never the
  ebuild's — no rewrite of an intact file can repair it — so it is now
  classified as an environment failure and the fixer is not invoked. A command
  that RAN and exited non-zero still reaches the fixer, which is the repair
  that gate exists to allow. And a fixer command that could not start is
  reported as that, instead of printing the whole error where an exit code was
  promised.

- **A privileged `--compile` PASS now means what a `--depth compile` PASS
  means.** The privileged path set no environment at all, so the distdir the
  run resolved reached the `ebuild` child not at all, while the unprivileged
  ladder has exported a computed one since the gates learned to say where their
  sources came from. Setting it on the spawned command would not have fixed it,
  and that was measured rather than assumed: with `env_reset` in force nothing
  Portage-related survives the privilege boundary, so an exported value is
  discarded before `ebuild` ever sees it. What does survive is the privilege
  tool's own argument form, so the resolved directory now travels as
  `sudo DISTDIR=<dir> ebuild …` — a literal argument this run computed, which
  is why nothing new crosses from the operator's shell. A run that resolved no
  directory still sets nothing and invents nothing.

  `doas` is treated as its own case rather than assumed equivalent: it has no
  argument form for an environment assignment and would try to execute a
  program by that name, so on a doas host nothing is passed and the gate says
  the distdir could not be enforced. A pass that cannot support its
  hermeticity claim must not imply one. The privileged PASS also carries the
  same two fidelity statements the unprivileged one carries — the distdir it
  read and its isolation label — produced by the same functions rather than by
  a second copy of their text, so a rewording lands on both paths at once.

### Changed
- **One reader for the DIST names a Manifest declares.** Two call sites read the
  file once only to prove it readable and then handed the path to a parser that
  reads it again, because that parser answers a missing or unreadable Manifest
  the same way it answers one that declares nothing. An error-returning sibling
  now reads once and reports the failure, and both probes are gone. The DIST
  grammar itself turned out to have a third copy, and now has one.

- **The justification above the sweeper's `contextcheck` suppression no longer
  looks like a second suppression.** It was written as `// nolint:contextcheck
  — …`, which golangci-lint does not recognise as a directive (a directive takes
  no space after the slashes and separates its reason with `//`). The
  suppression was never missing — the inline `//nolint:contextcheck` on the
  statement itself does that work — but a line that reads as the directive and
  is not one invites deleting the one that matters. It is now plainly prose and
  says why.

## [0.24.0] - 2026-08-18

### Added
- **`overlay validate --depth` now runs the build gates it names, instead of
  reporting a deeper class of skip.** A staged tree deliberately leaves without
  a Manifest — it describes the versions already published, not the candidate —
  and Portage refuses an ebuild whose Manifest does not describe its archive. So
  every build gate over a staged tree died in the setup phase and came back
  SKIPPED, which promotion read as acceptable.

  **What this entry covers, and what it does not.** The cause above was first
  measured on the *realign* path — a proof of 13 packages "passed" with 26/26
  gates SKIPPED and zero builds executed. That measurement is why the seams
  exist, but the realign path is **not** fixed here. `realign.Prove` calls
  `Stage` and `RunBuildGates` directly rather than going through `validate.Run`,
  so it supplies no Manifest, its gates still report SKIPPED, and
  `PromotionDecision` still returns passed for an all-SKIPPED list. What is
  fixed is `overlay validate --depth` and the autoupdate applier, which do go
  through `Run`. Closing the realign path needs the same seam wired into a
  second caller and is tracked separately.

  `validate.Options` gains two optional seams, `DistNames` and `StagedManifest`
  (`internal/autoupdate/validate/run.go`). The caller supplies the distfile
  names and the Manifest content; `Run` materialises the bytes inside the staged
  tree at mode 0600 and then runs the gates. Manifest *generation* stays in
  `internal/autoupdate` — `cmd/bentoo` composes the two, which is the only place
  that already imports both. The zero value of every new field reproduces the
  previous behaviour byte-for-byte, and that was proved by execution rather than
  by reading the diff: all four nil-seam refusals rendered from the new code
  diff empty against the same four rendered from the previous release's format
  strings.

  The applier feeds the same seams, and `lendPublishedDistNames` — which wrote a
  temporary Manifest into the staged tree so the gate could read names out of
  it, then defer-removed it — is deleted. That coupling had a failure mode worth
  naming: on a staged tree the gate could not write to, the lend died, the gate
  reported SKIPPED, and the bump was published on the strength of a gate that
  read nothing. `--check` and `--apply` reach the seam through the shared path,
  so the two drivers cannot drift apart in silence.

  Also added: `Options.LogDir`, so a standalone run retains the full build
  transcript in the same directory the apply path uses.

  Proved on the real golden pair under `-tags live`: `gst-plugins-qt6-1.29.2`
  fails the configure gate naming `aalib`, `1.28.6` configures cleanly, and the
  published overlay is byte-identical after a real build — 6 PASS / 0 FAIL,
  where the same suite measured 4 PASS / 2 FAIL before.

- **A proved realignment can now actually be published — per package, on
  evidence, and only on the maintainer's yes.** `realign.Promote` shipped with
  story 034's Stage 2 carrying every guard R5.3–R5.5 ask for, and nothing ever
  called it: a realignment could be proposed, staged, built and approved of in
  principle, and the answer still went nowhere. `overlay compare --realign
  --depth=<rung>` now asks, after each proof that PASSED, one question per
  package naming its atom, and a yes replaces the published ebuild with the
  exact bytes the gates read (`cmd/bentoo/overlay_compare_promote.go`, wired in
  `cmd/bentoo/overlay_compare_depth.go`).

  Three properties hold by construction, and each carries a test: the question
  is only put when at least one gate reported PASS — measured on this host
  (2026-08-10, re-confirmed 2026-08-16), a staged tree carries no Manifest, so
  `ebuild` dies in setup and every gate SKIPs, and an all-SKIPPED proof
  satisfies `PromotionDecision` while proving nothing, so it is refused as "a
  proof of nothing" instead of asked about; `--yes` keeps buying the build
  prompt only and never answers publication (its help text now says so —
  `cmd/bentoo/overlay_compare.go`); and a refusal from `Promote`
  (`ErrNotPromoted`) renders apart from a write error, because "an authority
  said no, nothing was written" and "the write broke after every authority said
  yes" must never be readable as one another.

- **The `::gentoo` ebuild is now the stated baseline for every `::bentoo`
  ebuild, and `overlay compare --realign` says how far each one has drifted
  from it.** The policy was never written down anywhere. Divergence is allowed —
  it is often the whole point of the overlay — but it is a decision, and a
  decision has a reason and usually a condition under which it stops applying.
  What we had was divergence with neither.

  Measured on the overlay before writing any of this: 321 packages scanned, 237
  also carried by `::gentoo`, 80 of those at the very same version, 84 with no
  `::gentoo` counterpart at all — and **zero** registry entries declaring a
  divergence. So on the first run every difference in the overlay is undeclared
  by definition, and the first useful output is not a realignment but a set of
  candidate declarations a maintainer can paste or edit.

  `ResolveBaseline` names the ebuild each comparison is measured against: the
  same version when `::gentoo` carries it, the nearest one otherwise with the
  distance stated, and nothing at all for a package it does not carry. The
  distance is reported because it bounds how much the comparison is worth — a
  baseline one patch away and one three series away are not the same kind of
  evidence, and the report must not let them look alike.

- **The finding that produced obentoo/bentoo#33 costs a `grep`, not a prompt.**
  `::gentoo`'s `gst-plugins-qt6-1.26.11` is `inherit gstreamer-meson` and passes
  **2** `-D` options; ours is `inherit meson python-any-r1 xdg-utils` and passes
  **85**, two of which stopped existing at 1.29. The whole of that issue sits in
  that pair of lines, and detecting it is a text comparison and a count rather
  than a judgement.

  Four axes are therefore compared directly — `inherit` and `IUSE` as set
  differences, the build options as counts and names, the dependency atoms as
  full specs grouped by atom. `CompareAxes` takes no provider and holds no way
  to reach one, so the axes keep answering under `--no-review`: a signal this
  cheap has no business depending on a provider being configured. The golden
  test empties `PATH` to prove it.

- **A divergence can now declare itself, in the ebuild rather than the
  registry.** `# BENTOO-DIVERGENCE: <axis>: <reason>` with an optional
  `#   drop-when: <condition>` continuation. The convention is not invented
  here: `sys-devel/binutils-2.47` already writes exactly this reason in prose,
  in a comment, and nothing reads it. The declaration lives beside the code it
  describes so it travels when the ebuild is copied to a new version — which is
  exactly when a divergence is silently inherited today.

  `drop-when` accepts a closed vocabulary answerable from the local tree:
  `gentoo-version >= X`, `gentoo-has-package`, `gentoo-inherits <eclass>`.
  Anything outside it is reported verbatim and **unevaluated** — never met,
  never unmet. Read as unmet it would keep a divergence alive forever; read as
  met it would retire one with no cause. A live `9999` ebuild is excluded from
  the version predicate, since it orders above every release and would otherwise
  satisfy every condition ever written.

- **A judgement on what is left, from a model that holds nothing that writes.**
  Only undeclared and expired divergences are sent; a decision with a reason is
  not re-litigated every run. The verdict is written onto the result and read by
  no code that decides an exit code or a `Verdict`, and the test derives the
  deciders from the source rather than from a list, so there is no allow-list
  anyone can widen later. An unreachable model leaves every divergence without a
  verdict, says so, and exits 0 — the deterministic half of the report is
  complete and useful without it. Cost is bounded rather than doubled: one call
  per package, keyed on the two files' bytes in the same cache the existing
  commentary uses, and the number of packages is printed before the pass opens a
  single file.

- **Every count is reported with its denominator, including the share nobody
  could attribute.** Per package and for the run: how many differences went to
  the version move, how many to us, how many to neither, beside the total. A
  classification whose reach is invisible is indistinguishable from a guess.
  `net-libs/nodejs-26.7.0` is the cautionary anchor — 492 lines differing from
  `::gentoo`'s *same* version, 32 mentioning `eselect` and 100 mentioning
  slotting. It is reported by class and never proposed for wholesale
  realignment; reverting deliberate slotting work is the failure mode this must
  not have.

- **Packages `::gentoo` does not carry are reported without granting anyone
  authority.** For the 84, another repository already available locally may be
  named — informative only, never a baseline, and where several carry the
  package each is named and none preferred. A repository that was not consulted
  is reported as **not checked**, never as one that does not carry the package;
  the ~428 registered repositories resolve by name, but nothing on disk holds
  most of their contents and asking about all of them would be thousands of
  network lookups in a stage that is otherwise entirely offline.

- **A proposed realignment is proved the same way a bump is.** "It matches
  `::gentoo` now" is a statement about text, and text is not evidence that the
  package still builds. So the realigned ebuild is materialised in a staged tree
  outside the published overlay and handed to story 033's ladder — `Stage` and
  `RunBuildGates`, unchanged and unwrapped — up to the depth the operator asked
  for. Staging outside the overlay is a security property rather than a tidiness
  one: the overlay auto-commits and pushes, so anything written inside it is
  published before any gate has spoken.

  The realignment path adds **no gate of its own**. The temptation is specific —
  a "matches `::gentoo` now" check would be cheap to append and would look like
  a gate — and it is exactly how this path would acquire an authority the design
  gave to three separate parties. The ladder's results are reported back as they
  came, in order and in number.

  A gate that fails is an answer and is carried in the report; a staging step
  that could not run is an error, because "we could not look" must never be
  readable as "it does not build". The new `internal/realign` package exists for
  an import edge: `internal/autoupdate/validate` already imports
  `internal/overlay`, so the reverse edge would be a cycle, not merely a
  boundary violation.

- **Publishing a realignment needs the maintainer AND the gates, and a passing
  gate is not consent.** The shortcut is the reasonable-sounding one — "every
  gate passed, so what is the maintainer adding?" What the maintainer adds is
  the judgement no gate can make: the overlay's `nodejs` carries a 492-line
  divergence that would pass every rung of the ladder, and reverting it would
  still be wrong. The gates answer "does it build"; only a human answers "should
  we". So approval is a parameter rather than a question the promoting code asks
  itself — a function that asked it could not be tested for refusing.

  A refusal names **which** authority said no, because "not promoted" is one
  outcome for several reasons and an operator who cannot tell them apart does
  not know whether to fix the ebuild, re-run the gates, or say yes. Every
  refusal returns before the first byte of the overlay is touched, and the tests
  assert that over the **whole tree** rather than the one file — a refusal that
  wrote the right nothing but dropped a Manifest passes any per-file check.

  What gets published is the proposal's own bytes, the same slice the gates ran
  against, so "the exact bytes that were proved" holds by identity rather than
  by resemblance; anything re-rendered at promotion time would be a file no gate
  ever read. A proof carrying **no gate at all** is refused: `--depth options`
  and below run no build gate, so "every gate reported PASS or SKIPPED" would be
  true of nothing, leaving approval as the only authority that ever spoke.

- **`overlay compare --realign --depth=<rung>` builds each proposal before
  believing it, behind one plan and one confirmation for the whole run.**
  `--depth` is the switch that turns proving on: `--realign` on its own is still
  report-only, so every invocation shipped before this one behaves exactly as it
  did. Given without `--realign` it is a usage error rather than a silent no-op —
  a command that quietly did nothing with `--depth=compile` would be
  indistinguishable from one that built everything and found no problem, which is
  the worse silence, since it reads as evidence.

  The plan names **every package and the depth**, and it is printed before the
  first build starts. "Three packages will be built" is not a plan anyone can
  decline in part, and the depth is the cost. One confirmation covers the run,
  not one per package: a prompt per package trains the operator to answer without
  reading. `--yes` buys past the prompt and never past the plan. The three gates
  are the sweep's — `--yes` proceeds unattended, an interactive terminal is
  asked, anything else builds nothing and **says `--yes` is the way through**,
  because a gate that cannot be passed is a dead end in exactly the CI and cron
  runs where it is reached. Interactivity requires **both** stdin and stdout to be
  a terminal, so `yes | bentoo overlay compare --realign --depth=compile` cannot
  answer for a human.

  What gets proposed is `::gentoo`'s own ebuild, verbatim, and only where the
  baseline is at **our own version** and the divergence is undeclared. A baseline
  at a different version is deliberately never proposed: adopting another
  version's file changes which version we ship, which is a bump and not a
  realignment. The rule reads the baseline and the byte comparison and nothing a
  model said, so `--no-review` and a machine with no model prove the same set.
  `--depth=none` and `--depth=options` are refused, because below `patches` the
  ladder runs no build gate and the resulting proof could never publish anything.

  Nothing is published. The staged tree goes where `overlay autoupdate --apply`
  and `overlay validate --depth` already stage — never inside the overlay, which
  auto-commits and pushes and whose `--clean` deletes any ebuild no registry pin
  claims. Declining is **exit 0**: nothing failed, a decision was taken.

- **A bump is now proved before it is published.** Story 031 shipped a gate that
  reads what upstream declares and what the ebuild passes, and reports the
  difference — but it could not stop the bump. Pointed at obentoo/bentoo#33 it
  names `aalib` and `libcaca` in about five seconds, and then the bump publishes
  anyway. The overlay this toolkit writes to auto-commits and pushes, so "we
  report it afterwards" and "it never reached anyone" are different promises.

  The candidate is now materialised in a staging tree of its own — a
  self-consistent single-package repository under `<configDir>/staging`, holding
  copies of the overlay's `eclass/`, `profiles/` and the package's `files/`, and
  mastering onto `gentoo` alone. Not onto the overlay: that name resolves through
  `repos.conf` to the *deployed* copy, which lags the working tree by however
  long the commit/push/sync cycle takes, and validating a new ebuild against a
  stale eclass is the same class of error as validating it against the wrong
  tarball. Only a bump whose gates all reported PASS or SKIPPED is copied into
  the published overlay, and only then is the registry pin written.

  Measured: a staged skeleton plus one package is 24 KB; `eclass/` is 32 KB and
  `profiles/` 56 KB. One tree per package and version, replaced on restaging, so
  there is no index to keep consistent and no lock to take.

- **Three build gates, from one `ebuild` invocation.** `ebuild <path> clean
  <phase>` runs setup, unpack, prepare and configure in a single pass, so the
  patches, configure and compile outcomes are derived from the phase markers in
  one captured log rather than by unpacking the same 6 MB tarball three times.
  A failure attributes itself to the last phase that started. This also settles
  what the patches gate is: `src_prepare` applies the patches the way the build
  will, in the ebuild's own order, through `eapply`/`eapply_user` — a strictly
  better test than the `patch --dry-run` story 031 deferred.

  Measured on the reference host: `1.29.2` exits 1 with
  `meson.build:1:0: ERROR: Unknown option: "aalib".`, `1.28.6` exits 0 with
  `>>> Source configured.`, and the whole unpack/prepare/configure cycle runs
  unprivileged for a user in the `portage` group.

- **A depth ladder, and a policy that picks a rung.** `none`, `options`,
  `patches`, `configure`, `compile`, each including every rung before it.
  Selected from how far the version moved — revision, patch, series, major —
  then lowered to `none` for a binary record, overridden per package, and
  replaced outright by `--depth` or `--compile`. `compile` is never a default:
  a default that started building on every major bump would be switched off in a
  week. An override that *lowers* the depth reports the bump as skipped by
  policy, never as validated; only an explicit operator flag buys less scrutiny
  quietly.

- **`--depth` on `overlay autoupdate` and on `overlay validate`**, and a
  `autoupdate.validate` block in `config.yaml` carrying the per-class depths,
  `require_isolation`, `require_proof`, `review`, `fix_on_failure`, `timeout`
  and per-package overrides. The policy lives in `config.yaml` rather than
  `packages.toml` on purpose: that file sits inside the overlay, auto-commits
  and publishes, and silently auto-disables a record whose syntax runs ahead of
  the installed binary. Here an unknown key is a warning and the run continues.

- **`--llm`, enabling a bump reviewer and a build fixer.** The reviewer reads
  the locally computed difference between the two versions' build declarations
  and reaches no network; it holds `Read` and nothing else, and emits at `info`
  or `warning` and never at `error` — so a model's opinion can raise validation
  depth but can never decide a gate. The build fixer holds exactly `Read` and
  `Edit`: no `Write`, because it only ever edits an ebuild that already exists,
  and no `Bash` in any form, because `--add-dir` bounds file writes but not a
  shell. After a repair the same phase is re-run and *that* outcome is the
  gate's — the agent's self-report is never the verdict.

- **`--check --llm` prints its plan before it spends anything**: how many
  packages, the depth for each and why, what is skipped and why, how many
  distfiles will be fetched, and the distribution of depths across the run. It
  then asks once for the whole run when any gate above `options` would run, and
  reports the proved, errored and skipped tallies at the end.

### Changed
- **Archive objects are stored at `<remote>/<subvolume>/<id>.zst`, and
  `snapshot restore` gained `--subvolume`.** The subvolume is now a directory
  under the remote instead of a fragment of the filename, which is what lets a
  prune scope itself by listing one path. The directory name uses the existing
  sanitize rule with no special case, so `/home` is `-home` and the root
  subvolume `/` is the directory `-`. Joining with `/` also removes an ambiguity
  the old `-` join had: `sanitize` emits `-` itself, so subvolume `/home` with id
  `otaku-42` and subvolume `/home/otaku` with id `42` produced the *same* key.

  Because each subvolume has its own directory, a restore must know which one to
  read. With **exactly one** subvolume configured it is inferred and nothing
  changes — that is the deployed configuration and it needs no editing. With
  **two or more**, `--subvolume` is required: without it the command exits
  non-zero naming the configured subvolumes, **before any subprocess runs**,
  rather than silently reading the wrong one. Naming an unconfigured subvolume
  fails the same way.

  The check applies whichever driver the named ship uses, including `restic`,
  which discards the value. That is deliberate: a gate conditioned on the driver
  is a gate a future driver has to remember to join, and forgetting it would
  restore the silent wrong-subvolume read this release removes. The practical
  effect is narrow — a `restic` restore on a config with two or more subvolumes
  now needs a flag it ignores, and the error names it.

- **`overlay compare` without `--realign` is unchanged** — the same output, the
  same exit code, the same package set, the same summary arithmetic. This is a
  shipped command and the regression was the risk, so the promise is mechanical
  rather than careful: the renderer never learns which flags were passed, every
  field this work adds renders nothing at its zero value, and the passes that
  fill them run only for a review run. `IncludeNotInRemote` is switched on only
  there — unconditionally it would give the 84 Bentoo-only packages rows they do
  not have today. A test rebuilds the expected report independently and compares
  whole renderings.

  `--realign` refuses what it cannot do rather than degrading: comparing against
  another repository while reading the baseline from `::gentoo`, or asking for
  the review where no local tree exists, are usage errors naming the reason —
  nothing was examined and the request itself was impossible. The exit code comes
  from the review's own outcome and never from the count of divergences, because
  an overlay of a derived distribution has divergences by definition and a code
  that counted them would be non-zero forever and ignored within a week. No
  baseline tree at all is the one non-zero condition.

- **BREAKING — `overlay validate --json` changes shape.** Per ebuild, the keys
  `options`, `qa`, `reason` and `findings` are gone. In their place: `gates`, an
  array of `{gate, outcome, reason?, findings?}`, plus `depth`,
  `depth_requested` and `depth_reason`. `package`, `version` and `sources` are
  unchanged. A `jq` expression written against the old keys will break.

  Two fixed outcome fields cannot carry five gates, and the single shared
  `reason` was already being overwritten: an option gate skipping for a missing
  distfile and a QA gate skipping for a missing `pkgcheck` rendered as one line,
  whichever was written last. More sharply, `ExitCode` filtered findings on the
  option gate — so an error from the configure gate, the gate that reproduces
  issue #33 by running the build's own configure step, was invisible to the exit
  code and the command exited 0. It now counts an error finding from any gate
  except `pkgcheck`'s, which stays advisory so a `metadata.xml` DOCTYPE typo
  cannot fail the whole tree.

- **BREAKING — the `pending.json` state machine `--list` renders has changed.**
  `validated` now sits *after* the static gates and means "passed the static
  gates" — not "ready to publish", and on the staged path no longer "the ebuild
  is in the overlay". A bump can sit at `validated` having failed a later gate
  and never been promoted, and the column has to be read that way.

### Fixed
- **An archive prune no longer deletes another subvolume's backups.** With two
  or more subvolumes shipping to one rclone remote, every successful ship
  destroyed part of another subvolume's history — not rarely, not under a race,
  but on every run, by construction.

  The objects lived in one flat namespace: `<remote>/<subvolume>-<id>.zst`. The
  retention policy buckets objects by calendar period and keeps the newest in
  each bucket, so objects from *different* subvolumes landed in the *same*
  bucket and competed for its single slot. Shipping `/root` deleted `/home`'s
  backup. Nothing reported a problem: the `deletefile` succeeded, so the run was
  green; the next `/home` ship still worked, because `btrfs send -p` references
  the parent's local path and that snapshot is still on disk. The damage
  surfaced only at restore, arbitrarily later, when the chain's base was gone —
  and incremental is the default mode, so this was the default path.

  A prune now lists **one subvolume's directory** and can therefore only see, and
  only delete, that subvolume's own objects. The protection is structural rather
  than a check: other subvolumes' backups are not spared by a guard, they are not
  candidates at all. `bentoo snapshot prune` applies the policy independently to
  each configured subvolume, reading every subvolume's lineage head **before**
  deleting anything, so a failure to read one leaves the remote untouched rather
  than half-pruned. A subvolume nothing has been shipped for yet is skipped with
  a warning naming it, which is the ordinary first-run state and not a failure.
  A directory entry is never passed to `rclone deletefile`.

  **No migration is needed and none is offered.** No archive ship is enabled in
  any deployed configuration, so no object exists under the old layout. If one
  did, it would simply be left alone: a prune only lists configured subvolumes'
  own directories, so anything outside that layout is never a deletion candidate.

- **A staged file whose final flush fails is now reported instead of copied
  half-way.** `copyRegularFile` closed the destination twice: once explicitly
  with the error handled, once through a deferred `Close` that discarded it. The
  handled close made the behaviour correct, but only on the path where `io.Copy`
  had already succeeded — and the duplicate meant the deferred close could only
  ever return `ErrClosed`, so it verified nothing. It now closes once, from the
  defer, and promotes the close error to the function's result when the copy
  itself succeeded. A write is not durable until the handle closes, so this is
  the last point at which a truncated file in the staged tree can be noticed
  rather than handed to a build gate as if it were complete. Earlier errors keep
  priority, because they name the actual cause. Also clears the CodeQL
  `go/unhandled-writable-file-close` alert, whose pattern the previous shape
  tripped even though the explicit close made it safe.

- **The baseline is now the `::gentoo` version genuinely nearest ours, not the
  one whose digits happen to match.** `versionDistance` summed each version
  component's absolute difference, weighted by significance. That measures two
  component *vectors* apart rather than two versions: as soon as a
  higher-significance component differs, the lower digits get compared across a
  boundary they do not span. From `1.5.1` it read `1.4.2` as nearer than
  `1.4.3`, although `1.4.3` is the later release and therefore fewer releases
  back. Replayed over the 158 packages this overlay carries at a version
  differing from `::gentoo`'s, that metric picked a baseline which was **not the
  nearest version in eleven cases**; the corrected one picks it in zero.
  `dev-util/nvidia-cuda-toolkit` is where it was furthest wrong: for ours at
  `13.3.1` it chose `12.3.2` over `12.9.2`, because the minor digit `3` matched
  ours exactly across two different major lines — and a digit that matches
  across major lines says nothing at all about proximity. A version is now read
  as a number on the significance ladder that already existed, and the distance
  is the difference of two such numbers. The ladder, the weights and the unit
  are unchanged, because the reduction's width bound compares a span against a
  baseline distance and two numbers on different ladders are not comparable.
  Whichever baseline was too far back, every release between it and the real
  one had its changes attributed to this overlay.

- **A reduction now either subtracts something or says it did not.** The
  three-way reduction was called with no third point at all, so it never
  reduced: every difference from the baseline came back unclassified and the
  whole of it was reported as ours. The third point it now uses is **our own
  previous version**, which exists for **86 of those 158 packages** and passes
  the width bound in all 86 — our own consecutive versions are close together by
  construction, while the baseline is however far `::gentoo` has fallen behind.
  Where our previous version still agreed with the baseline, what our bump
  changed there is recognised as version noise; where it had already diverged,
  the difference stays ours, which is the deliberate work the reduction exists
  to leave behind. A `::gentoo` version above the baseline is preferred where
  one exists, and is still refused when it spans further than the baseline is
  from us. Separately, the report no longer prints `reduced against a third
  point spanning N release steps` over a run in which nothing was attributed: a
  third point that was accepted and explained nothing now says exactly that. A
  confident sentence about work that did not happen is worse than no sentence.

- **`overlay compare` without `--realign` is unchanged by both fixes** — same
  output, byte for byte, and the same exit code. The pass these changes live in
  runs only for a review run.

- **`overlay validate --depth` now honours `require_isolation`.** This closes a
  policy bypass rather than adding an option.
  `autoupdate.validate.require_isolation` is read by the *same* build gates
  under `overlay autoupdate`, and until this story those gates were unreachable
  from `overlay validate` — every one of them SKIPPED, so nothing this command
  did could ever be unisolated. Wiring the seams made them run while the
  `BuildRequest` kept leaving the field at its zero value, so an operator's
  decision that builds must be isolated silently stopped applying to one of the
  two commands that build. `validate.Options` carries the setting now, and the
  command reads the same key from the same config.

- **`overlay validate --help` no longer promises a read-only run at every
  depth.** "Nothing is built, downloaded or changed" was *accurate* on `main`:
  every build gate reachable from this entry point reported SKIPPED, so whatever
  `--depth` said, nothing was ever built. Wiring the seams made those gates real
  — `--depth=configure` now stages each candidate and runs upstream's own build
  phases against it, compiling code and fetching any distfile this host does not
  hold, across every package the selector matches. The published overlay is
  still never written to, but "read-only" stopped being true of the *host*, and
  a command whose own help denies that is how someone starts a whole-overlay
  build by accident. Both the help and the file header now qualify the promise
  by depth.

- **An interrupted build is no longer reported as one that could not be
  started.** `buildDepthGates` funnelled every `RunBuildGates` error into "the
  build gates for X could not be started" — accurate when that function errored
  about the *request* and never about the build, and false since the interrupt
  guard began returning the cancellation the same way. The package the Ctrl-C
  actually landed on was described as never having begun, while every *later*
  package in the same sweep got the correct wording from `interruptedResult`:
  one report, two accounts of one event.

- **The two DIST-record parsers now agree on what a DIST line is.**
  `ManifestDistLines` matched `HasPrefix("DIST ")` and
  `ParseManifestDistFilenames` splits fields, so a tab-separated or indented
  record was a distfile to one and invisible to the other. They read the same
  file for the same run — one decides which archives the option gate looks for,
  the other which records the staged Manifest carries — so the disagreement
  produced a report that proved and denied the same file at once. Both use the
  field split now; the record's bytes still travel untouched.

- **An interrupted run no longer publishes the bump one run later.** Blocking
  the publish inside the interrupted run was not enough, because one thing it
  produces outlives it: the `StageRecord` written beside the retained tree. Its
  gates were never asked and therefore report SKIPPED, `Proves()` accepts any
  PASS-or-SKIPPED list at the requested depth, and the next `--apply` takes the
  R10.1 reuse path — which consults neither `refuseUnproved` nor
  `PromotionDecision` — under a context that is not cancelled. Ctrl-C published,
  just a run later and by a quieter route.

  A cancelled context now writes no record at all. Nothing else changes and
  nothing is lost: R10.5 already revalidates a retained tree carrying no
  readable record, because absence of a claim is not a passing claim, and the
  tree itself still stays on disk as the failure's evidence.

- **A mid-sweep interrupt no longer drops the packages it never reached.**
  `Run` has two cancellation checks, and only the one *before* a package listed
  the remaining targets as interrupted. The one *after* a package returns first
  on every real Ctrl-C, so the rule the first states — a package in view is
  never left unmentioned — held only for a run cancelled before its very first
  package. Every unexamined package silently vanished from the report. Both
  branches now list them.

- **An interrupted `overlay validate` now prints the partial report it already
  assembled, and exits 130 instead of 2.** `Run` does not merely fail when a
  sweep is stopped: it appends one `interruptedResult` per package it never
  reached, because its governing rule is that a package in view is never left
  unmentioned. The command took the plain `err != nil` branch and threw all of
  that away — under `--json` it emitted **no document at all**, so a stopped run
  and a run that produced nothing looked identical to the `| jq` the flag exists
  for.

  The exit code was the second half. `2` is what `Report.ExitCode` documents for
  "the selector matched nothing", so an operator's Ctrl-C and a mistyped package
  name were the same event at the shell unless someone parsed the diagnostic
  text. An interruption now answers 130 — 128 + SIGINT, the shell's own
  convention — on both the text and the `--json` path.

- **The test pinning "`--depth` executes the build gates" no longer passes
  without executing them.** It stripped `PATH` to stay hermetic, but the
  dependency pre-check runs in *front* of `RunBuildGates` and needs `emerge`, so
  every gate came back SKIPPED carrying the pre-check's "could not be
  determined" — and the assertion, which only checked that the *old deferral
  sentence* was absent, was satisfied by that list. The execution half of R2.2
  was unpinned for the whole story.

  The fixture now puts a stub `emerge` on `PATH` and leaves `ebuild` off it, so
  the run gets past the pre-check and `ebuild`'s absence becomes
  `RunBuildGates`' own answer — a sentence produced at exactly one place in the
  package, which is what makes it usable as proof of arrival. Both directions
  are asserted, since the negative alone is what let this pass before.

- **An interrupt can no longer publish a bump, by any route.** This guard has
  now been written three times, and the first two guarded a *verdict*: one
  inside `RunBuildGates`, one around `validate.Run`'s return. Each time, the
  next review found a route into promotion that did not pass through the one
  that had been guarded.

  Three were open. `runStaticGates` turns any `validate.Run` error — `ctx.Err()`
  included — into a single SKIPPED option gate, and `PromotionDecision` promotes
  on SKIPPED. `DependenciesSatisfied` runs *in front of* `RunBuildGates` and
  turns a cancelled probe into `SkippedGates` with a **nil** error, so the apply
  never sees a failure at all. And R10.1's reuse path calls `promote()` without
  consulting `PromotionDecision` in the first place.

  The invariant now sits at the WRITE rather than at the verdict.
  `refuseOnInterrupt` is the first statement of `promote()` — the one function
  that writes into the published overlay — and both call sites pass through it,
  so a future route that manufactures a promotable gate list out of a
  cancellation is merely wrong rather than publishing. A second check in `Apply`
  keeps the refusal honest: without it `refuseUnproved` answers first, and
  "proof at depth configure is required" sends the operator to change a policy
  that had nothing to do with their Ctrl-C.

  Proved by mutation rather than by a green run. With the guard neutralised all
  four new tests fail; with only the `promote()` check removed the reuse test
  fails alone; with only the early check removed the message test fails alone.
  That battery was worth running: the first draft of the static-gate test passed
  with the guard dead, because at compile depth it was reaching the older
  `RunBuildGates` guard instead. It is now pinned to depth `options`, where no
  build gate runs at all.

- **A failed build gate now quotes the cause instead of Portage's epilogue.**
  `failureExcerpt` quoted the last 12 non-empty lines of a failing phase, on the
  reasoning that the error is at the end. It is not. Portage's `die` prints its
  call stack, the offending snippet, the support boilerplate and four `located
  at '…'` lines *after* the error — measured here, 16 lines of epilogue
  following the one line that mattered. So every FAILED build gate this project
  ever reported quoted 12 lines of boilerplate and none of the cause.

  The window now ends at `die`'s banner and appends that banner, which is also
  the line the staging-repository name is scrubbed out of, **plus the message
  `die` was called with**, which Portage prints immediately after the banner.
  That last part was a second measurement: ending at the banner was still one
  line short, and for the common `emake || die "emake failed"` shape — a phase
  that produces no diagnostic of its own — die's message is the entire cause. An
  excerpt ending at the banner collapsed to a line that only repeats what the
  gate's reason already says. A transcript with no banner keeps exactly the old
  behaviour.

  The hermetic test that should have caught the first half was passing
  vacuously: its fixture carried three non-empty lines after the last phase
  marker where a real failure carries twenty-four, so the truncation never
  engaged. The fixture now carries `die`'s real epilogue, and with the old
  tail-quoting restored it fails with precisely the production symptom. A second
  fixture covers the bare-`die` shape, which the first cannot: there, `meson`
  happens to print a diagnostic before dying, so the loss was survivable.

- **A missing build dependency no longer reports as a broken ebuild.**
  `overlay validate --depth` drove `ebuild … clean <phase>` with no dependency
  pre-check. `ebuild` does no dependency resolution, so on a host lacking a
  `DEPEND` atom the phase started, died on the missing header, and the gate
  reported FAILED with error findings and exit 1 — blaming the candidate for
  something only that machine was missing. The autoupdate applier has answered
  this since story 031 and reports SKIPPED naming the atoms to install, so the
  same host gave opposite verdicts for the same package depending on which
  command asked. Both now call `DependenciesSatisfied` and say the same
  sentences.

- **An interrupted run is an error, not a verdict and not a skip.** The
  per-package loop never checked for cancellation, so a Ctrl-C during a
  whole-overlay `--depth=compile` kept staging trees and spawning `ebuild` for
  every remaining package. The package *already building* fared worse: the child
  is spawned through `CommandContext`, so the interrupt killed it, the phase
  counted as started-and-failed, and the gate reported FAILED with error
  findings — telling the operator their ebuild is broken because they pressed
  Ctrl-C.

  Reporting those gates as SKIPPED instead would have been **worse than the bug**:
  `PromotionDecision` promotes on a list of PASS-or-SKIPPED, so an interrupted
  `overlay autoupdate --apply --depth=compile` would have *published* the bump —
  into an overlay that auto-commits and pushes — on gates that were killed
  mid-build. A gate list cannot express "nothing was measured", because every
  value it can hold is a statement about the candidate.

  So `RunBuildGates` returns an error, wrapping the context cause. All three
  drivers already treat that as "could not be attempted": the applier fails the
  apply rather than promoting, realign's `Prove` returns without a verdict, and
  `Run` aborts the sweep. `Run` returns an error too, because `ExitCode` reads
  error-severity findings and an interrupted gate has none — a SIGTERM'd sweep
  would otherwise render as SKIPPED lines and exit 0, indistinguishable at the
  shell from a clean pass. Remaining packages are still listed, so the partial
  report names what went unexamined, and the partial transcript is still
  retained.

- **A staged tree is now thin, so Portage will build in it.** Staging carried
  `thin-manifests` from the published overlay, absence included — and Portage's
  default is `thin=false`. On a non-thin repository `digestcheck` goes past its
  early return and requires every `.ebuild` in the package directory to carry an
  `EBUILD` record, returning failure under `strict`, which is in the default
  `FEATURES`. A staged tree's Manifest describes distfiles and nothing else by
  construction, so the candidate had no `EBUILD` record and the tree was refused
  before the first phase marker: every build gate SKIPPED, for a package that
  would have built. `Stage` now imposes `thin-manifests = true`. It is the one
  place the "validate under the published tree's own rules" principle is
  deliberately broken, and the justification is that a staged tree holds one
  ebuild and no repository files — the non-thin checks exist to catch an
  undigested file added to a real package directory, and nothing can be added to
  this one. DIST digests are still verified against the archive on disk.

- **A staged Manifest no longer describes files that are not there.**
  `publishedManifestBytes` copied the published Manifest verbatim. A Manifest is
  DIST-only just when the repository sets `thin-manifests = true`; Portage's
  default is `thin=false`, and the file then also carries `EBUILD`, `AUX` and
  `MISC` records. `Stage` copies the candidate ebuild and the package's
  `files/` — not `metadata.xml`, not the sibling ebuilds — so those records name
  files the staged tree does not have, `digestcheck` raises `FileNotFound`, and
  `ebuild` dies before the first phase marker: every build gate SKIPPED for a
  package that would have built. Invisible on `::bentoo`, which sets
  thin-manifests, and broken on any overlay that does not — and `--overlay`
  accepts any path. The retired `manifestDistLines` filtered for exactly this
  reason; dropping the filter along with the helper was the regression. Each
  surviving DIST line still travels byte-for-byte, because Portage verifies
  those digests against the archive on disk.

- **A SKIP no longer denies the file it was read from.** When distfile names
  came through the caller seam, every refusal appended "the names searched for
  were supplied by the caller, not read from a Manifest". True of the `validate`
  package, false of the operator's world: `overlay validate` supplies names it
  read out of the published Manifest, so the sentence denied the existence of
  the file that had just been read. It now says the list arrived from outside
  and stops there.

- **A baseline review could be a silent no-op.** The `::gentoo` tree was located
  only by walking the packages in view, so a run whose packages `::gentoo` does
  not carry — `--realign --only-outdated`, say — found no tree, wrote nothing
  and exited 0 over a repository that was synced and perfectly readable. Which
  packages the operator asked to *see* decided whether the repository got
  *read*. It now falls back to the provider's own tree, recognised by Portage's
  `profiles/repo_name` rather than trusted, and reports SKIPPED when there is
  genuinely none.

- **A commit-tracked bump no longer fails on `COMMIT="<sha>"`, and a failed
  substitution no longer leaves an ebuild behind.** Two defects, one incident.
  The commit-hash rewrite matched four variable names across two patterns, and
  `COMMIT` appeared only in the unquoted one — written from the single package
  that omits the quotes. `sys-apps/asus-ec-sensors`, which pins
  `COMMIT="<sha>"`, therefore fell between the patterns and was told its commit
  variable did not exist.

  That failure lands *after* the new ebuild has been copied into the published
  package directory, and the orphan rollback was returned to the caller rather
  than run — so a substitution failure returned an error with no rollback to
  arm, and the copy stayed. Since this overlay auto-commits and pushes, the
  leftover was published: `asus-ec-sensors-0_p20260809` carrying the July
  commit, with no `Manifest` entry and no `md5-cache`, so any `emerge` of it
  fails on the digest. The rollback now runs where the failure happens.

- **The static gate now reads the archive the run just fetched, instead of a
  directory that may never have held it.** A bump could be published *unread* on
  any host that had not already downloaded the release — a fresh CI runner, a new
  machine, or simply the first time a package was bumped. Reproduced through the
  real `Apply` pipeline with one variable changed: whether the shared `DISTDIR`
  already held `gst-plugins-good-1.29.2.tar.gz`. With it, the gate names `aalib`
  and `libcaca` and the bump is refused. Without it, `RESULT success=true
  promoted=true` — obentoo/bentoo#33 goes out to an overlay that auto-commits and
  pushes.

  Two correct behaviours composed into a wrong one, which is why it survived
  review. The manifest step fetched into a private distdir and removed it on the
  way out — the cleanup of the very directory that stops a staged Manifest from
  making quarantine move the host's real distfiles aside. The static gates then
  read the *shared* distdir, where the only archive present was the previous
  release's; the gate declined it as belonging to another version, which is the
  right answer to the question it was asked, and reported SKIPPED. Promotion
  requires every gate to report "PASS **or** SKIPPED" — the rule that lets a host
  unable to run a build gate still publish a bump whose static gates passed.
  Neither rule is wrong alone.

  The fetched directory now travels with the bump it belongs to, on
  `candidatePaths` rather than on the applier, because applies run concurrently
  and a shared field would hand one package's gate another package's archive. It
  is removed when the whole staged sequence ends, on every path including
  failure, so a sweep over forty packages leaks nothing. An *empty* private
  directory falls back to the shared one: it is created before the manifest step
  runs, so preferring it while empty would hand the gate the same empty room.
  `--check` gets the identical lifetime — it publishes nothing, which makes its
  failure mode quieter rather than smaller: a plan that reports "proved" for a
  bump nothing read.

  The host `DISTDIR` invariant is unchanged and is now asserted against a run
  that actually downloaded, rather than only against runs that fetched nothing.

- **A skipped gate now names the directory it searched.** "No archive here" and
  "I looked in the wrong place" produced the same message, which is why the
  defect above read as a wrong-version refusal for as long as it did. The
  wrong-version wording itself is unchanged.

- **A gate no longer reads an archive belonging to another version.** Observed,
  not hypothetical: with only `gst-plugins-good-1.29.2.tar.xz` on disk, the
  1.28.6 ebuild was validated against the 1.29.2 archive and reported FAILED,
  naming options that 1.28.6 does declare. The single-present shortcut now
  survives only where it is safe — when the present name carries this ebuild's
  version, or when no distfile the Manifest names carries any version at all,
  which is the snapshot and commit-hash case the shortcut exists to serve.
  Otherwise the gate declines and names the archive it refused.

  The version match strips the `-rN` revision first: Gentoo's revision counts
  rebuilds of the *same* upstream tarball and never appears in a distfile name,
  so a literal reading would have made every revbumped ebuild in the overlay
  start declining — trading an observed false FAILED for a silent false SKIP.

- **A validation run no longer rearranges the host's distfiles cache.** The
  manifest step wraps `pkgdev manifest` in a quarantine whose contract is "a
  distfile present under a name the current Manifest does not list cannot be
  verified, so move it aside". Pointed at a staged tree whose Manifest does not
  yet name the new distfile, the names it moved aside were the host's real
  distfiles — and a quarantine failure is fatal by contract. The staged manifest
  now runs against a private distdir, seeded through the existing cache path so
  nothing is fetched twice, and the host `DISTDIR` is read and never written.

- **`snapshot apply` no longer removes a `.snapshots` that is a mount point.**
  Provisioning a snapper config clears a leftover `.snapshots` when it is empty,
  because `create-config` refuses to run while one exists and offers no way to
  skip creating its own. "Empty" was decided by listing the directory — and a
  separately mounted `@snapshots`, the arrangement `snapper` itself suggests,
  lists as empty whenever it is not mounted. The removal destroyed no snapshot
  (those live in the real subvolume) but took the mount point with it, so the
  next `mount` failed on a path that no longer existed.

  The removal is now refused for any path named in `/proc/self/mounts` or
  `/etc/fstab` — what is mounted now, and what the operator declared — and the
  error names the table it was found in, so the fix is visible from the message
  rather than inferred. Both tables are consulted because they answer different
  halves of the question: an unmounted entry appears only in `fstab`, and that
  is exactly the case a content listing cannot tell apart from a stale leftover.
  An unreadable table is skipped rather than fatal: the check exists to refuse a
  destructive removal, not to require a host to own an `fstab`.

## [0.23.0] - 2026-08-08

### Added
- **`overlay validate`: a read-only gate that asks whether an ebuild still
  matches the source it points at.** `overlay autoupdate --apply` proves the
  tarball hashes, and with `--compile` that the package builds. Between those
  sits the failure class that produced obentoo/bentoo#33: the version moved, the
  ebuild did not, and nothing asked whether the ebuild still fit. Upstream
  removed `aalib` and `libcaca` from `gst-plugins-qt6` at 1.29; the ebuild kept
  passing `-Daalib=` and `-Dlibcaca=`, and every check stayed green.

  The gate reads the build options the upstream archive declares, reads the ones
  the ebuild passes, and subtracts. No build, no privilege, no network, no
  model: the archive is already on disk, put there by the manifest step. The
  archive is never unpacked either — `tar -xO` sends the members it parses to
  stdout, which makes archive path traversal structurally impossible rather than
  something a check has to catch, and leaves the overlay byte-identical by
  construction. `--json` writes the whole report as one document, and exit codes
  are 0 clean / 1 an error finding / 2 a selector the overlay does not hold.

  **An outcome names its own reach.** A gate that could not run says SKIPPED and
  why, never a silent pass — a missing distfile, a build system that is not
  Meson, an unreadable ebuild. That is the whole design, and the measurement
  below is why it matters more than the findings.

  **Measured across the whole overlay — 407 ebuild versions, on a host whose
  DISTDIR holds few of their distfiles.** The gate read both sides for **4** of
  them and **403 reported SKIPPED with a reason**: 255 because the distfile is
  not on this host, 69 because the build system is not Meson (cmake, make,
  cargo, automake — each named), 47 because `tar` cannot read the archive at all
  (`.deb`, plain `.gz`, AppImage), 19 because several distfiles are present and
  none uniquely matches the version, and 13 because the Manifest names no
  distfile. **Unresolvable option names: 0 of the 4 the gate reached, and 0 of
  the 407 it examined.** The share is stated with both denominators because
  neither alone is honest — the interesting number here is not the zero, it is
  that this gate's reach is a function of what DISTDIR happens to hold.

  Two false-positive classes were found by building the gate and pointing it at
  the real tree, and both are worth recording because each would have got the
  gate switched off on its first run:

  - A package directory's Manifest names every version's distfile, so picking
    the first present one validated the 1.29.2 ebuild against the **1.28.6**
    archive — where the removed options are still declared — and reported a PASS
    for exactly the bump this exists to reject. The distfile is now chosen by
    the version, and an ambiguous match is a SKIPPED rather than a guess.
  - `meson.build` had to be at the archive's project ROOT, not merely somewhere
    inside it. WebKit and Node both vendor third-party code that uses Meson; the
    gate adopted a vendored subproject as the project root and compared their
    CMake and GYP `-D` options against it. That produced **89 false errors** on
    `net-libs/webkit-gtk` and one unearned PASS on `net-libs/nodejs`. All three
    are now SKIPPED naming the build system actually found.

  `pkgcheck` findings ride in the same report and never touch the exit code: the
  overlay carries pre-existing QA findings unrelated to any bump, and letting
  them decide the status would fail the whole tree. They are all carried at
  `info`, because across 95 captured records the JsonStream reporter emits **no
  level field at all** — inferring one from the message text would be a guess
  wearing a severity's clothes. Capturing those records also explained why the
  design could not observe one: pkgcheck's GitAddon raises on this overlay's
  history, prints a traceback to stderr, writes nothing to stdout and exits 0,
  so a package with findings looked clean. The scan now runs with
  `--cache=-git`.

- **`overlay autoupdate --check` now fetches each upstream URL once per run.**
  A full check issued **425 HTTP requests to fetch 230 distinct URLs**. The 195
  surplus were not a rounding error: 170 of them asked `gitlab.freedesktop.org`
  for the *same* GStreamer tag listing, once per plugin package, and the
  per-host rate limiter — one request every 6 seconds — turned that into
  **18.4 minutes of queue for a single 33.7 KB document**.

  Each distinct URL is now fetched once and the same body handed to every entry
  that asked for it. No registry field is added, no record is edited, and no
  entry's meaning changes.

  Two reads share a fetch only when the requests they would issue are
  byte-identical: the identity is the URL about to be requested plus a digest of
  the headers the record declared, joined by a NUL byte because no URL can
  contain one and therefore none can spell the boundary itself. Range is the
  sharp edge — a record asking for a window near the front of a large file must
  never be handed the whole file, or another record's window — so a differing
  header set is a different identity and gets its own request.

  Getting this wrong in the collapsing direction would not be a lost
  optimisation, it would be one entry silently receiving another entry's bytes.
  So the guarantee is that no entry's outcome depends on another's: a failure,
  a cancellation or a memory bound reached changes when a request is issued,
  never what a record concludes. `--no-fetch-cache` turns the sharing off and
  reproduces the old un-deduplicated run, which is what makes a suspicious
  version bisectable — it separates a real upstream change from a sharing bug.

### Fixed
- **A compile-gate pass now states the isolation it actually verified.** The
  gate printed a plain green whenever the build succeeded, and could not have
  done otherwise: Portage reports `network-sandbox` in FEATURES, creating the
  namespace needs privilege an ordinary user does not have, and Portage warns
  about neither. A green therefore meant either "built with the network cut off"
  or "built with full network access", and nothing printed told them apart.
  Measured on the development host as euid 1000: `unshare --net true` fails with
  EPERM, so every pass there was claiming more than it had.

  `ProbeIsolation` measures instead of inferring, because inferring is the
  defect. It starts a short-lived child with the namespace requested at fork
  time and reads the kernel's answer — `clone(CLONE_NEWNET)` without
  `CAP_SYS_ADMIN` fails before the child exists. Unsharing in-process was
  rejected: Go multiplexes goroutines onto OS threads and cannot reliably retire
  one left in a foreign namespace. A non-Linux fallback reports `undetermined`,
  and undetermined is treated exactly like a denial — a probe that could not run
  has proved nothing.

  A success without a verified namespace now renders `PASS (unverified
  isolation)` followed by the reason, and the new `--require-isolation` flag
  leaves the compile unrun rather than producing a green worth nothing. Nothing
  else moved: the confirmation prompt, the `sudo`/`doas` requirement and both
  existing success and failure conditions are unchanged, and the pre-existing
  compile-gate tests pass untouched.

## [0.22.0] - 2026-08-08

### Fixed
- **`overlay prune --include-patched` no longer discards work the content proves
  is ours.** The flag says "I accept discarding local work". It cannot mean that
  about work the report has just named the file for: a patch our ebuild applies
  and ::gentoo does not ship for that package exists in no other tree, so
  ::gentoo cannot restore it — it never had it. Such a package is now refused
  outright, and the refusal names the proving file.

  Measured on the live overlay, plan only: the removal set goes from 74 packages
  to 71. The three that leave it are `net-libs/nodejs`, `kde-plasma/spectacle`
  and `kde-plasma/kdeplasma-addons`; nothing joins it. The identical batch — 66
  packages, the safe-removal path — is byte-identical before and after, and the
  five unproved divergences keep their class, reason and inventory exactly.

  The planner asks the authorship question itself rather than expecting the
  answer on its input. `overlay compare` annotates its finished report, but
  `overlay prune` calls the comparison and nothing else, so every result
  arriving at the planner carried "unproved" — and all three proved packages
  were being planned for removal under the flag. It calls the same function the
  compare path calls: two notions of what proves authorship would let `compare`
  name a file as ours while `prune` deleted it.

  The refusal requires a divergence, which is load-bearing rather than
  defensive. An identical package holds nothing to attribute, and a stale
  `${FILESDIR}` reference in an ebuild both trees share would otherwise refuse
  it — 66 of the 74 packages the plan would remove are in that batch, so a
  refusal leaking there stops `prune` removing anything at all.

### Added
- **A divergence is now reported as proved ours where the content proves it, and
  the file that proves it is named.** Two ebuilds are symmetric and say nothing
  about who caused a difference; the `files/` tree beside them sometimes does.
  An ebuild referencing a `${FILESDIR}` file ::gentoo does not ship carries
  something upstream never had.

  Measured on the live overlay — 318 packages scanned, 8 verified as differing —
  `net-libs/nodejs`, `kde-plasma/spectacle` and `kde-plasma/kdeplasma-addons`
  come back proved, each naming its patch; the five remaining `+1/-1` packages
  stay unproved. `nodejs` is the case that justifies checking each filename
  rather than asking whether upstream ships *a* paxmarking patch: ::gentoo has
  them for 20.6.0, 22.12.0 and 24.1.0, but not for 26.5.1.

  Unproved is never a finding that the change is ::gentoo's, only that the
  report cannot tell — so the section caveat now prints only where an unproved
  finding exists. A caveat printed over a divergence the content settled is
  false about the finding it sits under, and teaches the operator to discount
  both.

  The resolver refuses what it cannot resolve rather than guessing. Globs and
  brace expansions name a set, `..` escapes leave the directory, unknown
  variables need the ebuild's environment, and a commented-out reference is not
  a reference. Each of those, taken literally, is a filename no repository has —
  and the miss would be reported as proof. Likewise only a genuine "does not
  exist" counts as absence: reading a permission error as "upstream lacks it"
  would manufacture a proof out of a failure to look.

- **The redundant section separates what is safe to remove from what is not.**
  It recommended removing 74 packages and warned, beneath the table, that 8 of
  them differ in content. Both statements were true and neither was actionable:
  the recommendation did not say which 8 to skip, so following it discarded work
  and doubting it discarded the advice.

  It now renders as two tables — 66 packages under the recommendation and 8
  under a heading that recommends nothing and says what is unresolved. The
  cleared group states its evidence and names `bentoo overlay prune` as the
  command that acts on it safely, saying what that command decides on: every
  version the two trees share plus the whole `files/` tree, never the verdict
  alone. Without a content check the section stays one undivided table and says
  that nothing was checked.

  A package no content check reached joins the second group, never the first:
  listing an unchecked package under a removal recommendation has the report
  vouch for a check that never ran.

- **The summary says which packages the verdict counts are counted over.** It
  reported 231 `keep` against 155 `keep` rows on screen and explained neither. A
  count larger than what is printed reads as a defect unless the report states
  its universe, so the operator either distrusts the number or hunts for missing
  rows. The summary now states that the counts cover every scanned package and
  how many have no row in any table, as a relation that can be checked against
  the tables above it. The three pre-existing summary lines are untouched.

- **A model now explains each undeclared divergence, without being allowed to
  decide anything.** The report could say a divergence exists and how large it
  is, but not what it *does* — and "read both ebuilds yourself" is the work the
  report exists to avoid. A local model (the `claude` CLI, on your own
  subscription) reads the two ebuilds and prints, beneath the finding, which side
  the difference came from and a one-line summary of what it does. Where it
  reads the divergence as ours, it also proposes the text for a `patched`
  declaration. Nothing writes it: the proposal is text on a terminal, and
  applying it stays a decision you make.

  Measured on the live overlay: 10 undeclared divergences, 10 notes — 4 read as
  ::gentoo's, 3 as ours, 3 as both sides having moved.

  That the model is commentary rather than a verdict is not a disclaimer, it is
  observable. It reads `spectacle`, `binutils` and `binutils-libs` as ours; the
  content proof from the `files/` tree names `spectacle`, `nodejs` and
  `kdeplasma-addons`. The two agree on exactly one package out of six. Had the
  classification been wired to a decision, it would have moved `nodejs` — 622
  added lines of slotting work — out of the proved group on nothing but a
  reading, and moved two packages into it that no file proves. So it annotates
  and the report decides, and that separation is now an executed test rather
  than an intention: the same fixture rendered with and without a reviewer
  yields byte-identical tables, verdict counters and removal recommendations,
  with the difference confined to commentary lines. Five deliberate mutations
  confirm the test bites, including one — a counter moved by the review pass —
  that the rendered output alone cannot see, because the summary counts are
  printed outside the report.

  Every failure costs nothing. No CLI, `--no-review`, an error, a timeout or
  unparseable output all reach the same nil-reviewer path and print exactly
  today's report, with at most one warning. `--no-review` contacts no model at
  all — measured at 0.08s against 43s for the reviewed run. Classifications are
  cached under the two ebuilds' content hashes with no expiry, because the key
  *is* the content: when either file changes the key changes and the old entry
  becomes unreachable, so an expiry could only discard a still-correct answer.

  `internal/overlay` still imports no `internal/autoupdate` symbol. The package
  declares its own narrow `DivergenceReviewer` interface and `cmd/bentoo` builds
  the adapter over the CLI client — the one new import edge, in the one place
  that already imports both halves. Model-produced text is passed as an argument
  everywhere it is printed, never as a format string.

### Changed
- **An undeclared divergence now says how large it is, and stops implying who
  caused it.** The finding read `our 6.7.4 ebuild differs from ::gentoo's, yet no
  entry declares why` — symmetric in fact, an accusation in effect. It is only
  half the truth: ::gentoo revises ebuilds *in place*, under the same version and
  with no revbump, so a copy taken last week can differ today without anyone here
  having touched it. The version comparison sees two equal version strings and
  cannot notice, and direction is not recoverable from two files.

  Measured on the live overlay, the eight findings were not one thing but two.
  Four were a single line — `PYTHON_COMPAT` revised upstream on `breeze-gtk`,
  `drkonqi`, `kwin` and `plasma-firewall` — where our copy had simply fallen
  behind. One was `net-libs/nodejs`, hundreds of lines of slotting work of our
  own. Both printed the identical sentence.

  Each finding now carries the size of the difference — `undeclared divergence
  (+1/-1)` against `(+622/-254)` — which separates the two at a glance, and the
  section prints one caveat naming the ambiguity the counts cannot resolve. The
  failure this prevents is declaring `patched` on a package that carries nothing
  of ours: a declaration is what suppresses a removal recommendation permanently,
  so a false one is not a harmless note.

  The counts come from `udiff.Lines`, already this repository's diff — no new
  dependency, and no second notion of what a line difference is. They match
  `diff`'s orientation but not always its magnitude: `lcs.DiffLines` stops
  searching for a minimal edit script after 100 diffs, so a large divergence
  reports a larger count than GNU diff would. That is acceptable for a number
  whose only question is "one line, or hundreds?", and nothing downstream
  computes on it. No verdict changed, and no removal criterion moved.

## [0.21.0] - 2026-08-07

### Added
- **`overlay prune` acts on the redundant verdict — but never on the verdict
  alone.** `overlay compare` calls 74 packages `redundant` and stops there by
  design: the verdict is advice, and the report removes nothing. Acting on it
  meant 74 `rm -rf` calls by hand, in a repository that auto-commits and pushes
  within minutes, each one also leaving a registry entry the next check would
  silently disable.

  The new command supplies the tool, and reverses the precedence for its own
  decision only. A verdict is a statement about *versions* — "::gentoo ships the
  same or more". Whether deleting our copy loses anything is a statement about
  *content*, and only a content comparison can make it. Measured against the
  live overlay on 2026-08-05: of those 74 packages, **8 carry real local changes
  nobody declared**, `kwin`, `plasma-desktop` and `nodejs` among them. A prune
  driven by the verdict alone deletes work. So the verdict only selects which
  packages are worth comparing, and the byte comparison authorises: every version
  the two trees share must match, and the whole `files/` tree with it. An
  identical ebuild is not enough on its own — a patch of the same filename with
  different contents makes an identical ebuild apply *our* patch, not theirs.

  Without `--apply` the command plans and prints: three groups, every package
  carrying its reason, listing every file that would go and every registry entry
  that would go with it. The planner has no capability to remove, so "prints a
  plan and changes nothing" is a property of the structure rather than of a
  well-placed condition.

  `--apply` asks twice, because the two batches are two decisions. The identical
  batch loses nothing — every byte is already in ::gentoo — so `--yes` may answer
  for it. The diverging batch discards work that exists nowhere else, so it is
  asked separately, naming each package and what removing it takes, and **`--yes`
  does not answer that one**: a session with no terminal is refused there
  outright. That flag exists so a scripted run can proceed unattended, and
  discarding the only copy of something is not a decision a script may take on
  its own.

  A removal deletes the package directory and then every `packages.toml` entry of
  that atom — all of them, since 90 of the registry's 321 atoms carry more than
  one and a half-deleted atom keeps updating a package that is gone. The registry
  edit runs after the removals and only for the packages whose directory actually
  went, so the file never claims a removal that did not happen. Every other record
  is re-emitted byte for byte; a run matching no atom leaves the file untouched
  rather than rewritten identically, because an mtime change on a file the overlay
  auto-commits is a commit.

  A package whose registry entry declares `patched` is refused outright and
  `--include-patched` does not reach it: the declaration already makes the verdict
  `keep` rather than `redundant`, and this command never removes a package the
  verdict refused. Clearing a stale declaration stays `overlay analyze`'s
  business, where the decision leaves a record.

  An API-only provider refuses everything and says so, rather than spending one
  rate-limited request per package to reach a refusal that was certain beforehand.
  A run that examined nothing never reports that nothing qualified — the two
  send an operator to entirely different places, and only one of them means the
  overlay is clean. `overlay compare` is unchanged: no flag added, no verdict
  changed, no count changed.

- **`overlay compare` now says what to do, not only what is.** It answered one
  question — how do the versions compare? — and the operator had to answer a
  second from memory: does this package carry changes of our own? Without that
  second answer the first is ambiguous in the most dangerous direction.
  *up-to-date* means "delete this from the overlay" for a package that merely
  ran ahead of ::gentoo, and "keep it, it is correct" for one carrying a patch.
  Same word, opposite actions, and nothing on screen distinguished them.

  A registry entry can now declare `patched = "<reason>"`, and the report gains
  a second, orthogonal **Verdict** column derived from that declaration plus the
  version comparison: `keep`, `redundant`, `needs-rebase`, `unknown`. Sections
  are grouped by it, redundant first, and that section states plainly that
  removing those packages is a recommendation — nothing is ever deleted,
  disabled or modified by any verdict. `--only-redundant` and `--only-patched`
  narrow the report and intersect with the existing `--only-outdated`.

  `unknown` is a verdict rather than a default, and that is the load-bearing
  choice. Measured against the live overlay on 2026-08-04: 13 of 313 overlay
  packages have no registry entry at all, and reading their silence as "not
  patched" would have recommended deleting `sys-devel/binutils` the day
  ::gentoo reaches 2.47. A recommendation to delete is exactly where a default
  must not be silent. `patched` is the reason, not a flag — a package marked as
  diverging without a stated reason cannot be re-applied by whoever bumps it
  next — so a present-but-whitespace-only value fails validation, the same rule
  that already rejects an empty `@` label.

  The value is declared, not derived: `overlay diff` is git diff over our own
  repository and says nothing about ::gentoo, so there was no existing
  capability to detect divergence. Where a local copy of the compared repository
  is already on disk the check is free, so the two ebuilds of a matching version
  are read and compared byte for byte. That produces two findings — a **stale
  declaration** when identical content still declares a divergence, and an
  **undeclared divergence** when differing content declares none — and neither
  can change the verdict. One mechanism decides and the other only checks;
  letting both decide would leave a disagreement between them with no
  resolution.

  `Status` is untouched: it keeps its four values, its counts, its sections'
  numbers and `--only-outdated`, all unchanged. The new column is additive, and
  a regression test asserting on the rendered output with no divergence
  information at all is what pins that rather than a claim in prose.

  **A patched package now says so on every run.** The declaration reached the
  terminal only through a verification finding, and verification needs a local
  copy of ::gentoo on disk — so on an API-only run a patched package printed
  exactly like an unpatched one: same `keep` verdict, no mention of the
  divergence. That is the ambiguity above, one level down. Every patched package
  now carries a line naming the registry entry that declared it and the declared
  reason, whether or not the content was checked. The entry key is printed whole
  because it is what you grep the registry for; the reason is capped, because one
  entry's prose must not decide the width of the report. A declaration already
  found stale keeps its warning and is not also restated as fact.

  The summary gained a `Verdicts:` line counting each verdict across the whole
  scan — not across a filtered view, so `--only-redundant` narrows what you see
  without rewriting the overlay's own totals.

### Fixed
- **`overlay autoupdate` downloaded every distfile into RAM, then sent the
  resulting failure to an LLM.** The manifest step hardcoded its distdir to
  `os.MkdirTemp("")` — `os.TempDir()`, a 31 GB **tmpfs** on the host where this
  was measured — so a bump's distfiles were written into memory, several packages
  at a time, on a machine already 11 GB into swap. `make.conf` meanwhile pointed
  `DISTDIR` at disk, and the sibling `overlay manifest` command has taken a
  configurable, persistent distdir all along. Two packages failed with
  `Cannot write to '…' (Success)` — wget's message when a write fails with
  `errno == 0`, the shape ENOSPC takes on tmpfs.

  Both ebuilds were correct, and the LLM fixer was invoked anyway: 10 minutes and
  up to 30 turns per package, holding `Bash(wget *)` and `Bash(pkgdev *)`, whose
  only available conclusion is that `SRC_URI` is wrong. Both packages then applied
  cleanly on a later run **with nothing changed** — the fixer was billed against
  quota for a condition that had already stopped existing, while holding the tools
  to edit a correct ebuild in a repository that auto-commits and pushes.

  The distdir now resolves `--distdir` → `autoupdate.distdir` → `portageq distdir`
  → `/var/cache/distfiles`, and never consults `os.TempDir()`. An unwritable
  directory is a hard failure that names the path, not a silent retreat.

  Sharing the host's real `DISTDIR` buys reuse and gives up isolation, so three
  rules the old per-run temp dir enforced structurally are now explicit: a
  distfile present under a name the current `Manifest` does not list is
  **quarantined** rather than digested (a truncated download must never become a
  published checksum — portage's default `FETCHCOMMAND` writes straight to the
  final name); a failed fetch removes **only** artefacts that run created; and a
  per-distfile lock serialises the sweep's concurrent workers. A directory bentoo
  did not create is never removed.

  Separately, a failure is now **classified from observable state** — is the
  distdir still writable, is there free space, is an expected artefact zero-length
  — and never from message text. That last point is not hypothetical: this host
  runs `LC_MESSAGES=pt_BR.UTF-8`, so a classifier grepping `Cannot write to`
  answers "repairable" the moment a child process stops inheriting a C locale.
  An environment failure now reports that the cause was the environment and not
  the ebuild, and the fixer is **never constructed** — whether that verdict comes
  from the classification or from the pre-flight, which answers "this distdir
  cannot be prepared" and "another writer still holds this distfile" before
  `pkgdev` is spawned at all. Uncertain still means repairable, so a wrong
  classification costs one fixer call and never a repair that exists today —
  which is also why a handful of rarer pre-flight refusals still reach the
  fixer rather than being guessed at.

  The private distdir the fixer verifies its own repair in is no longer made in
  `os.TempDir()` either; it now goes under the host's `PORTAGE_TMPDIR`, falling
  back to today's behaviour on a machine that cannot answer.

  Both fixers additionally record which model they used and whether it was an
  **alias** — the default `sonnet` is one, and an alias resolves to a different
  model over time, so a record that hides it invites the wrong conclusion when a
  bad edit is audited months later.

  **Known limitation:** `pkgdev` does not participate in portage's `distlocks`,
  verified rather than assumed — see the README. A sweep run concurrently with an
  `emerge` fetching the same distfile is not serialised against it.

  `overlay manifest` is unchanged, deliberately: it keeps its own documented
  default of a throwaway temporary directory needing no `sudo`.

- **The prune's refusal list explained itself with a reason that covered only
  half of what it prints.** Its doc comment justified showing no per-package
  inventory with "a package refused by its verdict was never even listed" — true
  of a verdict refusal, which stops the planner before the directory is ever
  read, and false of an unverifiable one, which passes the verdict gate and does
  build a full inventory that is then dropped deliberately. The decision was
  right; the record of it described a smaller set than the code covers, which is
  how a correct comment turns into a wrong answer for whoever reads it next.
  Output is byte-identical — nothing about the command's behaviour changed.

  Alongside it, "nothing was examined" is now tested in both of its shapes. The
  suite covered the run that COULD not look — no local ::gentoo tree on disk, so
  point bentoo at one — and not the run with NOTHING to look at, an overlay
  holding no package at all. There, that same advice sends an operator off to fix
  a provider that was never the problem. The two runs must not print the same
  thing, and now a test fails if they do.

## [0.20.0] - 2026-08-05

### Added
- **`bentoo overlay autoupdate --clean` now sweeps the overlay on its own,
  without `--apply`.** The post-check reconciliation could already tell you which
  ebuilds no registry entry claims, but removing them meant applying an update to
  each package one at a time — the residue of a package with nothing to update
  was simply unreachable. `--clean` alone now plans the whole overlay, or one
  category or `category/package` given as an argument, shows every directory and
  every file it would remove, asks once for the batch, and runs the removals
  concurrently with per-directory failure isolation.

  The candidates come from the same reconciliation `--check` prints, so what a
  sweep touches is what you were shown; the verdict on whether a file may go
  still comes from the existing planner, so the live-ebuild rule, the
  last-non-live floor and the pin-less block all continue to apply unchanged. An
  unattended run without `--yes` prints the plan and removes nothing.

  **Held packages are deliberately out of the sweep's reach.** Recording a held
  entry's pin (see below) means a hand-bumped held package leaves its previous
  ebuild unclaimed — and that ebuild is the fallback `hold` exists to keep. It is
  still reported by `--check`, so nothing becomes invisible; it is simply never
  removed for you. The sweep lists those directories under their own **Held**
  heading rather than passing over them quietly, because `--check` ends its
  unclaimed list by pointing at `--clean`: a sweep that answered "every ebuild is
  claimed" would contradict the report that sent you there. Clean it by hand if
  you want it gone.

  **The sweep runs one directory at a time unless you say otherwise.** The
  `--concurrency` flag describes parallel checks and applies; it now has to be
  passed explicitly to widen a sweep. Whether concurrent `pkgdev manifest` runs
  contend on DISTDIR or on pkgdev's own locking was never measured, and fanning
  out ten processes that delete files on an unverified assumption is not a
  default worth having.

### Changed
- **`--yes` and `--concurrency` now say what a sweep does to them.** `--yes` still
  described the post-check registry reconciliation as the only thing it approves.
  It is not: it also authorises a `--lint --fix` repair, and now the deletion of
  ebuilds by a standalone `--clean` sweep — in an overlay that auto-commits and
  pushes. The `--clean` help said an unattended sweep needs `--yes`; `--yes` never
  said it could delete. A flag whose consequence is publication has to carry that
  where its reader is, not only in the flag that happens to trigger it.

  `--concurrency` now states the exception it already had in the code: it still
  bounds parallel checks and applies, but a standalone sweep runs one directory at
  a time unless the flag is passed explicitly.

### Fixed
- **A held registry entry now records which ebuild it keeps.** The post-check
  reconciliation fills in each entry's `version` pin from what is on disk, and
  it skipped `hold = true` entries alongside `enabled = false` ones — a single
  condition covering two situations that were never the same. A disabled entry
  is skipped because there is nothing to record; a held entry was skipped to
  respect a maintainer decision. But `hold` means *"present, but do not
  auto-bump"*: the ebuild is on disk, and writing down which version it is
  second-guesses nothing. The consequence was that all three held entries in the
  registry had no pin and, by construction, never could — no number of `--check`
  runs would fill one in, and a manual bump of a held package left the registry
  stale with nothing on screen to say so.

  A held entry is now compared and reported exactly like any other: a
  disagreement is a stale pin in the same batch and the same confirmation, and a
  held entry whose directory holds no ebuild lands in the no-ebuild class, which
  is never written and so cannot erase the pin it still carries. Holding itself
  is untouched — a held package is still excluded from the check, from
  auto-disable and from revive, and the reconciliation never writes an entry's
  `hold` or `enabled` back. Disabled entries stay out, for the reason that
  actually applies to them.

## [0.19.0] - 2026-08-04

### Added
- **`base_from = "none"`: an upstream that publishes no version at all can now
  say so.** `base_from` named where the base version comes from, but had no way
  to say there is nowhere — and an absent field reads identically as "nobody
  declared the source" and as "there is no source to declare". The `legacy-base`
  rule added in 0.18.0 cannot tell those apart, so it reported the second kind
  forever with no action its reader could take. Across the overlay's 411 records
  it fired exactly twice, and both were false positives: `sci-ml/ik_llama-cpp`
  (one tag, `t0002`, a prerelease roughly a year behind an active HEAD) and
  `sys-apps/asus-ec-sensors` (one stale `v0.1.0`, board support landing as plain
  commits). Neither has a version in-tree, in a tag, or in a commit title, so
  the base cannot freeze — nothing is moving for it to fall behind.

  Declaring `"none"` silences the rule for those and keeps it sharp everywhere
  else. It resolves nothing at check time and forbids `base_url`,
  `base_pattern`, `base_tag_pattern` and `commit_version_pattern` alongside it,
  since declaring a source next to "there is no source" is a contradiction
  rather than dead weight. An absent `base_from` still behaves the same way and
  still loads — it just cannot say whether that was the intent, which is why the
  rule keeps reporting it.

## [0.18.1] - 2026-08-04

### Fixed
- **`--lint --fix` no longer prints the same findings twice when there is
  nothing to repair.** Once the mechanical findings are gone, the run listed
  them once as lint output and again under "still need a human", and the two
  sentences read as a contradiction besides — "Nothing to repair" followed by
  "2 issue(s) remain". The verdict is now one line that ties both together:
  nothing was repairable *because* what remains has no mechanical fix. The
  after-a-write report is unchanged and still lists in full, since there it
  comes from a fresh lint of the rewritten file and can differ from anything
  printed earlier.

- **Reviving a package no longer writes the redundant `enabled = true` that the
  linter then reports.** Enabling is the registry's DEFAULT, spelled by the
  key's absence — `EnablePackagesInConfig` already knew that well enough not to
  *insert* the key when it was missing, but still *rewrote* an existing
  `enabled = false` into `enabled = true`, which states nothing the file did not
  already say. Since 0.18.0 that line is a `redundant-enabled` finding and
  `--lint --fix` deletes it, so a revive and a repair would undo each other on
  every cycle: one KDE 6.7.3 → 6.7.4 batch revived 71 entries and put 71
  findings into a registry that had just been cleaned. Enabling now deletes the
  assignment instead. Disabling is unchanged and deliberately asymmetric —
  `enabled = false` is the only way to express disabled, so it is still written
  down.

## [0.18.0] - 2026-08-03

### Added
- **`--lint` now checks the registry's field SET, and `--lint --fix` repairs it.**
  Validation covered the values of a record's fields well — `select`, `type`,
  `track`, `base_from`, capture arity on the `base_*` regexes — but never which
  fields exist, which are live data, and in what order they appear. The result
  was a 411-record registry whose most-declared classification field was the one
  nothing read. Four rules close that, each derived from a count over the real
  registry rather than from taste: `legacy-binary` (23 records), 
  `redundant-enabled` (3), `field-order` (13, the Khronos entries putting
  `headers` mid-`base_*`) and `legacy-base` (2 entries tracking commits with no
  `base_from`, so the base version freezes without warning). `--lint --fix`
  rewrites the first three; `legacy-base` is reported and left alone, because
  choosing between `file`, `tag` and `commit_message` depends on where upstream
  versions itself and a guess is worse than the report.

  The repair is textual, not a re-encode: it keeps the maintainer's original
  lines and only reorders, drops or substitutes them, so the quoting choices
  survive (414 regexes in literal strings, 2 in basic strings because the regex
  itself contains a quote a literal string cannot hold) and every `comments`
  block comes through byte for byte. Before writing, it reparses the result and
  compares it record by record against the original, aborting without writing on
  any difference outside the declared transformations — plus a byte-level check
  on the doc blocks and a line inventory, because `# END` markers, the file
  header and the blank lines between records are invisible to a TOML parse and a
  rewriter silently stripping them is exactly the corruption the gate exists to
  prevent. Ten injected corruptions were each verified to abort leaving the file
  byte-identical. The write is gated behind the unified diff and a confirmation:
  this overlay auto-commits and pushes, so an unattended repair is a published
  repair, and only `--yes` writes without asking.

- **An unknown key in `packages.toml` now fails the load instead of vanishing.**
  Loading used `toml.Unmarshal`, which silently ignores any key the struct does
  not declare, so writing `serie` instead of `series` would quietly disable the
  release-line filter — precisely the silent failure `series` exists to prevent.
  Every offending key is named with its record in one error rather than one per
  round trip. There is deliberately no repair: a wrong name may be a misspelling
  of a real field or a concept that does not exist.

- **The six authenticated-fetch keys hidden inside `meta` are validated.** The
  field's own comment claimed the checker ignored it, but the applier reads
  `fetch_url`, `fetch_method`, `fetch_serial_env`, `fetch_serial_field`,
  `fetch_form` and `fetch_filename` out of it — real fields inside a
  `map[string]string`, where a typo silently disabled the authenticated
  download. Strict decoding cannot see them (a map claims every key inside it),
  so they get their own validator, and the doc comment now says what the field
  actually is.

- **`version` in `packages.toml`: a record now says which ebuild it keeps, and
  `--clean` sweeps the rest.** `--clean` used to remove exactly one file — the
  ebuild it had just bumped from — so every other stale version stayed. The
  registry could not help decide what was left over, because it stored no
  version at all: which ebuild belonged to which entry was resolved at run time
  from `series` and the key's `:slot` suffix, and never written down. The new
  `version` field records that answer, and the sweep becomes "keep what the
  registry claims, remove what it does not". Measured on a 410-record overlay:
  94 directories hold more than one non-live ebuild and 90 of them are
  deliberate — one entry per release line or per slot — so a "keep only the
  highest" sweep would have destroyed all 90. This one removes exactly the four
  directories that really hold residue.

  The pin is written by the **apply**, after the ebuild lands on disk, never by
  the check from the upstream target. Writing an unbuilt version would point the
  registry at a file that does not exist, and the rule "remove what the registry
  does not claim" would then delete the only ebuild there is — a failed update
  would become a deleted package.

  Nothing is removed when the answer is unknown: if any entry sharing the
  directory declares no `version`, the sweep reports what it would have removed
  and touches nothing. Live `-9999` ebuilds are always kept, and the last
  non-live ebuild of a directory is never removed whatever the pins say.

- **`--check` reconciles the registry against the overlay, behind one
  confirmation.** After a check, divergences are reported in three classes —
  a stale pin, an ebuild no entry keeps, an entry whose directory holds none —
  and a single prompt writes the whole batch. `--yes` (`-y`) approves without
  prompting and is **required** for a non-interactive run: without it, a piped
  or scripted check reports the divergences and writes nothing. `packages.toml`
  is a published artifact, so an unattended write would reach `origin` within
  minutes. Only stale pins are written; the other two classes are reported and
  left for a human.

### Changed
- **`binary` is retired; `type` is the only classifier.** Nothing in the checker
  or the applier ever read `PackageConfig.Binary` — the live classifier is
  `resolveType`, which uses `type` and falls back to reading the ebuild. The only
  consumer was `overlay analyze`, which *wrote* the field, so the registry
  accumulated 23 records declaring `binary = true` with no effect. `--lint --fix`
  migrates them to `type = "bin"`, or drops the line where `type` is already
  present.

- **Both record writers now emit the canonical field order.** `overlay analyze`
  printed a suggestion assembled by hand-written blocks in arbitrary sequence,
  and `--save` delegated to `toml.Encoder`, which emits in struct-declaration
  order — so a record the tool generated could fail the linter it was about to be
  checked against. Both now render through one function driven by the same order
  slice the linter ranks against. That also fixes two defects in the save path: a
  populated `headers` or `meta` was written as a sub-table, `["pkg".headers]`,
  which the record scanner reads as a *new* record — leaving the real one
  reported unclosed and undocumented while the phantom collected its `# END` —
  and `timeout = 0` / `revision = 0` were written into every saved record because
  `omitempty` does not suppress a numeric zero.

### Fixed
- **`--apply all --clean` no longer deletes a release line it just built.** The
  sweep decided what to keep from the pin alone, while the reconciliation
  already treated an entry as holding whatever it resolves to on disk. Because
  `--apply all` builds one applier whose registry snapshot is never reloaded,
  applying both lines of a two-entry package in one command planned the second
  against the pin the run started with — which the first apply had already made
  stale. The ebuild built seconds earlier was held by nobody and was removed,
  with the apply still reporting success and the registry left pinning a file
  that no longer existed. Both halves of a claim now count.

- **The 206 gate now observes the request that was actually sent.** Accepting
  HTTP 206 is conditional on the record having asked for a range, but the check
  read the header map from `packages.toml` instead of the wire — and the two
  disagree at the edges. `setHeader` drops names containing CR/LF and then
  applies `TrimSpace` + `CanonicalMIMEHeaderKey`, and the client's own default
  headers never appear in that map at all. The gate now reads `Range` off the
  request recorded on the response, which removes the second copy of the
  acceptance policy entirely so it cannot drift from header application again.

  A nil response, or one whose request the transport did not record, reads as
  "no range declared": absent evidence is not permission, so an unsolicited 206
  still fails on the status error.

- **`--lint` no longer flags the file header of `packages.toml`.** The
  `stray-comment` rule treated every comment outside a record as stranded
  documentation, header included. Restoring the overlay's ~112-line header — the
  text that documents the record model itself: field order, `enabled` vs `hold`,
  the traps a new record has to avoid — turned a clean registry into 112
  violations and an exit code of 1, so the rule read as an order to delete the
  one thing that makes the file editable by hand. Comments before the first
  record are now the file header and are allowed; a comment anywhere after the
  first record has begun — between records or trailing the last one — is still
  reported, because it belongs in the `comments` field of the record it
  describes.

## [0.17.1] - 2026-08-01

### Fixed
- **A `Range` header on a record now works: `--check` accepts HTTP 206 as
  success.** `Headers` in `packages.toml` could already put `Range` on the wire,
  but the fetcher treated everything except 200 as a failure, so the partial
  response came back as `HTTP request returned status 206` and the header was
  unusable. This mattered most for `www-misc/warsaw`, at 56% of all autoupdate
  traffic: the version string sits ~590 KB into an 8.2 MB payload that has not
  changed since 2024-08-26, so every check pulled the whole file and
  intermittently blew the 30 s per-request deadline. With a 2 MiB range the same
  check measures ~1 s end to end.

  The guard names 200 and 206 explicitly rather than accepting the 2xx range:
  204 and 205 carry an empty body by definition, and admitting them would trade
  a clear HTTP error for a confusing parser failure further downstream. 5xx is
  unaffected — the retry layer classifies it as retryable and never reaches this
  guard.

## [0.17.0] - 2026-08-01

### Added
- **`base_from` / `base_url` / `base_pattern`: a commit-tracked record can say
  where its base version actually lives.** The `X.Y.Z` in front of the
  `_p<date>` suffix had exactly one possible source — a regex over commit titles
  — and that source is the weakest one upstreams offer. The fetch reads a fixed
  window of recent commits (`per_page=`), and the window is measured in commits,
  not days: 50 commits cover ten months of Vulkan-Headers but 1.3 days of zed,
  whose `Bump Zed to v1.15.0` had already fallen to index 59 and become
  invisible. `base_from = "file"` instead fetches the file upstream maintains
  itself (`crates/zed/Cargo.toml`, mesa's `VERSION`, `meson.build`,
  `CMakeLists.txt`) — one request, no window, no pagination. Measured against
  live upstreams, it corrects eight packages at once: `media-libs/mesa`
  26.2.0 → 26.3.0, `net-libs/libqmi` 1.38.1 → 1.39.1, `net-misc/modemmanager`
  1.25.1 → 1.25.95, `media-libs/vulkan-loader` 1.4.354 → 1.4.358,
  `dev-util/vulkan-tools` 1.4.354 → 1.4.357, `media-libs/vulkan-layers`
  1.4.352 → 1.4.357.

- **`base_from = "tag"` / `base_tag_pattern`: resolve the base version from a
  tag listing.** For upstreams whose in-tree files use a different scheme than
  the ebuild does — glslang and spirv-* version themselves as `2026.3`/`1.5.5`
  while the overlay tracks them on `vulkan-sdk-X.Y.Z.W`, which exists only as
  tags. Filtering by family via `base_tag_pattern` is the whole job: these repos
  carry four or more tag families at once, and an unfiltered ranking picks
  `khronos-master-20141209` for vulkan-loader purely because it contains a large
  number. It takes the highest tag of the family rather than emulating
  `git describe` — proving ancestry would cost a `/compare` call per candidate
  per check, and would still reject the right tag, since a release-branch tag
  reports `diverged` (SPIRV-Tools: ahead 22, behind 1) against `main`.
- **A commit that IS a release tag now yields the bare version.** vulkan-headers
  pinned `11d6898`, which is exactly tag `v1.4.358`, yet shipped as
  `1.4.358_p20260731` — and `_p` orders ABOVE its base, so the name claimed to
  be newer than the very release it was. Restricted to `_p`: a `_pre` package's
  version-bump commit opens the cycle rather than closing it (zed's
  "Bump Zed to v1.15.0" precedes the release by weeks), so there the snapshot
  form stays correct.
- **The checker now reads the commit a snapshot ebuild pins.** It only ever
  wrote that value. Without reading it, a bare release version in the overlay
  would be re-bumped every single day, because tomorrow's `1.4.358_p<date>`
  compares newer than today's `1.4.358`. The guard yields to a base correction,
  which is precisely the case where the commit does not move and the version
  must: vulkan-tools sat at 1.4.354 against upstream's 1.4.357 with its pinned
  commit already current.
- **`--lint` now reports a package directory that holds a stable and a
  pre-release line without declaring `series`.** The combination fails silently
  and looks like success: `selectCurrentEbuild` takes the directory's highest
  version as "the current one", so once the pre-release line lands beside the
  stable one, every stable release compares older and the entry reports "up to
  date" forever — the line simply stops being maintained. The rule fires only on
  a genuine stable/unstable pair (one line carrying `_alpha`/`_beta`/`_pre`/
  `_rc`, another not), so two successive versions of one line mid-rotation stay
  quiet, and so do two `_p` snapshot lines. Run against the overlay's 323
  entries it reports zero false positives; `app-editors/zed-bin@stable` /
  `@preview` is the shape it points you toward.

### Changed
- **A declared base-version source that resolves nothing now fails the check.**
  It used to fall through to whatever base the ebuild already carried, which is
  indistinguishable from being up to date. Six of the seven registry entries
  carrying a `commit_version_pattern` matched nothing at all — the pattern had
  been copied from Vulkan-Headers to sibling Khronos repos that never write
  `Update for Vulkan-Docs` in their commit titles — and their bases froze up to
  seven releases behind while the `_p<date>` kept advancing every day. The
  versions looked alive and were not. The error names the pattern, the endpoint,
  and the two ways out (raise `per_page`, or move to `base_from = "file"`).

## [0.16.0] - 2026-07-31

### Added
- **`suffix` / `suffix_when`: a record can declare that what it extracts is a
  pre-release.** Upstream numbering rarely says so. LibreOffice publishes
  `26.8.0.1` in its testing channel with a version string indistinguishable from
  a stable one, so the value landed in the overlay as a finished release: the
  bump rewrote `libreoffice-26.8.0.1_pre.ebuild` to `libreoffice-26.8.0.1.ebuild`
  and reported an update where there was none. `suffix = "_pre"` restores both
  the label and the ordering — Gentoo sorts `_pre` below the bare version, so the
  bump now fires exactly when upstream promotes the release. `suffix_when` gates
  it by regex, for the common case of one index listing several release lines
  (the LibreOffice archive carries the stable 26.2 line and the testing 26.8 one
  together, and `select = "max"` always returns the latter). The suffix is
  applied after `transform` and before comparison, so `select` orders the values
  that will actually become the PV; it is idempotent, and rejected in
  combination with `track = "commit"`, which derives its own snapshot suffix.

- **`comments`: record documentation is now a field, not a floating comment.**
  Two problems went with `#` lines. A comment between two records belongs to
  neither, so nothing says which one it documents; and `overlay analyze --save`
  re-encodes the whole registry through `toml.Encoder`, which silently erased
  every doc comment in it. As a field the text has an owner and survives the
  rewrite — `savePackagesConfig` now writes records one at a time, emitting the
  doc as a readable multi-line string instead of one line of escaped `\n`.

- **`series` + `@label`: one package can be tracked on several release lines.**
  An overlay routinely carries more than one ebuild per package, and one entry
  could not track them all — `selectCurrentEbuild` takes the directory's highest
  version, so the other lines were never bumped. The `:slot` key suffix covered
  the case where the lines are separate SLOTs (`net-libs/webkit-gtk`); nothing
  covered lines that share a SLOT. `app-editors/zed-bin` shows the cost: with
  `1.13.1` stable and `1.14.1_pre` preview side by side and the entry tracking
  the stable channel, every stable release below `1.14.1` compared *older* than
  the preview ebuild and reported "up to date" — the line silently stopped being
  updated. `series` is a regex that narrows both ends of the comparison (which
  ebuild is current, which upstream candidates are eligible), and `@label` makes
  the keys unique (`app-office/libreoffice@stable` / `@testing`). A version
  outside the series now fails loudly instead of being compared against an ebuild
  the entry does not track; a series that matches no ebuild reports
  `ErrSeriesNotFound` rather than the orphan error that auto-disables an entry;
  and two entries that would scan the same ebuilds are rejected by validation
  and by `--lint`, since a label alone filters nothing.

- **`bentoo overlay autoupdate --lint`** checks the registry against the record
  model: every record closed by a `# END` marker, documented by a trailing
  `comments` field, with no comment floating outside a record — plus the
  semantic validation of every record's fields. It reports the full list with a
  per-rule tally and exits non-zero, so it works as a pre-commit gate. `# END`
  is a comment because TOML has no block delimiter: a bare `[END]` table would
  parse as a package named `END`, and repeated per record, as a duplicate-table
  error that stops the file from loading.

### Fixed
- **`SetDefaultHeaders` no longer drops the built-in User-Agent.** It replaced
  the whole map, so any caller setting a single header also erased the
  `User-Agent` the constructor installs — the descriptive UA that exists
  precisely because many WAF/Cloudflare-fronted upstreams reject Go's default
  `Go-http-client/1.1` with a 403. Provided headers are now merged into the
  defaults, and an explicit key still overrides. The bug was latent rather than
  live: today the method has no production caller, only tests. Found by the
  repository's AI code-quality findings (#79).

## [0.15.3] - 2026-07-31

### Added
- **CI requires a changelog entry for user-visible changes.** A PR that touches
  `go.mod`, `go.sum` or non-test Go source must also touch `CHANGELOG.md`, or
  carry a `no-changelog` label. Three releases in a row nearly shipped notes
  that omitted whole changes — PR #57 before 0.15.0, #60-63 before 0.15.2,
  #69-73 before this — every time because a merge touched only the module files
  and nobody noticed until the release was being cut. Dependency bumps are the
  recurring case: the tooling that opens them does not write changelog entries,
  so the omission was silent by construction. Now it is loud. Verified against
  six cases, including the two that must fail.


### Changed
- Bumped dependencies: `github.com/antchfx/xpath` v1.3.7 → v1.3.8,
  `github.com/mattn/go-isatty` v0.0.22 → v0.0.24,
  `github.com/mattn/go-runewidth` v0.0.24 → v0.0.27,
  `github.com/aymanbagabas/go-udiff` v0.3.1 → v0.4.1, and the `golang-x` group
  (`x/mod` v0.38.0, `x/tools` v0.48.0, `x/vuln` v1.6.0, `x/telemetry`). The two
  `mattn` modules are the ones the previous entry predicted: indirect, linked
  into the binary, and proposed for the first time now that Dependabot watches
  transitive requirements.

  Validated as a set against `main` rather than trusting each PR's own checks,
  which had run against an older base: `go mod verify`, no `go mod tidy` drift,
  `go build` under the default and both build tags, `go test -race`,
  `make build-all`, and `govulncheck` re-run under all three tags — the last
  because this batch upgrades `x/vuln` itself, so the analyser changed.

  Two linked modules stay behind deliberately. `chromedp/cdproto` is pinned to
  the exact pseudo-version `chromedp` v0.16.0 requires and must move with it,
  not ahead of it. `playwright-go` stays at v0.6000.0 because v0.6100.0 is still
  published under the wrong module path — the two releases are 19 hours apart on
  the same day, so the pin is not accumulating debt.
- **Dependabot now watches indirect dependencies too.** The `gomod` config used
  the default `allow` (direct only), so everything transitive went unwatched —
  except `golang.org/x/*`, which slipped through only because the `golang-x`
  group pattern matches it. A dependency-chain audit found
  `github.com/mattn/go-isatty` and `go-runewidth`, both linked into the binary,
  sitting behind with no PR ever opened. What matters is being compiled in, not
  how the requirement is spelled in `go.mod`. The added volume is bounded by the
  existing PR limit and the 7-day cooldown.

## [0.15.2] - 2026-07-30

### Changed
- Bumped dependencies: `github.com/antchfx/xpath` v1.3.6 → v1.3.7,
  `github.com/chromedp/chromedp` v0.15.1 → v0.16.0 (with `cdproto` to match),
  `actions/checkout` v7.0.0 → v7.0.1 and `actions/setup-go` v6.5.0 → v7.0.0.
  The setup-go major is an internal ESM migration — the runtime stays `node24`
  and no input changed. Each action's pinned SHA was checked against the real
  tag. A full supply-chain audit accompanied the bumps: no open Dependabot
  alerts, and no findings from `govulncheck` (under all three build tags),
  `osv-scanner` over the whole module graph, or `trivy`. `go1.26.5` is current.
- **CI now compiles the tagged script evaluators.** `script_evaluator_chromedp.go`
  is `//go:build chromedp && !playwright` and its playwright sibling is the
  mirror, so `go build ./...` and `go test ./...` skipped both files entirely —
  the only code that calls chromedp or playwright-go was never compiled by any
  job. A dependency bump that broke their API passed CI fully green and failed
  only for whoever built with the tag. The `build` job now runs `go build` and
  `go vet` under each tag. Verified by breaking a chromedp call on purpose:
  default `build`, `vet` and `test` all still passed; the new step failed.
- **`govulncheck` now runs under each build tag too.** The same blind spot, with
  a security consequence rather than a compile one: govulncheck skips a file
  behind a tag it was not given, so the default run said nothing about chromedp
  or playwright-go — the two modules whose call sites live only in the tagged
  evaluators. A reachable CVE in either went unreported. The `audit` job now
  analyses default, `chromedp` and `playwright`, reports every affected tag
  rather than stopping at the first, and fails the job if any has a reachable
  third-party finding. Confirmed by simulating a finding under one tag: the
  other two still passed, the affected one was named, and the step exited 1.

### Fixed
- **A slot that matches nothing no longer disables the entry.**
  `selectCurrentEbuild` reported "no ebuild declares this SLOT" as
  `ErrNoEbuildFound` — the same error that means "this package is gone from the
  overlay", which the checker acts on by writing `enabled = false` into
  `packages.toml`. A typo'd slot (`net-libs/webkit-gtk:4.2`) therefore switched
  a live entry off silently, with no error and no exit code to notice. The two
  are now distinct: the new `ErrSlotNotFound` is raised only when the package
  directory IS present and no ebuild in it declares the slot, and it surfaces as
  an ordinary failure. The applier likewise stops pruning such a pending entry
  as obsolete — the package is there, the key is wrong.

  This is the failure mode a pre-0.15.0 checker hits against the slot keys it
  predates: it reads `net-libs/webkit-gtk:4.1` as a directory name, does not
  find it, and disables both webkit-gtk entries without a word. Config the
  checker cannot interpret must fail loudly rather than quietly downgrade
  itself.

## [0.15.1] - 2026-07-26

### Fixed
- **`hold = true` is now enforced at apply time, not only in the checker.** The
  flag was read in exactly one place — `CheckAll`, which skips held packages —
  and the applier never consulted the config at all. An explicit
  `--check <pkg> --force` bypasses the checker's filter and writes the update to
  `pending.json` like any other entry, so from that moment `--apply all` applied
  the very bump the hold existed to prevent, silently and indistinguishably from
  an ordinary update. This is reachable from normal use rather than a corner
  case: probing a held package with `--force` is the only way to track it at
  all, since it is absent from every mass check, and doing so arms the queue as
  a side effect. `Apply` now refuses a held package before touching the
  filesystem, modelled on the existing obsolete path — `Success` false, `Error`
  nil, not counted as a failure by `--apply all`. Unlike an obsolete entry the
  pending record is **kept**: the update is real and still pending, it just may
  not be applied automatically, and pruning it would erase what the operator
  wants to see. Surfaced as `Status: Held` per package and a `Held:` line in the
  apply-all summary.
- **The `revision` doc comment no longer recommends the case it exists to
  prevent.** It offered the bentoo overlay's bare `webkit-gtk-2.52.5.ebuild` as
  a legitimate example of an absent revision. A bare PV sorts below every `-rN`,
  so that ebuild lost to `::gentoo`'s `-r600` and took its non-upstream
  `USE=webdriver` with it — portage was selecting the Gentoo ebuild for SLOT 6
  the whole time. Zero is right for an ordinary package, never for a slot that
  `::gentoo` revisions.

### Changed
- Bumped indirect dependencies to their latest releases: `golang.org/x/net`
  v0.56.0 → v0.57.0, `golang.org/x/sync` v0.21.0 → v0.22.0, `golang.org/x/sys`
  v0.46.0 → v0.47.0, `golang.org/x/text` v0.38.0 → v0.40.0, `golang.org/x/tools`
  v0.46.0 → v0.47.0, and `golang.org/x/telemetry` (2026-06-25 snapshot). No API
  changes; routine upstream fixes. `go mod tidy` reports no drift and
  `govulncheck` finds no reachable vulnerabilities in the resulting graph.
- Dependabot no longer proposes `playwright-go` v0.6100.0. The tag is published
  under the wrong module path — its `go.mod` declares
  `github.com/mxschmitt/playwright-go`, so it cannot be resolved under the
  `playwright-community` name and the bump is uninstallable. Pinned to v0.6000.0
  until upstream retags; the ignore entry says to drop it once a fixed release
  lands.

## [0.15.0] - 2026-07-26

### Added
- **Snapper configs are provisioned through snapper's own API.** `snapshot apply`
  no longer writes `/etc/snapper/configs/<name>` and `/etc/conf.d/snapper` by
  hand; it calls `snapper create-config` to provision and `snapper set-config`
  to apply the managed keys. Writing those files directly cannot work: snapperd
  caches its config list and offers no D-Bus reload, so a name added to
  `SNAPPER_CONFIGS` by hand is invisible to an already-running daemon, and a
  config file edited behind its back is not observed at all. `create-config`
  also emits all 24 template keys (including `FSTYPE`, which bentoo never wrote)
  and creates `.snapshots` as a 0750 subvolume, all visible to the live daemon
  at once. Coverage is read from `snapper list-configs` rather than from a
  file's presence, and the managed keys are applied to pre-existing configs too
  — those are exactly the ones still running the template's retention. A
  leftover `.snapshots` blocks `create-config` outright, so provisioning removes
  an **empty** one and refuses a populated one, naming the command the operator
  can run instead. The dry-run names the two commands instead of the file that
  is no longer written.
- **Multi-slot packages in autoupdate.** A `packages.toml` key may now carry a
  `:slot` suffix — `["net-libs/webkit-gtk:4.1"]` — so a package that ships
  several SLOTs out of one directory gets one entry, one pending record and one
  cache record per slot instead of the slots overwriting each other. The current
  version for such an entry is resolved by reading each ebuild's `SLOT=` and
  considering only the matching ones, rather than taking the directory's highest
  version (which is whichever slot happens to be ahead). The suffix is part of
  the entry's identity only; it is stripped everywhere a filesystem path is
  built.
- **`revision` field in `packages.toml`.** The `-rN` suffix to write on a freshly
  bumped ebuild, for the packages that use the revision to tell their SLOTs apart
  (`-r410`/`-r411` are SLOT 4.1 of `net-libs/webkit-gtk`, `-r600`/`-r601` are
  SLOT 6). It is declared rather than inherited from the source ebuild because
  a PV change resets the revision (`foo-1.2.3-r1` bumps to `foo-1.2.4`), and
  where a revision marks a slot the value to write is the slot's base, not the
  source's. Absent/zero keeps the plain PV, which is what every ordinary package
  wants.

### Fixed
- **An apply can no longer overwrite an existing ebuild.** `copyEbuild` refused
  only a same-version copy; any other collision was written straight through
  `os.Create`, truncating the destination before a byte was read — and a later
  failure in the same apply then had the deferred orphan-rollback `os.Remove` it
  outright. The destination is now checked and the apply fails with
  `ErrEbuildExists`. This is the safety net under the slot work above: the
  colliding case is exactly a multi-slot package whose slots share a PV series.
- **One implementation of "current ebuild in the overlay".** The checker's
  `getCurrentVersion` and `currentEbuildPath` and the applier's
  `resolveCurrentVersion` were three copies of the same directory scan, so the
  slot filter had to land in three places to be correct in one.
- **Snapshot retention in `snapshot.toml` now actually reaches snapper.** A
  config file edited behind the running daemon's back was never observed, so
  the configured `TIMELINE_LIMIT_*` values never took effect — with
  `TIMELINE_LIMIT_HOURLY="7"` on disk, snapper reported 10. This predates the
  snapper engine entirely; `set-config` fixes a bug older than the work that
  uncovered it.

### Changed
- **`WriteEngineConfig` takes a context and a `Runner`.** The btrbk driver uses
  neither and is unchanged.

## [0.14.0] - 2026-07-19

### Added
- **Unified secret resolution.** Every secret bentoo consumes now resolves
  through one chain — an environment variable, then the user secrets file
  `$XDG_CONFIG_HOME/bentoo/secrets` (else `~/.config/bentoo/secrets`), then the
  system secrets file `/etc/bentoo/secrets`. The secrets file is `.env` style
  (`NAME=value`, `#` comments, an optional `export ` prefix), should be
  `chmod 600`, and triggers a one-time warning when it is group- or
  world-readable. The names bentoo looks up: `GITHUB_TOKEN` then `GH_TOKEN`
  (GitHub API), a per-repository `BENTOO_REPO_<NAME>_TOKEN` (the repo's config
  key uppercased, every character outside `[A-Z0-9]` mapped to `_`),
  `BENTOO_NTFY_TOKEN` (snapshot ntfy auth), and the *names* configured in
  `llm.api_key_env` (e.g. `ANTHROPIC_API_KEY`) and `fetch_serial_env` (e.g.
  `FILEZILLA_PRO_KEY`) — each resolved through the same chain.
- **Legacy-secret migration diagnostic.** When a loaded config still carries a
  removed secret key, bentoo prints an actionable warning — once, and before any
  config write — telling you to move the value into the secrets file.

### Changed
- **`--token` now outranks a per-repository token.** For `overlay compare` the
  precedence is `--token` flag > per-repo `BENTOO_REPO_<NAME>_TOKEN` > global
  `GITHUB_TOKEN`/`GH_TOKEN`. Previously a config/file token could silently
  override the explicit flag.

### Removed
- **Plaintext secret fields in config are no longer read (BREAKING).**
  `github.token` and `repositories.<name>.token` in `config.yaml`, and
  `ntfy.token` (`[notify.ntfy]`) and `smtp.password` (`[notify.email.smtp]`) in
  `snapshot.toml`, are no longer consulted. (The runtime provider token field
  itself is unchanged — only the config source was removed.) With
  `smtp.password` gone, **no bentoo config file holds a secret value of any
  kind** — the claim `SECURITY.md` makes now stands without exception.

### Migration

This is a **breaking change**: any token kept in a config file is now ignored.
For each one, move the value into the secrets file under the named environment
variable and delete the config key:

```bash
mkdir -p ~/.config/bentoo
cat >> ~/.config/bentoo/secrets <<'EOF'
GITHUB_TOKEN=ghp_xxxxxxxxxxxx
BENTOO_REPO_MY_OVERLAY_TOKEN=ghp_xxxxxxxxxxxx
BENTOO_NTFY_TOKEN=tk_xxxxxxxxxxxx
BENTOO_SMTP_PASSWORD=your-smtp-password
EOF
chmod 600 ~/.config/bentoo/secrets
```

- `github.token` → `GITHUB_TOKEN` (or `GH_TOKEN`).
- `repositories.<name>.token` → `BENTOO_REPO_<NAME>_TOKEN` (`<name>` uppercased,
  every character outside `[A-Z0-9]` replaced by `_`).
- `[notify.ntfy] token` → `BENTOO_NTFY_TOKEN`.
- `[notify.email.smtp] password` → `BENTOO_SMTP_PASSWORD`. `host`, `port` and
  `user` stay in `snapshot.toml` — they are configuration, not secrets. Until
  you migrate, email notifications are sent **unauthenticated** rather than
  failing, and each load of `snapshot.toml` warns once naming the stale key.

When the notifier runs as root under the systemd timer, `$HOME` may be unset and
the user secrets file is not consulted — put the value in `/etc/bentoo/secrets`
(root-owned, `chmod 600`) or in the unit's environment for that case.

Then remove the now-ignored `github:`/`token:` keys from `config.yaml` and the
`token` key from `snapshot.toml`. bentoo warns (once, before any config write)
if a legacy secret key is still present.

## [0.13.1] - 2026-07-13

### Fixed
- **`overlay autoupdate --check` no longer fails with HTTP 403 against
  Cloudflare-fronted upstreams.** Cloudflare fingerprints the HTTP/2 connection
  preface and answers Go's standard-library client with a "Just a moment..."
  403 interstitial no matter what `User-Agent` it sends, while serving the exact
  same request over HTTP/1.1. The autoupdate HTTP client now retries a 403 that
  arrived over HTTP/2 once over HTTP/1.1 before accepting it, which is what a
  registry entry alone could never fix: `app-misc/claude-desktop-bin` failed its
  version check with "all version extraction methods failed: HTTP request
  returned status 403" against a `packages.toml` entry that was already correct.
  The retry bypasses the retry loop and the circuit breaker — it is one extra
  request on a path that already lost its attempt, so it neither amplifies load
  nor trips the breaker on its own — and is skipped when the request carries a
  body it cannot replay, or when the caller supplied its own HTTP client via
  `SetHTTPClient` (re-enable it explicitly with `SetHTTP1FallbackClient`).

## [0.13.0] - 2026-07-01

### Added
- **Example configuration file and a `make install-config` target.** A fully
  commented `config.example.yaml` documents every setting — `overlay`, `git`,
  `github`, `autoupdate` (LLM/search), and `repositories` — and spells out the
  difference between `provider: claude-code` (the agentic fixer that runs the
  local `claude` CLI on your subscription session, no API spend) and
  `provider: claude` (the HTTP API, which needs `api_key_env`).
  `make install-config` copies it to `~/.config/bentoo/config.yaml` (honoring
  `XDG_CONFIG_HOME`, written `0600`) and never overwrites an existing config.

### Changed
- **`overlay autoupdate --apply all` regenerates Manifests in parallel.** The
  per-package apply — whose slow step is the network-bound `pkgdev manifest`
  distfile fetch — is now dispatched across a worker pool bounded by
  `--concurrency` instead of running strictly one package at a time. Results are
  still reported in input order and one package's failure never aborts the rest.
  With `--compile` the applies stay serial so the elevated compile step's
  confirmation prompt and `sudo`/`doas` invocation are never interleaved.
- **Default `--concurrency` lowered from 20 to 10.** The single value now bounds
  both the `--check` HTTP fan-out and the new `--apply all` worker pool. Ten
  keeps the per-host rate limiters saturated on `--check` while stopping
  concurrent `pkgdev manifest` downloads — which bypass those limiters — from
  overwhelming a single host on `--apply`.

## [0.12.0] - 2026-06-30

### Added
- **`provider: local` repositories — read an on-disk package tree in place.** A
  repository can now point at a local directory (e.g. a synced
  `/var/db/repos/gentoo`) with `provider: local` + `path:`, and bentoo reads it
  directly with no clone. The destructive cache operations (`RemoveCache`,
  `ForceUpdate`) are disabled for a local tree so the user's real repository is
  never pulled or removed.

### Fixed
- **`overlay autoupdate --revive` always aborted with "no local package
  directory".** Revive seeds a base ebuild off the `::gentoo` tree and so needs an
  on-disk `PackageDirProvider`, but the provider was resolved from the registry
  default (the GitHub API mirror, which has no on-disk tree). The only documented
  workaround — a `git` provider with a filesystem `url:` — was mangled into
  `https://github.com/<path>.git` and failed to clone. The new `provider: local` +
  `path:` schema makes a local `::gentoo` tree work end-to-end, and the
  revive/compare guidance messages now document it.

## [0.11.1] - 2026-06-30

### Fixed
- **`autoupdate.llm.bare: false` now actually uses the CLI's logged-in session.**
  In non-bare mode the spawned `claude` process inherited the parent environment
  verbatim, so an `ANTHROPIC_API_KEY` exported in the shell (e.g. from a shell rc)
  leaked into the child and the CLI authenticated via the paid API instead of the
  operator's subscription session — the opposite of the documented intent. The
  child environment is now scrubbed of `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`
  and the configured `api_key_env` in non-bare mode. Bare mode is unchanged (the
  key is still injected solely via the child env, never argv/logs).
- **Package host detection anchored via `net/url`** so a crafted upstream string
  can no longer bypass the host check (CodeQL finding, #20).

### Security
- Pin `govulncheck` via the `go.mod` tool directive for reproducible vulnerability
  scans (#18).
- Add a security policy (`SECURITY.md`, #19).

### Changed
- Bump `actions/checkout` 6.0.3 → 7.0.0 (#17) and
  `github.com/charmbracelet/x/ansi` 0.11.6 → 0.11.7 (#15).

## [0.11.0] - 2026-06-28

### Added
- **Agentic `packages.toml` repair at the end of `autoupdate --check`.** After a
  check run, packages that failed version detection (an outdated parser config, a
  moved upstream URL, or a changed JSON/HTML shape) can now be repaired in place
  by an LLM fixer (`RegistryFixer`). It is wired only when
  `autoupdate.llm.provider` is `claude-code` (the only provider that can edit
  files), runs the local `claude` CLI scoped with a narrow tool allowlist, and is
  offered interactively with a keep/revert prompt so every edit is reviewed before
  it is kept. Spend is bounded by `autoupdate.llm.max_budget_usd`. Packages
  without a configured `claude-code` provider are unaffected.

## [0.10.0] - 2026-06-21

### Added
- **Live TUI output for long-running subprocesses.** `overlay autoupdate --apply`
  / `--apply all` and `overlay manifest` now render a Bubble Tea-based live view —
  per-package status, overall progress, and a bounded tail of the running
  `pkgdev`/`wget` fetch — instead of a frozen line. Finished work leaves a `✓`/`✗`
  history line in the scrollback. `Ctrl-C` cancels the in-flight operation (with
  the existing orphan rollback) and restores the terminal; a `sudo`/`doas` compile
  prompt is shown on the real TTY.
- **`--no-tui` flag and `BENTOO_NO_TUI` environment variable** to force plain,
  ANSI-free streaming output. The live UI is also disabled automatically when
  stdout is not a TTY or when `NO_COLOR` is set; plain mode still streams the fetch
  tail to stderr.

### Changed
- The hand-rolled ANSI progress reporter for `overlay manifest` was replaced by the
  shared Bubble Tea TUI system, so the tool now has a single terminal-UI
  implementation. Behavior with no reporter (and in non-TTY/plain mode) is
  unchanged.

### Fixed
- **Manifest auto-fixer now surfaces the real failure on a contradictory `claude`
  exit.** When the `claude` child exited non-zero while still printing a success
  envelope, `FixManifest` emitted an empty `(success):` message and discarded the
  exit code, result, and stderr. All four terminal error branches now funnel
  through a single bounded formatter that includes the process exit code (or the
  timeout/cancellation cause), the envelope subtype/result/errors, the captured
  stderr, and the raw stdout on a parse failure — each truncated to a diagnostics
  budget.

## [0.9.0] - 2026-06-21

### Added
- **`hold` flag in `packages.toml`** for a package that is present in the overlay
  but must not be auto-bumped (e.g. `sci-ml/ollama`, whose llama.cpp FetchContent
  rearch needs manual patchset/distfile work per release). Unlike `enabled = false`
  — which is now overlay-driven bookkeeping (see below) — `hold = true` is a
  deliberate maintainer decision the status reconciliation never auto-flips. A held
  package is skipped exactly like a disabled one (no fetch, no pending, absent from
  progress and totals).

### Fixed
- **`autoupdate --check` now reconciles `enabled` status with the overlay.** A
  package that was auto-disabled (`enabled = false`) when its ebuild was removed
  stayed disabled forever even after the ebuild was re-added — so it was silently
  skipped (observed with `sci-ml/ollama`, whose source ebuild was present yet never
  checked while `sci-ml/ollama-bin` updated). `CheckAll` now re-enables, at the
  start of each run, any disabled package whose ebuild is present in the overlay
  again: the overlay is the source of truth, not `packages.toml`. The edit is
  comment-preserving and held packages (`hold = true`) are left untouched.

## [0.8.0] - 2026-06-21

### Fixed
- **Manifest LLM auto-fixer no longer dies with `argument list too long`.** When
  `pkgdev manifest` failed on a large distfile, its combined output — including
  `wget`'s progress bar, which emits one dot per ~1KB, so a multi-hundred-MB
  download dumped hundreds of thousands of progress lines — was embedded whole into
  the agent's `-p` instruction. That single argv element exceeded Linux's
  `MAX_ARG_STRLEN` (128 KiB per argument), so `execve` failed with `E2BIG`
  (`fork/exec … : argument list too long`) and the fixer never started (observed
  bumping `net-misc/rustdesk`). The manifest output is now bounded to a head+tail
  budget before it reaches argv, preserving the actionable diagnostic (the failing
  URI and the final `failed fetching` lines) while eliding the noisy middle.
- **`autoupdate --check` retries now actually run against a slow or hung host.**
  The per-operation budget was equal to the per-request HTTP timeout (both 30s),
  so the first slow request consumed the entire budget and the 3 configured
  retries never fired — an intermittently slow upstream (e.g. `salsa.debian.org`,
  `sources.debian.org`) failed outright with `context deadline exceeded`. The
  per-operation budget is now derived to be large enough for every retry attempt
  to run within it (`per-request × (retries+1) + backoff`), so a transient slow
  first attempt is retried instead of failing the package. A timeout error now
  also names the host and the per-request cap, so it is clear which endpoint was
  slow and which knob to raise.

### Added
- **Configurable HTTP timeout for `autoupdate --check`.** The per-request timeout
  is now resolved as `--timeout <seconds>` (flag) > `autoupdate.http_timeout`
  (config) > `30` (default), and the per-operation budget scales from it. Raise it
  for hosts whose single response legitimately takes longer than the default.
- **Per-package `timeout` in `packages.toml`.** A package may set `timeout = N`
  (seconds) to override its per-operation budget — extra retry headroom for a
  reliably slow host without slowing the rest of the batch. Absent/`0` uses the
  global budget. The override also applies to the `script` (headless-browser)
  parser path.

## [0.7.1] - 2026-06-18

### Fixed
- **Manifest auto-fixer no longer hangs past its timeout.** The agentic fixer
  spawns child processes (`pkgdev`, `wget`); if the per-call timeout or a SIGINT
  killed `claude` while a child still held the output pipe open, `FixManifest`
  could block far past the deadline. It now bounds post-cancellation cleanup
  (`cmd.WaitDelay`), so it always returns within the configured timeout plus a
  short grace. The same change deflakes a CI test that reproduced the hang and
  resolves a `contextcheck` lint nit in the fixer — no behaviour change to a
  successful fix.

## [0.7.0] - 2026-06-18

### Added
- **LLM auto-fix for failed manifests in `autoupdate --apply`.** When `pkgdev
  manifest` fails during an apply (typically a `SRC_URI` 404 because the upstream
  URL convention changed between versions — e.g. a stable release moving from
  `4.7_rc3` to a `4.7-stable` asset), the applier now invokes an agentic LLM fixer
  to repair the ebuild in place and retries the manifest once. The fixer is wired
  automatically whenever `autoupdate.llm.provider` is `claude-code` (the only
  provider that can edit files); it runs the local `claude` CLI scoped to the
  package directory (`--add-dir`) with a narrow tool allowlist (`Read`, `Edit`,
  `Write`, `Bash(pkgdev *)`, `Bash(wget *)`, …) and never uses
  `--dangerously-skip-permissions`. The success check is authoritative: bentoo
  re-runs its own `pkgdev manifest` and only treats the apply as recovered if that
  passes — otherwise the half-applied ebuild is rolled back as before. A recovered
  apply is reported with a `Fixed:` line summarising the change. Spend is bounded
  by `autoupdate.llm.max_budget_usd` and a 10-minute per-invocation timeout.
  The agent is QA-aware: the bentoo "10 ebuild gotchas" are injected into its
  system prompt (`--append-system-prompt`, so the guidance survives `--bare`), it
  is offered the `/bentoo` skill when the session resolves it, and after a
  successful fix an advisory `pkgcheck` pass runs and any findings are surfaced on
  a `QA:` line for human review before committing (never blocking the apply).

## [0.6.0] - 2026-06-14

### Added
- **`autoupdate --check --revivable`.** A new flag that folds the revivable-orphan
  scan into a normal `--check` run: after checking the active packages, it also
  reports disabled-and-absent entries whose upstream is newer than `::gentoo`
  (the same data `--revive-list` produces), reusing the check's fetch pass. It is
  read-only and best-effort — a `::gentoo` provider failure only warns and never
  changes the check's exit code. `--revive-list` (standalone) and `--revive`
  (the mutating action) are unchanged.

### Changed
- **Env-based GitHub token resolution is now consistent.** `GH_TOKEN` is honoured
  everywhere as a fallback to `GITHUB_TOKEN` (the gh CLI convention) — previously
  only the autoupdate checker honoured `GH_TOKEN`, while `overlay compare` and the
  revive `::gentoo` provider read `GITHUB_TOKEN` only. A single
  `github.TokenFromEnv()` is now the shared source of truth.
- **Config load warns on unknown keys.** `gopkg.in/yaml.v3` silently drops keys
  that map to no struct field (e.g. a `token` placed under `overlay:` instead of
  `github:`), leaving requests unauthenticated with no hint. A strict re-decode
  now surfaces such mistakes as a stderr warning; it never blocks loading.

## [0.5.1] - 2026-06-14

### Fixed
- **`autoupdate --revive-list` no longer flags disabled-but-present packages.**
  `FindRevivableOrphans` only checked `enabled = false`, so a package that was
  manually disabled (or re-added after being auto-disabled) while its ebuild was
  still in the overlay was wrongly reported as revivable — and reviving it would
  seed an older `::gentoo` base over the newer overlay ebuild. The scan now also
  requires the package to be genuinely absent (`ErrNoEbuildFound`); checking the
  overlay first also skips the upstream/`::gentoo` lookups for present packages.
  Found by a smoke test against a real overlay (`dev-util/claude-code`, disabled
  but present at a version ahead of `::gentoo`).

## [0.5.0] - 2026-06-14

### Added
- **Generic auxiliary-variable substitution in autoupdate.** Two new optional
  `packages.toml` fields, `aux_var` and `aux_pattern`, keep a free-text ebuild
  variable (e.g. `MY_BUILD="esr-bb24"`) in sync with upstream. Unlike the
  existing `commit_sha_path` path — which is locked to `parser="json"` and a
  40-hex SHA — this is parser-agnostic (regex/html) and carries any captured
  value. `aux_pattern` (one capture group) is applied to the same response body
  used for version detection; the captured value is stored on the pending update
  and substituted into the copied ebuild at apply time, before the manifest
  step. Unblocks recurring manual bumps such as `mail-client/betterbird-bin`
  (`MY_BUILD` esr-bbNN tag) and nomachine's build-numbered `MY_P`. The two
  fields are mutually required and `aux_pattern` must compile.
- **Revive orphaned packages in autoupdate.** When a package is removed from the
  overlay, the autoupdate checker disables its `packages.toml` entry; but upstream
  can later move ahead of `::gentoo`, and a disabled entry is skipped forever, so
  that bump is lost. Two new modes close the gap:
  `bentoo overlay autoupdate --revive-list` is a passive report of disabled
  entries whose upstream is strictly newer than the highest version `::gentoo`
  still carries (no mutation); `--revive <pkg|all>` performs the full revive —
  seed the current `::gentoo` ebuild (plus `metadata.xml` and `files/`) into the
  overlay, re-enable the entry, and bump to the upstream version via the existing
  check+apply flow (so `aux_var`/commit substitution and the manifest step come
  for free). The `::gentoo` source is resolved exactly as `overlay compare` does
  (config repos > registry, honouring a local/clone repo); an API-only `::gentoo`
  cannot seed a base ebuild and aborts with an actionable hint to configure a
  local gentoo repository. Each package is independent — one failure never aborts
  the rest, and the run exits non-zero if any failed.

## [0.4.2] - 2026-06-13

### Security
- **Secret scanning in CI.** A new `secrets` job runs the `gitleaks` binary
  (`go install …@v8.30.1`, pinned) over the full git history on every push and
  PR. It uses the binary rather than `gitleaks-action`, which requires a
  `GITLEAKS_LICENSE` for GitHub organizations. A `.gitleaks.toml` adds a narrow
  allowlist for the `BENTOO_T13_*_KEY` env-var *names* used in tests (they are
  names, not secret values), and `.pre-commit-config.yaml` wires the same hook
  for local scans. The full history scanned clean (0 real leaks across 179
  commits).
- **Dependabot release cooldown.** Both ecosystems (`gomod`, `github-actions`)
  now quarantine freshly published versions for 7 days before opening a bump PR,
  dodging the window when most hijacked/yanked packages are caught.
- **Workflow static analysis (zizmor).** A new `workflow-lint` job runs
  `zizmor` (pinned `1.25.2` via `pipx`) against the workflows to catch template
  injection, excessive permissions, credential persistence, and unpinned
  actions.
- **CI least-privilege hardening.** Added a top-level `permissions:
  contents: read` (every job now starts with the minimum) and
  `persist-credentials: false` on every `actions/checkout` (the `GITHUB_TOKEN`
  is no longer left behind in `.git/config`) — the findings zizmor surfaced.

### Changed
- **`govulncheck` hardened in CI.** Pinned to `@v1.3.0` (was `@latest`) and the
  fragile `grep`-based output parsing was replaced with a `jq` filter over the
  `-json` stream that fails the job only on *reachable* third-party
  vulnerabilities (`stdlib`/`toolchain` issues, which need a Go update rather
  than a dependency bump, are excluded).
- **Bumped indirect dependency `go.mongodb.org/mongo-driver`** v1.17.4 →
  v1.17.9. `go mod tidy` also pruned orphan `go.sum` entries and reclassified
  `github.com/spf13/pflag` from indirect to direct (it is imported directly).
  `govulncheck ./...` reports no known vulnerabilities; all direct dependencies
  are current.

## [0.4.1] - 2026-06-11

### Fixed
- **Runner: cancelled subprocesses can no longer stall `Wait`.** `execRunner`
  now sets `WaitDelay`, so when a context is cancelled and an orphaned
  grandchild (e.g. a shell pipeline stage) keeps the stdout/stderr pipes open,
  the wait is forced closed after 1s instead of blocking until the orphan
  exits. Surfaced by `TestExecRunner_ContextCancelKills` flaking on CI.
- **`snapshot status`: guard against negative `Bsize`** from `statfs` before
  the `uint64` conversion in the available-space calculation (gosec G115).
- Lint cleanups across snapshot tests (inferred types, De Morgan, simplified
  compile-time check) and justified `//nolint:gosec` on the conventional
  0755 `/etc` directories (portage build user must traverse `bashrc.d`).

### Changed
- Bumped indirect dependencies: `golang.org/x/net` v0.56.0, `golang.org/x/sys`
  v0.46.0, `golang.org/x/text` v0.38.0, `cascadia` v1.3.4, `golang-set` v2.9.0,
  `go-json-experiment/json` (2026-06 snapshot). `govulncheck` (default and
  `playwright,chromedp` tags) reports no known vulnerabilities.

## [0.4.0] - 2026-06-10

### Added
- **`bentoo snapshot` command group (Phase 1).** Declarative btrfs snapshot
  management driven by a single `snapshot.toml`, orchestrating mature tools
  rather than reimplementing them. bentoolkit renders native config and
  coordinates the tools; it never calls `btrfs` directly.
  - **Config model** (`internal/snapshot/config.go`): TOML parsing with
    system-scope path resolution (`/etc/bentoo/snapshot.toml` preferred, XDG
    fallback) and a `Validate()` that fails hard on unknown
    `engine.driver`/`ship.type`/`schedule.backend` (`ErrInvalidDriver`) before any
    side effect, and warns-but-continues on non-fatal issues (empty subvolumes).
  - **Four orchestration interfaces** — `Engine`, `Shipper`, `Notifier`,
    `Scheduler` — selected by a factory, with an injectable `Runner`/`execCommand`
    seam so drivers are tested without a real btrbk/systemd/btrfs.
  - **`btrbk` engine**: renders `btrbk.conf` (retention → `snapshot_preserve`/
    `target_preserve`), and `Create`/`Prune`/`List` via `btrbk run`/`clean`/`list`.
  - **`ssh` shipper**: contributes its `target` to the btrbk.conf so btrbk
    performs send/receive (no bytes moved in Go).
  - **systemd scheduler**: golden-tested `.service` (`Type=oneshot`,
    `PrivateMounts=yes`) + `.timer` (`OnCalendar`/`Persistent`/`RandomizedDelaySec`)
    generation, installed atomically with `daemon-reload` + `enable --now`.
  - **Dependency detection** at validate-time: a missing driver binary yields an
    actionable error naming the Portage package (e.g. `app-backup/btrbk`).
  - **CLI verbs** `apply` / `run` / `list` / `status`, with a `Manager` pipeline
    (engine → prune → ship) that accumulates a `RunResult` persisted under
    `/var/lib/bentoo/snapshot/last-run.json`.
  - Every subprocess runs via `exec.CommandContext` so a cancelled context
    (SIGINT/timeout) kills in-flight children; generated files are written
    atomically (temp + rename).
- **Snapshot run notifications.** The Phase 1 `Notifier` hook gains real backends,
  selected and fanned out from a `[notify]` section (`internal/snapshot/notify.go`):
  - **ntfy** (`[notify.ntfy]`): POSTs a run summary to a topic URL — elevated
    priority + alert tag on failure, normal priority on success; an optional `token`
    is sent as a Bearer header.
  - **healthchecks.io** (`[notify.healthchecks]`): pings the base `ping_url` on
    success and `ping_url/fail` on failure, with an optional pre-run `ping_url/start`
    ping wired through a best-effort `Manager` hook.
  - **webhook** (`[notify.webhook]`): POSTs the serialized `RunResult` as JSON with
    any custom `headers` applied.
  - `on` selects which outcomes notify (`success`/`failure`; default: failure only).
    Delivery is **best-effort** — a failing backend is logged as a warning, never
    changes the run's exit code, and does not stop the others. Tokens and webhook
    secrets travel only in request headers and are **never logged or put in errors**.
    All backends share one bounded, context-aware `http.Client`
    (`httputil.BuildTransport`, capped response body, descriptive User-Agent).
- **Cloud backup + restore (Phase 3).** Two off-site `Shipper` drivers and a
  `restore` verb, on the same config/schedule as local snapshots.
  - **`restic` shipper** (`internal/snapshot/ship_restic.go`): backs up a transient
    **read-only snapshot mount** via `restic backup --tag bentoo,<subvol>
    --compression <auto|max|off>`, then maps `[engine.retention]` to
    `restic forget --prune`. A `mounter` seam guarantees the RO mount is unmounted
    even when the backup errors. Repo/password travel as `--repo`/`--password-file`
    **paths** (the 004 `Runner` has no env), never as secret values.
  - **`archive` shipper** (`internal/snapshot/ship_archive.go`): streams
    `btrfs send [-p parent] | zstd | rclone rcat <remote>/<obj>` under one cancellable
    context; any stage non-zero fails the ship. `mode = "incremental"` (default) uses
    `-p` when a recorded parent exists, else warns and sends full (no silent fallback).
  - **Parent tracking** (`internal/snapshot/parent.go`): persists the last
    successfully shipped snapshot per `(subvolume, ship)` under
    `/var/lib/bentoo/snapshot/parents/` (atomic writes) — recorded **only on success**
    so a failed ship never breaks the incremental chain.
  - **Archive GFS retention**: after a successful ship, `rclone lsjson` → a
    grandfather-father-son policy from `[engine.retention]` → `rclone deletefile` the
    losers, **never** the active parent.
  - **`bentoo snapshot restore <id> --target <path> --ship <name> [--yes]`**
    (`internal/snapshot/restore.go`, `cmd/bentoo/snapshot_restore.go`): dispatches by
    driver — archive **validates the full+delta chain before** `btrfs receive` and
    refuses a broken chain; restic does a granular `restic restore --target`.
    Destructive restores require `--yes` or an interactive `[y/N]` confirm.
  - Detection adds `restic` → `app-backup/restic` and `archive` → `net-misc/rclone`
    at validate-time. All subprocesses run via `exec.CommandContext`; secrets stay
    out of argv/logs. Drivers are covered by mock `Runner`/`mounter`/`parentStore`
    tests (no real btrfs/restic/rclone).
- **Snapshot rollback via snapper (Phase 4).** A second `Engine` driver for local
  timeline snapshots and **system rollback** — the "undo a broken update" path
  complementing btrbk's off-site replication. Selected with `engine.driver =
  "snapper"`; the btrbk engine is untouched (additive driver).
  - **`snapper` engine** (`internal/snapshot/engine_snapper.go`): `Create` runs
    `snapper -c <config> create --description "bentoo snapshot"
    --cleanup-algorithm timeline --print-number`; `List` parses the
    pipe-separated `snapper list` table into `[]Snapshot` (skipping the header
    and the `current` pseudo-snapshot); `Prune` delegates retention to
    `snapper cleanup timeline` — the GFS counts live in the rendered config's
    `TIMELINE_LIMIT_*` keys, native retention as with btrbk.
  - **Snapper config rendering** (`internal/snapshot/snapper_config.go`):
    `apply` ensures `/etc/snapper/configs/<name>` per managed subvolume
    (`/` → `root`, `/home` → `home`) idempotently — managed keys (`SUBVOLUME`,
    `TIMELINE_*`, `NUMBER_CLEANUP`) are updated in place, user settings and
    comments are preserved, nothing is duplicated. The engine-config write is
    now driver-aware (`WriteEngineConfig`): btrbk renders `btrbk.conf`, snapper
    ensures its configs — `apply` and `run` dispatch accordingly.
  - **`bentoo snapshot rollback <id>`** (`internal/snapshot/rollback.go`,
    `cmd/bentoo/snapshot_rollback.go`): runs `snapper -c root rollback <id>`.
    Destructive, so it requires `--yes` or an interactive `[y/N]` confirm, and
    it is **refused with a clear error when the active engine is not snapper**
    (the guard fires before the confirm prompt; declining is a clean exit-0
    abort, mirroring `restore`).
  - **Opt-in emerge hook** (`internal/snapshot/hook.go`,
    `cmd/bentoo/snapshot_hook.go`): `bentoo snapshot hook --install` writes
    `/etc/portage/bashrc.d/50-bentoo-snapshot.sh` — `pre_pkg_setup`/
    `post_pkg_postinst` phase functions creating snapper **pre/post pairs** per
    package (`--cleanup-algorithm number`, pruned by the managed
    `NUMBER_CLEANUP="yes"`) — and wires it through a marker-delimited managed
    block in `/etc/portage/bashrc` (user content preserved; install is
    idempotent). `--uninstall` removes both cleanly. The hook is **never**
    installed by `apply` (asserted by test); snapper failures never break an
    emerge (`|| true` guards).
  - Detection adds `snapper` → `app-backup/snapper` at validate-time (the
    story text said `app-admin/snapper`; the real Gentoo category is
    `app-backup`). grub-btrfs / boot-into-snapshot integration is documented as
    a follow-up, not implemented here.
- **Snapshot packaging & polish (Phase 5).** Closes the snapshot feature:
  every backend optional at install time, every verb previewable, retention
  on demand, and full visibility into timers and remotes.
  - **Email notifier** (`internal/snapshot/notify.go`, `[notify.email]`):
    sends the run summary to the configured recipients via local `sendmail -t`
    (through the `Runner` seam) or direct SMTP (stdlib `net/smtp`, PLAIN auth
    when `user`/`password` are set, addr via `net.JoinHostPort`). Plugs into
    the composite notifier, respects `notify.on`, and the SMTP password is
    **never** placed in argv, logs, or error strings.
  - **Full `--dry-run` coverage** (`internal/snapshot/plan.go`,
    `cmd/bentoo/snapshot_*.go`): `apply` prints the engine config and systemd
    units it would write; `run` prints the engine → prune → ship pipeline it
    would execute; `restore`/`rollback`/`prune` print the destructive actions —
    all with **zero side effects** (no subprocess, no writes, no state, no
    confirm prompt), asserted by spy-based tests per verb. Plan lines come from
    pure helpers (`PlanApply`/`PlanRun`/`PlanPrune`).
  - **`bentoo snapshot prune [--ship NAME] [--dry-run]`**
    (`cmd/bentoo/snapshot_prune.go`, `Manager.Prune`): applies
    `[engine.retention]` on demand — engine-native prune (`btrbk clean` /
    `snapper cleanup timeline`) per subvolume plus the archive GFS per archive
    ship (`PruneRemoteOnDemand`, guarding every recorded active parent from the
    parent store). `--ship` scopes to one destination (engine prune skipped);
    unlike the best-effort post-ship GFS, a manual prune **reports failures**.
  - **Status & list polish** (`cmd/bentoo/snapshot_status.go`,
    `snapshot_list.go`): `status` now breaks the last `RunResult` down **per
    stage** (stage, subvolume, target, outcome, error), reports the timer's
    **next scheduled run** via `systemctl list-timers`, and free space
    (snapshot dir + local ship targets). `list --remote` merges **btrbk target
    backups** (`btrbk list backups`) and **restic snapshots**
    (`restic snapshots --json`) into the listing, clearly labeled, with
    per-source lenient errors (`Manager.ListRemote` → `[]RemoteGroup`).
  - **Ebuild USE flags** (bentoo overlay, `app-portage/bentoolkit`):
    `IUSE="btrbk snapper restic rclone systemd"` mapping each optional backend
    to its conditional `RDEPEND` (`app-backup/btrbk`, `app-backup/snapper`,
    `app-backup/restic`, `net-misc/rclone`, `sys-apps/systemd`) — mirroring
    `internal/snapshot/detect.go` exactly — plus `metadata.xml` flag
    descriptions (including the previously undescribed `browser` flag).
    pkgcheck-clean; `detect` keeps naming the exact missing package at runtime.

## [0.3.21] - 2026-06-05

### Fixed
- **`BUILD_ID` substitution for version-tracked packages (cursor 403).** Cursor
  embeds a per-release `commitSha` in its `SRC_URI` via `BUILD_ID`. The
  autoupdate bumped only `PV`, leaving `BUILD_ID` stale, so the `.deb` URL mixed
  the old build id with the new version and returned **403 Forbidden**. A
  version-tracked package may now set `commit_sha_path` (requires `parser="json"`);
  the checker resolves the SHA from the same JSON response into the pending
  update, and `substituteCommitHash` rewrites `BUILD_ID="<40hex>"` at apply time.
  Verified end-to-end against the live cursor API (`3.6.31 → 3.7.12`): the
  manifest fetch now succeeds where it previously 403'd.

## [0.3.20] - 2026-06-05

### Added
- **Preserve the `_pre` suffix for commit-tracked snapshot packages.** A new
  `extractSnapshotSuffix` helper detects whether the current ebuild uses `_pre`
  (pre-release) or `_p` (post-release) and reuses it when building the new
  version, so commit-tracked `_pre` packages keep the correct Gentoo ordering
  (`X.Y_pre<date>` < `X.Y` < `X.Y_p<date>`). The `AllSnapshotPackages` table is
  extended with `zed`, `mesa`, `mesa_clc` and `libqmi`.

### Changed
- **Bumped `github.com/chromedp/cdproto`** to `20260427013145`; `go mod tidy`
  promotes it to a direct dependency. Build, vet, tests and `govulncheck` pass.
- Minor lint cleanups in `internal/autoupdate`: write ebuilds with `0o600`
  permissions in `substituteCommitHash` (gosec), and drop an unused `fmt`
  import / needless `Sprintf` in the commit-track tests.

## [0.3.19] - 2026-06-05

### Added
- **`track = "commit"` mode for `_p` snapshot packages in `packages.toml`.**
  Packages versioned as `X.Y.Z_p<date>` (post-release snapshots) can now be
  tracked by commit instead of by tag. Setting `track = "commit"` on a package
  entry makes the checker fetch the latest commit on a branch (GitHub or GitLab
  commits list API), extract the commit date as the new `_p<YYYYMMDD>` suffix,
  and store the commit SHA for substitution at apply time. Cache reads are
  bypassed for commit-tracked packages so the SHA is always current.

- **Automatic base-version detection from commit titles
  (`commit_version_pattern` + `commit_message_path`).** When a snapshot
  package declares these two fields, the checker scans all returned commit
  titles and extracts a version from the first match. If the detected version
  is newer than the current base (e.g. a commit titled *"Update for
  Vulkan-Docs 1.4.353"* while the ebuild is at `1.4.352_p…`), the new version
  is built from the detected base (`1.4.353_p<today>`) rather than the stale
  one. The base is never downgraded: a commit mentioning an older version is
  ignored. This covers the common Khronos pattern where a Vulkan SDK or
  Vulkan-Docs version bump appears in the commit stream days or weeks before
  the upstream tag is cut.

- **Commit-hash substitution at apply time.** `PendingUpdate` now carries a
  `CommitHash` field. When `--apply` runs on a commit-tracked package, a new
  `substituteCommitHash` step rewrites the commit variable in the copied ebuild
  before the manifest step. Handles the three variable forms used in the
  overlay: `EGIT_COMMIT="<sha>"`, `GIT_COMMIT="<sha>"`, and `COMMIT=<sha>`
  (bare, no quotes — used by `dev-db/sqlitebrowser`).

- **22 new tests** in `checker_commit_track_test.go` covering:
  `extractSnapshotBase` (8 cases), `scanCommitsForVersion` (6 cases including
  GitHub and GitLab formats, invalid JSON and bad regex), `CheckPackage` commit
  tracking (date-only bump, base-version bump via commit title, no-update,
  SHA persistence in pending, cache-bypass guarantee), and a table-driven suite
  with one sub-test per `_p` package (glslang, spirv-headers, spirv-tools,
  vulkan-headers, vulkan-tools, vulkan-layers, vulkan-loader, sqlitebrowser,
  modemmanager), plus edge-case tests for downgrade protection and the GitLab
  `+00:00` timezone format.

## [0.3.18] - 2026-06-04

### Added
- **`autoupdate` auto-disables orphaned packages instead of erroring forever.**
  When a package's ebuild is removed from the overlay, the checker used to fail
  every run with `failed to get current version: no ebuild file found`. It now
  detects the orphan, sets `enabled = false` for that entry in `packages.toml`,
  and surfaces it as an informational `no ebuild in overlay — disabled` line
  rather than a recurring failure (so it no longer pollutes the exit code).
  Subsequent runs skip the entry silently. The edit is surgical — it inserts a
  single `enabled = false` line into the affected section, preserving every
  comment, ordering, and formatting in the hand-maintained file (unlike a full
  TOML re-encode). Applies to both `--check` (batched, one atomic write) and the
  single-package path.

## [0.3.17] - 2026-06-03

### Fixed
- **`autoupdate --apply` no longer fails on stale pending entries.** The applier
  trusted the `current_version` recorded at check-time to locate the source
  ebuild; when the overlay had since drifted — the package bumped further by
  hand, or removed entirely — that file was gone and Apply died with a cryptic
  `source ebuild file not found`. Apply now re-resolves the current version
  against the live overlay (mirroring the checker's selection): a stale-but-
  present `current_version` self-heals to the real source, and a genuinely
  obsolete entry (package removed, or overlay already at/beyond the target) is
  **pruned from `pending.json`** and reported as `Obsolete (pruned)` rather than
  counted as a failure. `--apply all` gains an `Obsolete: N` summary line and no
  longer exits non-zero solely because of superseded entries.

## [0.3.16] - 2026-06-03

### Added
- **Live test covering the `~/.config/bentoo/secrets` serial path for
  authenticated distfile fetch.** `TestFetchDistfileLiveFileZillaProSecretsFile`
  blanks `FILEZILLA_PRO_KEY` so `resolveSecret` must fall back to the secrets
  file, then runs the real FileZilla Pro POST and asserts a non-trivial binary
  comes back — proving a user-configured serial is accepted end-to-end. Gated
  on `FILEZILLA_SECRETS_E2E=1`, so it never runs in CI. Test-only — no runtime
  or binary behavior changes.

## [0.3.15] - 2026-06-03

### Fixed
- **CI: `TestApply_CancelsOnContextCancellation_Compile` no longer flakes on
  slow runners.** The test slept a fixed 200ms before cancelling, assuming the
  `Apply` goroutine had cleared the instant manifest step and was blocked in
  compile; on a loaded CI runner it had not, so the cancel aborted the manifest
  instead and the assertion failed. It now waits on a deterministic signal (the
  exec factory closes a channel the first time it builds a compile command,
  which only happens after the manifest step returns) before cancelling, so the
  cancellation can only ever hit the compile step. Test-only change — no runtime
  or binary behavior is affected.

## [0.3.14] - 2026-06-03

### Added
- **Per-package `enabled` toggle in `packages.toml`.** Each entry may now set
  `enabled = false` to be skipped silently by `overlay autoupdate --check` — no
  network fetch, and absent from progress output and the run totals — without
  deleting its configuration. An absent/`true` value means enabled (the
  default), so existing configs need no migration. This is the clean way to
  park an orphaned entry (e.g. a package whose ebuild was removed from the
  overlay but whose check config is worth keeping). `CheckAll` filters disabled
  packages up front alongside the `--only` type filter; `CheckPackage` (an
  explicitly named package) is intentionally unfiltered, treating an explicit
  name as a conscious override.
- **Authenticated distfile fetch for serial-gated packages.** Some commercial
  packages (e.g. `net-ftp/filezilla-pro`) gate their distfile behind a
  serial/registration key, so `pkgdev manifest` cannot fetch it from `SRC_URI`.
  A package's free-form `[meta]` block can now drive an authenticated download:
  before the manifest step `--apply` submits the vendor's download form (POST or
  GET) with the serial injected, and drops the file into pkgdev's private
  `--distdir` so it digests the local copy. The serial is **never** stored in
  the overlay — it is resolved at runtime from an env var or
  `~/.config/bentoo/secrets` and scrubbed from every log line and error message.
  HTML/zero-byte responses and unsafe filenames are rejected; the download is
  context-bounded so SIGINT cancels it. Packages without a `[meta]` fetch spec
  (the overwhelming majority) follow the normal pkgdev-from-`SRC_URI` path
  unchanged.

## [0.3.13] - 2026-06-03

### Changed
- **`overlay autoupdate --check` is now dramatically faster on GitHub/GitLab-heavy
  overlays.** The HTTP rate limiter gained per-host policies: GitHub hosts run at
  ~10 req/s (100ms) and GitLab at ~3.3 req/s (300ms) — the two providers that
  dominate `packages.toml` — while every other host keeps the conservative
  1-req/6s default. Previously a single uniform 1-req/6s-per-host limit serialised
  the ~220 GitHub/GitLab packages regardless of `--concurrency`, capping a full
  check at ~13 min; with per-host tuning it completes in well under a minute.
  The default `--concurrency` was raised from 10 to 20 to keep the tuned limiters
  saturated (max remains 100). New `RateLimiter` options `WithHTTPInterval`,
  `WithHostPolicy` and `WithTunedHostPolicies` expose the tuning; the zero-config
  limiter keeps its uniform 6s-per-host behavior.

## [0.3.12] - 2026-06-03

### Added
- **`overlay autoupdate` now classifies each package as binary or source and can
  filter on it.** A new optional `type = "bin" | "source"` field in
  `packages.toml` records a package's kind; when omitted it is auto-detected from
  the current ebuild (`RESTRICT="bindist"`, a `-bin` suffix, or a binary
  `SRC_URI`) via the existing `detectBinaryPackage` heuristic, so existing
  configs need no change and only override/correction cases set it explicitly.
  `--check` now tags every result line (`[bin]`/`[src]`) and prints a
  `Checked N source, M bin` summary, and a new `--only=bin|source` flag restricts
  the batch to one kind — filtered packages are skipped *before* any network
  fetch. An unrecognized `type` value (in `packages.toml`) or `--only` value
  fails fast rather than silently checking everything. `CheckResult` gains a
  `Type` field; classification is metadata only and does **not** change
  apply/compile behavior.

## [0.3.11] - 2026-06-03

### Fixed
- **`overlay autoupdate --apply` now regenerates the Manifest without root or
  Portage write access.** The apply step ran `ebuild <path> manifest`, which
  inherits the system `DISTDIR` (`/var/cache/distfiles`) and tries to *write*
  the fetched `SRC_URI` distfiles there. As an unprivileged user this failed
  with `No write access to '/var/cache/distfiles'`, so every apply aborted
  before updating the Manifest (`ebuild manifest command failed`). `runManifest`
  now mirrors the `overlay manifest` subcommand: it creates a private writable
  distdir (`os.MkdirTemp`, removed when the step returns) and runs
  `pkgdev manifest --distdir <tmpdir>` from the package directory. `pkgdev`
  neither requires root nor touches the system `DISTDIR`, so the manifest step
  works as a regular user. Timeout, context cancellation and orphan-ebuild
  rollback are unchanged.

## [0.3.10] - 2026-06-03

### Fixed
- **`overlay add <pkg>` now reports only what it staged, not the whole working
  tree.** After staging, the command printed `overlay.Status()` — a full
  `git status --porcelain` of the working tree — so every modified package in the
  overlay appeared in the output, making it look like `add <pkg>` had staged
  everything. The index was actually correct (only the chosen paths were staged,
  and `overlay commit` uses `git commit` with no `-a`, so only staged changes are
  committed); the feedback was the only thing misleading. Root cause: the status
  parser collapsed the porcelain `XY` columns with `TrimSpace`, discarding the
  staged-vs-unstaged distinction. Added `GitRunner.StagedStatus()` /
  `ParseStagedStatusOutput` (keyed on the index column `X`; untracked and
  worktree-only entries are dropped and staged renames are split into delete+add,
  matching the existing parser) and a matching `overlay.StagedStatus`; `runAdd`
  now displays that. The no-argument `overlay add` (equivalent to `git add .`)
  still lists everything, since there everything truly is staged.

## [0.3.9] - 2026-06-03

### Added
- **`overlay autoupdate --check` now shows live progress.** The check fans out
  concurrently and previously printed nothing until the final results table,
  leaving the terminal silent through the whole network/LLM phase. `runCheck`
  now wires the Checker's existing `WithProgressCallback` (until now never
  connected) to a self-rewriting `Checking: [pct%] done/total` line, mirroring
  `overlay compare`. The counter is driven by `CheckAll`'s atomic counter, so it
  stays monotonic despite the concurrent workers; the line is cleared before the
  results table and suppressed under `--quiet`.

## [0.3.8] - 2026-06-03

### Fixed
- **`make audit-ctx` (CI) no longer fails on the chromedp backend.** The
  `context.Background()` that roots the chromedp browser allocator in
  `script_evaluator_chromedp.go` (added in 0.3.6) lacked the `// SAFE:`
  justification the context-spine audit requires, so the `audit-ctx` job failed
  with *"naked context.Background() found"*. Annotated it like the other
  intentional root contexts (`applier.go`, `analyzer.go`, …); no behavior change.

## [0.3.7] - 2026-06-03

### Added
- **`overlay autoupdate --apply --clean` (`-c`) removes the old ebuild after a
  successful apply.** With the flag, once the new version is created, manifested
  and the pending entry cleared, `Apply` deletes the previous version's ebuild
  (the one it bumped from) and regenerates the Manifest so the now-orphaned
  distfile entries are pruned — leaving only the freshly created version, the way
  a manual version bump ends. It is best-effort: a removal or manifest-prune
  failure is surfaced as a `Clean:` warning on the result but never flips the
  apply to failed, since the update itself is already done. The removed version
  is reported as `Removed: <pkg>-<old>.ebuild`. Works with both
  `--apply <pkg> --clean` and `--apply all --clean`.

## [0.3.6] - 2026-06-03

### Added
- **`chromedp` backend for the `parser="script"` headless-browser path.** A new
  `liveEvaluator` implementation in `script_evaluator_chromedp.go` (built with
  `-tags chromedp`, mutually exclusive with `playwright` via
  `//go:build chromedp && !playwright`) drives the system Chrome/Chromium
  directly over the DevTools Protocol — no Node.js driver and no
  `playwright install` step. `chromedp.Evaluate(..., WithAwaitPromise(true))`
  reaches parity with Playwright's `page.Evaluate`, including resolving an
  `(async () => {...})()` IIFE to its string result. The integration test
  (`-tags chromedp -run Integration`) mirrors the Playwright one. `go mod tidy`
  without a tag still prunes both browser deps, so `chromedp` and `playwright-go`
  are pinned as direct requires.

### Fixed
- **`overlay autoupdate --apply` no longer produces invalid ebuild filenames for
  upstreams whose version carries a tag prefix.** When the detected `NewVersion`
  came from a git tag like `v9.2.0588`, `Apply` used it verbatim to build the
  destination ebuild name (`vim-v9.2.0588.ebuild`), which Portage rejects with
  *"does not follow correct package syntax"* — failing the manifest step for vim,
  vim-core, bind-tools, nodejs, ollama, ollama-bin, bisq-bin, etc. `Apply` now
  strips the prefix (`stripVersionPrefix` + trim) and validates the result with
  `ebuild.IsValidVersion` before touching the filename, surfacing a clear
  `ErrInvalidNewVersion` for non-versions (e.g. `latest`) instead of a cryptic
  Portage error.

## [0.3.5] - 2026-06-02

### Added
- **`overlay autoupdate --apply all` applies every pending update in one run.**
  Previously `--apply` accepted only a single exact `category/package`; any other
  value failed with `ErrPackageNotInPending`. The new `all` sentinel (safe because
  real package names always contain a `/`) reuses a single `Applier` and iterates
  over a snapshot of the pending list, applying each package independently. Each
  outcome is printed, followed by an `Apply All Summary` (`Applied: N` / `Failed:
  M`). Successfully applied packages leave `pending.json`; failures remain marked
  `failed`. The process exits non-zero when any package fails, matching the
  single-package contract. `--apply all --compile` still prompts per package.

## [0.3.4] - 2026-06-02

### Security
- **Bump the `go` directive to 1.25.11.** The Go 1.25.10 standard library is
  affected by [GO-2026-5037](https://osv.dev/GO-2026-5037) and
  [GO-2026-5039](https://osv.dev/GO-2026-5039), both fixed in 1.25.11. The
  `go.mod` directive drives the toolchain CI installs (via setup-go's
  `go-version-file`), so the bump clears the osv-scanner findings. No source
  changes; build and vet stay green.

## [0.3.3] - 2026-06-02

### Fixed
- **`overlay autoupdate --check` no longer fails en masse with HTTP 403.** The
  version-check hot path (`Checker.fetchContent`) issued requests via
  `GetWithContext`, which bypasses `applyHeaders` — so checks never sent a
  `User-Agent`, the configured GitHub token, or the per-package `headers` from
  `packages.toml`. Every request went out as an anonymous `Go-http-client/1.1`,
  and `api.github.com` (60 req/h per IP) answered `403` for the bulk of
  GitHub-backed packages. `fetchContent` now routes through
  `GetWithHeadersContext`, putting the User-Agent, `Authorization` token, and
  TOML-declared headers on the wire. The full batch check dropped from 68
  spurious 403s to 0.

### Added
- **GitHub API authentication for autoupdate.** New `WithGitHubToken` checker
  option, wired by `overlay autoupdate` from `~/.config/bentoo/config.yaml`'s
  `github.token`, with `GITHUB_TOKEN`/`GH_TOKEN` env taking precedence — the
  same resolution order `overlay compare` uses. Raises the GitHub API limit from
  60 to 5000 req/h, eliminating rate-limit 403s in the full batch check.
- **Default `User-Agent` (`bentoolkit/<version>`)** on the autoupdate HTTP
  client, avoiding the `Go-http-client/1.1` string that WAF-fronted upstreams
  reject outright.

## [0.3.2] - 2026-06-02

### Changed
- **Bump `github.com/mattn/go-colorable` from 0.1.14 to 0.1.15.** Routine
  maintenance update of an indirect dependency; no functional or security
  impact. A dependency audit (`govulncheck ./...`) reported no vulnerabilities
  in the reachable code, and all direct dependencies are already current.

### Notes
- The proposed `github.com/deckarep/golang-set/v2` 2.8.0 → 2.9.0 bump was
  intentionally **not** applied: 2.9.0 pulls in `go.mongodb.org/mongo-driver`
  (unused, not reachable) with no security benefit. The package stays pinned at
  2.8.0 (an indirect dependency of `playwright-go`).

### Internal
- Minor `gofmt` comment normalization in `internal/autoupdate/version_history.go`.

## [0.3.1] - 2026-06-02

### Security
- **Bump `github.com/go-jose/go-jose/v3` from 3.0.4 to 3.0.5**
  ([CVE-2026-34986](https://github.com/go-jose/go-jose/security/advisories/GHSA-78h2-9frx-2jm8),
  High / CVSS 7.5). Decrypting a JWE that uses a key-wrapping algorithm with an
  empty `encrypted_key` could panic, allowing a denial of service. The
  dependency is indirect and the upgrade is a drop-in patch (the module's
  dependency graph is unchanged). (#7)

### Fixed
- **CI `Lint` job restored to green.** Three pre-existing `staticcheck` findings
  were failing `golangci-lint run ./...`; because the `Build` job depends on
  `Lint`, this had been blocking release builds:
  - `newSelectExtractor` carried no-op `switch` cases whose pure
    `strings.HasPrefix` return values were discarded (SA4017). The redundant
    cases were removed; `"[*]"`-prefixed and non-indexed paths still pass
    through unchanged.
  - the non-bare `claude-code` test's empty guard now actually asserts that
    `ANTHROPIC_API_KEY` is not injected into the child process (SA9003): the
    child exits non-zero with a stderr marker so any leak surfaces as an error.
  - the deliberately nil context test case is annotated `//nolint:staticcheck`
    (SA1012).

## [0.3.0] - 2026-06-02

### Added
- **`transform`, `select`, and a `script` parser for `overlay autoupdate`.**
  Three extensions that let packages previously skipped for parsing limitations
  be tracked again:
  - **`transform`** applies ordered regex substitutions to the extracted version
    (e.g. imagemagick `7.1.2-24` → `7.1.2.24`, godot `-beta` → `_beta`),
    per-candidate and before the Gentoo comparison, so a raw upstream string that
    is not yet a valid version can be normalized.
  - **`select = "max" | "last"`** chooses among multiple matches instead of the
    first, reusing the version-history list extractors (JSON `[*]`, CSS, XPath)
    plus a new regex list extractor. The 10-item history cap is now parameterized
    (`-1` = unlimited) so `select="max"` is not defeated by truncation of an
    ascending list (gn).
  - **`parser = "script"`** evaluates JS against a live DOM for multi-step / SPA
    cases (LibreOffice's 3-segment dir → 4-segment tarball). It is backed by
    `playwright-go` behind the `playwright` build tag (`page.Evaluate` auto-awaits
    Promises); the default build returns `ErrScriptSupportNotBuilt`, keeping the
    browser dependency opt-in. `@file.js` scripts load from `.autoupdate/scripts/`
    with path-traversal protection.

  `ValidatePackageConfig` now accepts `parser="script"`, validates `select`, warns
  and ignores malformed `transform` rules, and warns that `transform`/`select` are
  ignored on the script path.
- **`claude-code` LLM provider + LLM wiring for `analyze` and `--check`.** A new
  `llm.provider: claude-code` drives the locally-installed `claude` CLI (Claude
  Code) headlessly (`claude -p … --output-format json`, page content on stdin)
  instead of the HTTP API, reusing your existing Claude Code login or an API key.
  Authentication is hybrid via the new `llm.bare` config (`auto`/`true`/`false`):
  `--bare` + `ANTHROPIC_API_KEY` (the cheap path) or the CLI's login/subscription
  session. The new `llm.max_budget_usd` caps per-call spend (`claude
  --max-budget-usd`). The provider defaults to the `sonnet` model alias and runs
  via `exec.CommandContext`, so SIGINT or the timeout kills the child process;
  page content is passed on stdin (never argv) and the API key never reaches argv,
  logs, or errors. `bentoo overlay analyze` now builds the configured provider for
  schema proposal, and `bentoo overlay autoupdate --check` now uses it to extract a
  version for packages that set `llm_prompt` (tried after the primary/fallback
  parsers). Both commands degrade gracefully: when the `claude` CLI is missing or
  unauthenticated they log a Warn and fall back (heuristic schema / skip
  extraction) rather than failing. Internally, the `Checker`'s LLM hook was
  refactored to accept the `LLMProvider` interface (previously Claude-HTTP-only),
  so any provider can be injected; the existing `claude`/`openai`/`ollama`
  providers are unchanged.
- **Robust LLM schema parsing via `flexString`.** A field the schema types as a
  string but a model emits in another shape no longer fails the whole parse:
  scalars (number/bool) are kept as text, `null` becomes `""`, and an object or
  array (returned by some models for e.g. `fallback_config`) is dropped to `""` —
  so one malformed secondary field can't discard an otherwise-valid schema
  proposal.

### Fixed
- **`overlay autoupdate --check` no longer fails packages that queue behind
  others on the same host.** `fetchContent` derived a single
  `opTimeout`-bounded context and used it for *both* the per-host rate-limiter
  wait *and* the HTTP request. A package queued behind several others on the
  same host could therefore burn the entire per-operation deadline while still
  waiting for a rate-limit token and fail with `context deadline exceeded`
  before any request was issued (observed with 13 packages sharing
  `gitlab.freedesktop.org`). The limiter wait now uses the parent
  (signal-aware) context; the `opTimeout` starts only after a token is acquired
  and bounds just the HTTP round-trip. SIGINT/SIGTERM still cancels the wait.

## [0.2.2] - 2026-06-01

### Fixed
- **`overlay autoupdate --check` now actually supports the `html` parser.**
  `Checker.fetchAndParse` built its parser with `NewParser`, which rejects
  `html` outright (`use NewParserFromConfig for html parser`) and has no way to
  carry the `selector`/`xpath` fields — so every package configured with
  `parser = "html"` failed at fetch time even though the parser, the config
  fields, and the README all advertised it. `fetchAndParse` now builds the
  parser via `NewParserFromConfig` and threads `selector`/`xpath` (plus the
  optional regex post-processing in `pattern`) through for both the primary and
  fallback URLs. This makes HTML scraping work end to end, including extracting
  a version from an element attribute via an XPath such as
  `(//a[contains(@href, '/linux-x64/cursor/')]/@href)[1]` with
  `pattern = "cursor/([0-9.]+)"`.
- **`overlay autoupdate --check` no longer silently reports "up to date" for a
  non-comparable upstream version.** `Checker.compareVersions` previously passed
  the raw upstream value straight to `ebuild.CompareVersions`, whose lenient
  `parseVersion` coerces any unparseable component to `0` — so an upstream tag
  like `INKSCAPE_1_4_4`, or even a `v`-prefixed `v7.0.0`, parsed to a near-zero
  version and compared as *older* than the current ebuild, masking real updates.
  `compareVersions` now normalizes both sides (trims whitespace, strips a
  leading `v`/`version-`/etc. prefix) and validates them with the new
  `ebuild.IsValidVersion`. When either side is not a well-formed Gentoo-style
  version, the result is flagged `CheckResult.NotComparable`: it is surfaced as a
  warning, excluded from the pending list, and never counted as "up to date".

### Added
- **`ebuild.IsValidVersion`** reports whether a string is a well-formed
  Gentoo-style version that `CompareVersions` can order meaningfully, so callers
  can reject junk (`latest`, upstream tag names) instead of comparing against a
  silently-zeroed version.

## [0.2.1] - 2026-05-22

### Changed
- Bumped indirect dependencies to their latest patch/minor releases:
  `golang.org/x/net` v0.54.0 → v0.55.0,
  `golang.org/x/sys` v0.44.0 → v0.45.0, and
  `golang.org/x/crypto` v0.51.0 → v0.52.0. No API changes; routine
  upstream fixes. `govulncheck` reports zero known vulnerabilities
  against the resulting module graph.
- `.gitignore` now ignores the entire `.epic/` directory; previously only
  `.epic/**/.draft/` and `.epic/archive/` were excluded. Epic plugin state
  is no longer versioned.
- **`llm_prompt` is documented as `analyze`-only; `--check` emits a Warn when
  the field is set.** The README previously implied `llm_prompt` worked under
  `--check`, but the live LLM branch in `Checker.fetchUpstreamVersion` is gated
  on a non-nil `llmClient` that the CLI has never wired. `NewChecker` now logs
  one Warn per package whose `llm_prompt` is set, identifying the package and
  pointing the user at `bentoo overlay analyze`. The struct field is retained
  so existing `packages.toml` files load unchanged.

### Fixed
- **`overlay autoupdate --apply` now honours SIGINT/SIGTERM.** The signal-derived
  context built by `runAutoupdate` is now threaded into `NewApplier` via
  `WithApplierContext`, so a Ctrl-C during `ebuild manifest` or the elevated
  compile step terminates the spawned child within ~2 s and triggers the
  existing orphan-rollback path. This closes the gap left by 0.2.0, whose
  CHANGELOG claimed SIGINT/SIGTERM cancelled in-flight HTTP requests and child
  processes — the claim now holds for both `--check` and `--apply`.
- **`autoupdate.cache_ttl` from `~/.config/bentoo/config.yaml` is now applied.**
  A new `WithCacheTTL` checker option carries the user-configured TTL through
  to `Cache.TTL`; previously the value was loaded into config but ignored, so
  cache entries always expired at the hardcoded 1-hour default.
- **`pending.json` clears after a successful `--apply`.** A package that
  completes the full apply path (copy + manifest, plus compile when
  `--compile`) is removed from `pending.json`, so `bentoo overlay autoupdate
  --list` no longer surfaces already-applied entries. Failures keep the entry
  for retry. A delete-after-success bookkeeping failure emits a Warn but does
  not flip `result.Success`.
- CI: silenced a `contextcheck` false positive on the `applier.Apply` call in
  `runApply`. The signal-derived context is propagated into the applier's
  spawned processes via `WithApplierContext` (`a.ctx`), not a `ctx` parameter,
  so the lint warning is annotated with an inline `//nolint:contextcheck`
  justification rather than altering `Apply`'s signature.

Validated with `go build`, `go vet`, `go test ./...`, and `govulncheck`
(0 vulnerabilities).

## [0.2.0] - 2026-05-17

### Added
- `--concurrency=N` flag on `overlay autoupdate` and `overlay compare` bounds
  the number of packages processed in parallel. Default `10`, valid range
  `[1, 100]`; a value outside the range fails fast with a clear error before
  any package work begins.
- Shared, tuned HTTP transport (`httputil.BuildTransport`) with connection
  pooling, replacing per-request ad-hoc transports across the autoupdate and
  provider HTTP paths.
- `BENTOO_DISABLE_HTTP2=1` environment variable opts the shared transport out
  of HTTP/2 (HTTP/1.1 only) for environments where an HTTP/2 proxy misbehaves.
- Git clone URL and branch validators, and LLM regex/XPath validation, run
  before the corresponding external invocation.
- Documented process exit codes for `overlay autoupdate`: `0` success, `1`
  partial failure, `2` total failure / invalid configuration.
- `goleak`-based goroutine-leak detection in the test suite.

### Changed
- **BREAKING:** `${VAR}` expansion in `packages.toml` header values is now
  allow-listed. It applies only when the header name (case-insensitive) is one
  of `Authorization`, `X-Api-Key`, `X-Auth-Token`, `Private-Token` **and** the
  variable is prefixed `BENTOO_` or is one of `GITHUB_TOKEN`, `GITLAB_TOKEN`,
  `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`. A non-allow-listed `${VAR}` is now
  passed through literally with a `Warn` instead of being expanded — rename
  such variables to add the `BENTOO_` prefix.
- **BREAKING:** `overlay autoupdate` now exits `1` on partial failure (at least
  one package failed and at least one succeeded); previously it exited `0`.
- **BREAKING:** the `ProgressCallback` signature is now
  `func(done, total uint64)`.
- **BREAKING:** `CheckAll` / `AnalyzeAll` now return a `BatchResult`, separating
  successful items from per-package failures.
- Cache files and the apply-log are now written with mode `0600` (was `0644`).
- HTTP/2 is now enabled by default on the shared transport.

### Security
- Env-var header-expansion allow-list prevents a malicious or mistaken
  `packages.toml` from exfiltrating arbitrary process secrets through a
  non-auth header or an arbitrary variable name.
- Git clone URL and branch validation rejects unsafe inputs such as `file://`
  URLs and argument/flag injection.
- HTTP response bodies are capped at 10 MiB; an oversized body now fails with
  `ErrResponseTooLarge` instead of being read unbounded.

### Fixed
- An orphan `.ebuild` left behind when `ebuild manifest` fails is now rolled
  back.
- Per-package errors in batch operations are no longer silently swallowed; the
  `//nolint:errcheck` directive that hid them was removed.
- The rate limiter is now actually invoked on the HTTP hot path.
- `git clone` and `ebuild manifest` invocations now run under a timeout.
- `SIGINT`/`SIGTERM` now cancels in-flight HTTP requests and child processes.

Validated with `go test -race ./...`, `golangci-lint run`,
`govulncheck ./...`, and `make audit-ctx`.

## [0.1.11] - 2026-05-15

### Changed
- CI: bumped `actions/checkout` v4 → v6.0.2 and `actions/setup-go` v5 →
  v6.4.0 to run on Node 24 ahead of GitHub's Node 20 removal
  (scheduled 2026-09-16); both actions are now pinned to their commit
  SHAs (with the version tag in a trailing comment) for supply-chain
  hardening. `google/osv-scanner-action` was likewise bumped v2.0.2 →
  v2.3.8 and SHA-pinned, and the removed `--skip-git` flag was dropped
  (not scanning the git root is the v2.x default).
- CI: Go toolchain version is now sourced from `go.mod`
  (`go-version-file: go.mod`) instead of the fuzzy `'1.25'` input, so
  the runner always matches the module's stated `go` directive.

### Fixed
- CI: green again after the `actions/setup-go@v6` upgrade flipped
  `GOTOOLCHAIN`'s default from `auto` to `local`, which made the fuzzy
  `'1.25'` input resolve to 1.25.9 on the runner while `go.mod`
  requires `>= 1.25.10`. Sourcing from `go.mod` keeps the two in
  lockstep and removes the manual bump treadmill.

## [0.1.10] - 2026-05-10

### Changed
- Bumped indirect dependencies to their latest patch/minor releases:
  `golang.org/x/net` v0.53.0 → v0.54.0,
  `golang.org/x/sys` v0.43.0 → v0.44.0, and
  `golang.org/x/text` v0.36.0 → v0.37.0. The `go` directive in
  `go.mod` was also bumped from `1.25.9` to `1.25.10` to track the
  latest 1.25.x toolchain. No API changes; routine upstream fixes.
  Validated with `go build`, `go vet`, `gofmt`, `go mod verify`,
  `go test -race`, `govulncheck` (0 vulnerabilities) and
  `golangci-lint` (0 issues) against the project's 10-linter config.

## [0.1.9] - 2026-05-03

### Added
- `overlay manifest` now reuses distfiles already present in the system
  Portage cache instead of re-downloading them. Before each `pkgdev`
  invocation, every `DIST` entry listed in the package's existing
  `Manifest` is looked up in `--distfiles-cache` (default
  `/var/cache/distfiles`) and, when found, symlinked into the working
  distdir. The cache is opened read-only — nothing is ever written
  back. Pass `--distfiles-cache ""` to disable, or point the flag to
  a custom directory. Cache misses fall through to pkgdev's normal
  download path, so the optimization is transparent.
- `LogReporter` now appends `[reused N]` to the per-package OK line
  when at least one distfile was satisfied from the cache, and
  `ManifestUpdate` exposes a new `Reused` field for downstream callers.

## [0.1.8] - 2026-04-29

### Fixed
- CI lint pipeline (`golangci-lint`) is green again. Two `fmt.Fprintln`
  calls in the manifest reporters were swapped for `fmt.Fprintf` with
  an explicit `\n`, since the project's `errcheck` exclusion list
  covers `Fprint`/`Fprintf` but not `Fprintln`. The pkgdev distfiles
  cache directory is now created with mode `0o750` instead of `0o755`
  to satisfy `gosec` G301 (per-user cache; group-only access is
  sufficient). No behaviour change.

## [0.1.7] - 2026-04-29

### Added
- `overlay manifest` now regenerates packages in parallel with a worker
  pool (default 10 simultaneous `pkgdev` invocations, configurable via
  `--jobs`/`-j`). Dramatically faster on overlays with many packages.
  Per-target ordering of the returned results is preserved regardless
  of completion order, and `pkgdev` sub-processes are wired through
  `exec.CommandContext` so SIGINT/SIGTERM cancels an in-flight run
  cleanly.
- Live terminal UI for `overlay manifest`: when stdout is a TTY, a
  fixed block at the bottom shows one slot per active worker plus a
  `[done/total] ████░░░░ NN%` global progress bar; finished packages
  scroll above the block as `✓` / `✗` history lines. Outside a TTY
  (CI logs, pipes), output falls back to plain `START / OK / FAIL`
  log lines — concurrent-safe via an internal mutex. No new
  dependencies; the TUI is built on raw ANSI escapes.

### Changed
- `RegenerateManifests` (internal API) gained `Jobs`, `Reporter` and
  `Ctx` fields on `ManifestOptions`, plus a new `ProgressReporter`
  interface (`Total`/`Start`/`Done`/`Finish`) for lifecycle events.
  `pkgdev` output is now captured per-job into a buffer and surfaced
  to the reporter on failure rather than streamed straight to the
  shared stdout, so parallel runs no longer interleave their logs.

## [0.1.6] - 2026-04-28

### Added
- `overlay manifest` now accepts `--distdir <path>` to choose where
  `pkgdev` writes the distfiles it fetches. When set, the directory is
  expanded (`~` and relative paths), created if missing, and preserved
  between runs as a persistent download cache. Default behaviour is
  unchanged: a temporary directory under `os.TempDir()` is created and
  removed at the end of the run. The pkgdev progress line now logs the
  resolved `distdir` so it is visible at a glance.

## [0.1.5] - 2026-04-28

### Changed
- Bumped indirect dependencies to their latest patch/minor releases:
  `golang.org/x/net` v0.52.0 → v0.53.0,
  `golang.org/x/sys` v0.42.0 → v0.43.0,
  `golang.org/x/text` v0.35.0 → v0.36.0,
  `github.com/mattn/go-isatty` v0.0.20 → v0.0.22, and
  `github.com/golang/groupcache` to the 2024-11-29 snapshot. Pulls in
  routine upstream fixes (no API changes); `govulncheck` reports zero
  known vulnerabilities against the resulting module graph.

## [0.1.4] - 2026-04-28

### Changed
- `overlay commit` no longer renders package-internal support files
  (`Manifest`, `metadata.xml`, `files/*`, patches) in the generated
  commit message. They are implementation details of the surrounding
  ebuild changes and were producing noisy lines such as
  `del({dev-util/claude-code/Manifest, .../metadata.xml, .../files/*})`
  on every commit. Eclasses, profiles, licenses, top-level metadata and
  files at the overlay root continue to be listed. When a commit only
  touches package-internal files, the message falls back to the generic
  `update: package files`.

## [0.1.3] - 2026-04-28

### Added
- `overlay manifest` subcommand: regenerate `Manifest` files for the
  whole overlay, a single category, or a single package
  (`bentoo overlay manifest [<category> | <category>/<package>]`).
  Default behaviour does a clean regeneration — the existing `Manifest`
  is moved aside, `pkgdev manifest` runs against a per-invocation
  `--distdir` under `os.TempDir()`, and the backup is restored on
  failure. Use `--keep` to preserve the existing `Manifest` (soft
  reconcile) or `--dry-run` (`-n`) to list targets without invoking
  pkgdev. Runs unprivileged; no sudo required.

### Changed
- Rename flow (`overlay rename`) now reuses the shared
  `RegenerateManifests` helper in `internal/overlay/manifest.go`
  instead of carrying its own pkgdev wrapper. Behaviour is preserved
  (`Keep: true` mode), eliminating duplicated logic.

## [0.1.2] - 2026-04-24

### Fixed
- `autoupdate` applier now rejects same-version ebuild copies instead of
  silently truncating the source file. `os.Create` truncates before
  `io.Copy` reads, so a self-copy produced a zero-byte ebuild. Adds a
  guard in `copyEbuild` and a degenerate-case skip in the
  `TestEbuildCopyVersioning` property tests that intermittently broke CI.

### Changed
- `.gitignore` now excludes `.tab/` and `.epic/` local plugin state so
  TAB (tech-advisory-board) and Epic plugin data never gets committed.

## [0.1.1] - 2026-04-24

### Fixed
- `overlay commit` now renders non-ebuild files (eclasses, profiles,
  licenses, metadata and arbitrary repo files) in the generated commit
  message instead of falling back to the generic `update: package files`.
  Examples: `add(eclass/rpm.eclass)`, `mod(profiles/package.mask)`,
  `add(app-misc/hello-1.0), add(eclass/rpm.eclass)`.

## [0.1.0] - 2026-04-20

### Added
- Initial release after versioning restructure. Prior history archived;
  project restarts at 0.1.0 following SemVer from this milestone forward.

[Unreleased]: https://github.com/obentoo/bentoolkit/compare/v0.17.1...HEAD
[0.17.1]: https://github.com/obentoo/bentoolkit/compare/v0.17.0...v0.17.1
[0.17.0]: https://github.com/obentoo/bentoolkit/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/obentoo/bentoolkit/compare/v0.15.3...v0.16.0
[0.15.3]: https://github.com/obentoo/bentoolkit/compare/v0.15.2...v0.15.3
[0.15.2]: https://github.com/obentoo/bentoolkit/compare/v0.15.1...v0.15.2
[0.15.1]: https://github.com/obentoo/bentoolkit/compare/v0.15.0...v0.15.1
[0.15.0]: https://github.com/obentoo/bentoolkit/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/obentoo/bentoolkit/compare/v0.13.1...v0.14.0
[0.13.1]: https://github.com/obentoo/bentoolkit/compare/v0.13.0...v0.13.1
[0.13.0]: https://github.com/obentoo/bentoolkit/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/obentoo/bentoolkit/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/obentoo/bentoolkit/compare/v0.10.0...v0.11.0
[0.8.0]: https://github.com/obentoo/bentoolkit/compare/v0.7.1...v0.8.0
[0.7.1]: https://github.com/obentoo/bentoolkit/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/obentoo/bentoolkit/compare/v0.6.0...v0.7.0
[0.4.2]: https://github.com/obentoo/bentoolkit/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/obentoo/bentoolkit/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/obentoo/bentoolkit/compare/v0.3.21...v0.4.0
[0.3.21]: https://github.com/obentoo/bentoolkit/compare/v0.3.20...v0.3.21
[0.3.20]: https://github.com/obentoo/bentoolkit/compare/v0.3.19...v0.3.20
[0.3.19]: https://github.com/obentoo/bentoolkit/compare/v0.3.18...v0.3.19
[0.3.18]: https://github.com/obentoo/bentoolkit/compare/v0.3.17...v0.3.18
[0.3.0]: https://github.com/obentoo/bentoolkit/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/obentoo/bentoolkit/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/obentoo/bentoolkit/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/obentoo/bentoolkit/compare/v0.1.11...v0.2.0
[0.1.11]: https://github.com/obentoo/bentoolkit/compare/v0.1.10...v0.1.11
[0.1.10]: https://github.com/obentoo/bentoolkit/compare/v0.1.9...v0.1.10
[0.1.9]: https://github.com/obentoo/bentoolkit/compare/v0.1.8...v0.1.9
[0.1.8]: https://github.com/obentoo/bentoolkit/compare/v0.1.7...v0.1.8
[0.1.7]: https://github.com/obentoo/bentoolkit/compare/v0.1.6...v0.1.7
[0.1.6]: https://github.com/obentoo/bentoolkit/compare/v0.1.5...v0.1.6
[0.1.5]: https://github.com/obentoo/bentoolkit/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/obentoo/bentoolkit/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/obentoo/bentoolkit/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/obentoo/bentoolkit/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/obentoo/bentoolkit/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/obentoo/bentoolkit/releases/tag/v0.1.0
