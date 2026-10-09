#!/usr/bin/env bats
# Integration tests for scripts/release-deps.sh (story 085).
#
# Every test runs the script against a throwaway git repository holding a tiny
# Go module with one real dependency (golang.org/x/sync, vendored offline from
# the local module cache), a throwaway tag v1.2.3 and throwaway cosign keys.
# Nothing touches the real repository, the real dist/ or the maintainer's key.
#
# Run one sub-task alone:
#   bats --filter '^generate:' tests/release-deps.bats
#   bats --filter '^verify:'   tests/release-deps.bats
#
# RELEASE_DEPS_SCRIPT overrides the script under test (default: this
# checkout's scripts/release-deps.sh).

bats_require_minimum_version 1.5.0

VERSION=1.2.3
NAME=bentoolkit-1.2.3
TARBALL=bentoolkit-1.2.3-vendor.tar.xz
SBOM=bentoolkit-1.2.3.spdx.json
SUMS=bentoolkit-1.2.3-SHA256SUMS
DEP_MODULE=golang.org/x/sync
DEP_VERSION=v0.23.0
# Commit time of the tagged commit; the annotated tag itself is dated a year
# later so a script using the tag object's date (or "now") is caught.
COMMIT_DATE=2020-01-02T03:04:05Z
TAG_DATE=2021-06-07T08:09:10Z
SIGNING_CONFIG='{"mediaType":"application/vnd.dev.sigstore.signingconfig.v0.2+json","caUrls":[],"oidcUrls":[],"rekorTlogUrls":[],"rekorTlogConfig":{"selector":"ANY"},"tsaUrls":[],"tsaConfig":{"selector":"ANY"}}'
PW_SENTINEL=pw-sentinel-085-xyzzy

expected_outputs() {
	printf '%s\n' "$TARBALL" "$TARBALL.sigstore.json" "$SUMS" "$SBOM" "$SBOM.sigstore.json" | LC_ALL=C sort
}

# ---------------------------------------------------------------- fixtures

fixture_git() {
	git -c tag.gpgSign=false -c commit.gpgSign=false "$@"
}

write_module() { # $1 = dir, $2 = Go source for main.go
	printf 'module example.com/fixture\n\ngo 1.27.0\n\ntoolchain go1.27.2\n\nrequire %s %s\n' \
		"$DEP_MODULE" "$DEP_VERSION" >"$1/go.mod"
	printf '%s\n' "$2" >"$1/main.go"
}

MAIN_TAGGED='package main

import "golang.org/x/sync/errgroup"

func main() { var g errgroup.Group; _ = g.Wait() }'

MAIN_AFTER_TAG='package main

import (
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

func main() { var g errgroup.Group; _ = g.Wait(); _ = semaphore.NewWeighted(1) }'

MAIN_UNCOMMITTED='package main

import (
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

func main() { var g errgroup.Group; _ = g.Wait(); var s singleflight.Group; _ = s }'

setup_file() {
	export FX="$BATS_FILE_TMPDIR"
	export RELEASE_DEPS_SCRIPT="${RELEASE_DEPS_SCRIPT:-$(cd "$BATS_TEST_DIRNAME/.." && pwd)/scripts/release-deps.sh}"

	# Pin the Go caches before HOME moves, so the warm module cache and the
	# go1.27.2 toolchain module stay reachable offline.
	GOPATH="$(go env GOPATH)"
	GOMODCACHE="$(go env GOMODCACHE)"
	GOCACHE="$(go env GOCACHE)"
	export GOPATH GOMODCACHE GOCACHE
	export GOPROXY=off GOFLAGS=-mod=mod
	export SYFT_CHECK_FOR_APP_UPDATE=false
	export HOME="$FX/home"
	mkdir -p "$HOME"
	export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
	export GIT_AUTHOR_NAME=fixture GIT_AUTHOR_EMAIL=fixture@example.invalid
	export GIT_COMMITTER_NAME=fixture GIT_COMMITTER_EMAIL=fixture@example.invalid

	# Throwaway keys, all outside the fixture repository.
	mkdir -p "$FX/k" "$FX/k2" "$FX/kpw"
	COSIGN_PASSWORD="" cosign generate-key-pair --output-key-prefix "$FX/k/cosign" >/dev/null 2>&1
	COSIGN_PASSWORD="" cosign generate-key-pair --output-key-prefix "$FX/k2/cosign" >/dev/null 2>&1
	COSIGN_PASSWORD="$PW_SENTINEL" cosign generate-key-pair --output-key-prefix "$FX/kpw/cosign" >/dev/null 2>&1
	printf '%s\n' "$SIGNING_CONFIG" >"$FX/signing-config.json"

	# Fixture repository. Its directory name is NOT bentoolkit, so a prefix
	# derived from the checkout's name is caught.
	local r="$FX/fixture-repo"
	mkdir -p "$r"
	(
		cd "$r" || exit 1
		fixture_git init -q -b main
		write_module "$r" "$MAIN_TAGGED"
		GOTOOLCHAIN=go1.27.2 go mod tidy
		printf 'dist/\n' >.gitignore
		printf 'fixture source file that must never reach the vendor tarball\n' >README.md
		cp "$FX/k/cosign.pub" cosign.pub
		fixture_git add -A
		GIT_AUTHOR_DATE="$COMMIT_DATE" GIT_COMMITTER_DATE="$COMMIT_DATE" fixture_git commit -q -m "fixture"
		GIT_COMMITTER_DATE="$TAG_DATE" fixture_git tag -a -m "v$VERSION" "v$VERSION"
		# A tag that exists but whose version is not X.Y.Z.
		GIT_COMMITTER_DATE="$TAG_DATE" fixture_git tag -a -m "v1.2" "v1.2"
		# A branch, not a tag, named like a release tag.
		fixture_git branch v9.9.9
		# A tag whose go.sum is corrupt: go mod vendor must fail after the
		# preconditions passed.
		sed -i 's/^golang.org\/x\/sync v0.23.0 h1:.*/golang.org\/x\/sync v0.23.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=/' go.sum
		fixture_git commit -q -am "corrupt go.sum"
		fixture_git tag -a -m "v2.0.0" v2.0.0
		fixture_git reset -q --hard "v$VERSION"
	)

	# One golden generation shared by the verify tests (read-only; copied).
	mkdir -p "$FX/golden-repo"
	cp -a "$r/." "$FX/golden-repo/"
	(
		cd "$FX/golden-repo"
		COSIGN_KEY="$FX/k/cosign.key" COSIGN_PASSWORD="" \
			"$RELEASE_DEPS_SCRIPT" generate "$VERSION" dist >"$FX/golden.log" 2>&1
	) || true
}

setup() {
	REPO="$BATS_TEST_TMPDIR/repo"
	mkdir -p "$REPO"
	cp -a "$FX/fixture-repo/." "$REPO/"
	cd "$REPO" || return 1
	export COSIGN_KEY="$FX/k/cosign.key"
	export COSIGN_PASSWORD=""
	unset FORCE
	SCRIPT="$RELEASE_DEPS_SCRIPT"
}

# ---------------------------------------------------------------- helpers

rd() { run --separate-stderr "$SCRIPT" "$@"; }

fail() {
	printf '%s\n' "$*" >&2
	printf -- '--- status: %s\n--- stdout:\n%s\n--- stderr:\n%s\n' "${status:-}" "${output:-}" "${stderr:-}" >&2
	return 1
}

assert_status() {
	[ "$status" -eq "$1" ] || fail "expected exit $1, got $status"
}

# Every non-zero exit prints a `release-deps: <reason>` line on stderr.
assert_reason() { # $1 = ERE the reason line must contain
	grep -qE "^release-deps: .*($1)" <<<"$stderr" ||
		fail "no 'release-deps: ' stderr line matching /$1/"
}

assert_output_has() { # stdout+stderr contain the fixed string
	grep -qF -- "$1" <<<"$output"$'\n'"${stderr:-}" || fail "output does not contain '$1'"
}

# Names (hidden ones included) and content digests of a directory.
snapshot() { # $1 = dir
	if [ ! -e "$1" ]; then
		echo "ABSENT"
		return
	fi
	(cd "$1" && find . -mindepth 1 -printf '%P %y\n' | LC_ALL=C sort &&
		find . -mindepth 1 -type f -exec sha256sum {} + | LC_ALL=C sort -k2)
}

listing() { # $1 = dir
	(cd "$1" && find . -mindepth 1 -printf '%P\n' | LC_ALL=C sort)
}

assert_dist_is_exactly_outputs() { # $1 = dist dir
	[ -d "$1" ] || fail "$1 does not exist"
	local got want
	got="$(listing "$1")"
	want="$(expected_outputs)"
	[ "$got" = "$want" ] || fail "$1 holds:"$'\n'"$got"$'\n'"expected exactly:"$'\n'"$want"
}

# The five release files exist and are non-empty in $1 (other files allowed).
assert_generated() {
	local f
	while read -r f; do
		[ -s "$1/$f" ] || fail "$1/$f was not generated"
	done < <(expected_outputs)
}

sha() { sha256sum <"$1" | cut -d' ' -f1; }

tar_list() { tar -tJf "$1"; }

cosign_verify() { # $1 = pub, $2 = bundle, $3 = file
	cosign verify-blob --key "$1" --insecure-ignore-tlog=true --bundle "$2" "$3" >/dev/null 2>&1
}

cosign_sign() { # $1 = key, $2 = file, $3 = bundle
	cosign sign-blob --yes --key "$1" --signing-config "$FX/signing-config.json" \
		--bundle "$3" "$2" >/dev/null 2>&1
}

# A PATH directory holding every executable of the current PATH except the
# named ones (first PATH entry wins, as with a normal lookup).
path_without() {
	local bin d i
	bin="$BATS_TEST_TMPDIR/bin-without-$(printf "%s-" "$@")"
	mkdir -p "$bin"
	local -a dirs
	IFS=: read -r -a dirs <<<"$PATH"
	for ((i = ${#dirs[@]} - 1; i >= 0; i--)); do
		d="${dirs[i]}"
		[ -d "$d" ] || continue
		find "$d" -maxdepth 1 \( -type f -o -type l \) -perm -u+x -print0 |
			xargs -0 -r ln -sfn -t "$bin"
	done
	for i in "$@"; do rm -f "$bin/$i"; done
	printf '%s\n' "$bin"
}

# The golden set from setup_file, copied into this test's dist/.
use_golden() {
	local g="$FX/golden-repo/dist"
	[ -d "$g" ] && [ "$(listing "$g")" = "$(expected_outputs)" ] ||
		fail "golden generation did not produce the five release files; its log:"$'\n'"$(cat "$FX/golden.log" 2>/dev/null)"
	mkdir -p dist
	cp -a "$g/." dist/
}

# ================================================================ generate (1.1)

@test "generate: writes exactly the five release files, nothing else, and leaves the repository untouched" {
	mkdir -p dist
	local before
	before="$(git status --porcelain --ignored=no)"
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	assert_dist_is_exactly_outputs dist
	[ "$(git status --porcelain --ignored=no)" = "$before" ] || fail "the working tree changed"
	[ ! -e vendor ] || fail "a vendor/ directory was created in the repository"
}

@test "generate: creates the dist directory when it does not exist yet" {
	[ ! -e dist ]
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	assert_dist_is_exactly_outputs dist
}

@test "generate: tarball entries are only bentoolkit-<v>/vendor/ (no source file, no .git, no checkout-name prefix)" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	local entries bad
	entries="$(tar_list "dist/$TARBALL")"
	bad="$(grep -vE "^$NAME/vendor(/|\$)" <<<"$entries" || true)"
	[ -z "$bad" ] || fail "entries outside $NAME/vendor/:"$'\n'"$bad"
	! grep -qE '(^|/)\.git(/|$)|README\.md$|main\.go$|cosign\.pub$|(^|/)go\.(mod|sum)$' <<<"$entries" ||
		fail "source or repository files entered the tarball"
	grep -qx "$NAME/vendor/modules.txt" <<<"$entries" || fail "vendor/modules.txt missing"
	grep -qx "$NAME/vendor/golang.org/x/sync/errgroup/errgroup.go" <<<"$entries" || fail "vendored dependency missing"
}

@test "generate: a commit after the tag, an uncommitted edit and untracked files (even an untracked vendor/) stay out" {
	write_module "$REPO" "$MAIN_AFTER_TAG"
	fixture_git commit -q -am "after the tag"
	write_module "$REPO" "$MAIN_UNCOMMITTED"
	printf 'untracked\n' >untracked.txt
	mkdir -p vendor/golang.org/x/evil
	printf 'package evil\n' >vendor/golang.org/x/evil/evil.go
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	local entries
	entries="$(tar_list "dist/$TARBALL")"
	! grep -qE 'semaphore|singleflight|evil|untracked' <<<"$entries" ||
		fail "working-tree or post-tag content entered the tarball:"$'\n'"$entries"
	[ -f vendor/golang.org/x/evil/evil.go ] && [ -f untracked.txt ] ||
		fail "the script modified the working tree"
	grep -q singleflight main.go || fail "the script reverted an uncommitted edit"
}

@test "generate: vendor content is byte-for-byte go mod vendor of the tag's tree" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	local ref="$BATS_TEST_TMPDIR/ref" got="$BATS_TEST_TMPDIR/got"
	mkdir -p "$ref" "$got"
	fixture_git archive "v$VERSION" | tar -x -C "$ref"
	(cd "$ref" && GOTOOLCHAIN=go1.27.2 go mod vendor)
	tar -xJf "dist/$TARBALL" -C "$got"
	diff -r "$ref/vendor" "$got/$NAME/vendor" || fail "vendor tree differs from go mod vendor of the tag"
}

@test "generate: a lightweight tag works and the prefix follows the requested version" {
	fixture_git tag v1.2.4 "v$VERSION^{commit}"
	rd generate 1.2.4 dist
	assert_status 0
	[ -f dist/bentoolkit-1.2.4-vendor.tar.xz ] || fail "tarball for 1.2.4 missing"
	local bad
	bad="$(tar_list dist/bentoolkit-1.2.4-vendor.tar.xz | grep -vE '^bentoolkit-1\.2\.4/vendor(/|$)' || true)"
	[ -z "$bad" ] || fail "wrong prefix:"$'\n'"$bad"
}

@test "generate: two generations are byte-identical even from another path, umask and timezone" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	local other="$BATS_TEST_TMPDIR/elsewhere/another-clone"
	mkdir -p "$other"
	cp -a "$FX/fixture-repo/." "$other/"
	run --separate-stderr bash -c 'cd "$1" && umask 077 && TZ=Asia/Tokyo LC_ALL=C "$2" generate "$3" "$4"' \
		_ "$other" "$SCRIPT" "$VERSION" "$BATS_TEST_TMPDIR/dist2"
	assert_status 0
	assert_generated "$BATS_TEST_TMPDIR/dist2"
	[ "$(sha "dist/$TARBALL")" = "$(sha "$BATS_TEST_TMPDIR/dist2/$TARBALL")" ] ||
		fail "tarballs differ: $(sha "dist/$TARBALL") vs $(sha "$BATS_TEST_TMPDIR/dist2/$TARBALL")"
}

@test "generate: every tar entry is owner/group 0 stored numerically, with no user or group name" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	local v bad
	v="$(tar -tvJf "dist/$TARBALL")"
	[ -n "$v" ] || fail "empty tarball"
	bad="$(awk '$2 != "0/0"' <<<"$v")"
	[ -z "$bad" ] || fail "entries not stored as 0/0 without names:"$'\n'"$bad"
}

@test "generate: tar entries are in sorted name order" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	local entries
	entries="$(tar_list "dist/$TARBALL" | sed 's:/$::')"
	[ "$entries" = "$(LC_ALL=C sort <<<"$entries")" ] || fail "entries are not sorted:"$'\n'"$entries"
}

@test "generate: every tar entry carries the tag commit's time, not the tag object's date nor now" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	local want bad
	want="$(TZ=UTC date -d "$COMMIT_DATE" '+%Y-%m-%d %H:%M:%S')"
	bad="$(TZ=UTC tar --full-time -tvJf "dist/$TARBALL" | awk -v w="$want" '($4 " " $5) != w')"
	[ -z "$bad" ] || fail "entries without mtime $want UTC:"$'\n'"$bad"
}

@test "generate: the SBOM is SPDX JSON listing every module that vendor/modules.txt records" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	jq -e '.spdxVersion | startswith("SPDX-")' "dist/$SBOM" >/dev/null || fail "not an SPDX JSON document"
	local mods m v
	mods="$(tar -xJOf "dist/$TARBALL" "$NAME/vendor/modules.txt" | awk '$1 == "#" {print $2, $3}')"
	[ -n "$mods" ] || fail "modules.txt lists no module"
	while read -r m v; do
		jq -e --arg m "$m" --arg v "$v" \
			'[.packages[] | select(.name == $m and .versionInfo == $v)] | length > 0' "dist/$SBOM" >/dev/null ||
			fail "SBOM lacks module $m $v"
	done <<<"$mods"
}

@test "generate: SHA256SUMS lists exactly the tarball and the SBOM by bare name, and checks" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	local names
	names="$(awk '{print $2}' "dist/$SUMS" | sed 's/^\*//' | LC_ALL=C sort)"
	[ "$names" = "$(printf '%s\n' "$TARBALL" "$SBOM" | LC_ALL=C sort)" ] ||
		fail "SHA256SUMS names:"$'\n'"$names"
	(cd dist && sha256sum --quiet -c "$SUMS") || fail "SHA256SUMS does not check"
}

@test "generate: each bundle verifies its own file with the public key and not the other file" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	! cosign_verify "$FX/k/cosign.pub" "dist/$TARBALL.sigstore.json" "dist/$SBOM" ||
		fail "the tarball's bundle verifies the SBOM"
	! cosign_verify "$FX/k/cosign.pub" "dist/$SBOM.sigstore.json" "dist/$TARBALL" ||
		fail "the SBOM's bundle verifies the tarball"
	! cosign_verify "$FX/k2/cosign.pub" "dist/$TARBALL.sigstore.json" "dist/$TARBALL" ||
		fail "the tarball's bundle verifies with an unrelated key"
	cosign_verify "$FX/k/cosign.pub" "dist/$TARBALL.sigstore.json" "dist/$TARBALL" || fail "tarball bundle does not verify"
	cosign_verify "$FX/k/cosign.pub" "dist/$SBOM.sigstore.json" "dist/$SBOM" || fail "SBOM bundle does not verify"
}

@test "generate: signs with no network at all and the bundles carry no transparency-log entry" {
	run --separate-stderr unshare -rn "$SCRIPT" generate "$VERSION" dist
	assert_status 0
	assert_dist_is_exactly_outputs dist
	local b
	for b in "dist/$TARBALL.sigstore.json" "dist/$SBOM.sigstore.json"; do
		jq -e '(.verificationMaterial.tlogEntries // []) | length == 0' "$b" >/dev/null ||
			fail "$b carries a transparency-log entry"
	done
}

@test "generate: the key password never appears in the output" {
	export COSIGN_KEY="$FX/kpw/cosign.key" COSIGN_PASSWORD="$PW_SENTINEL"
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	! grep -qF "$PW_SENTINEL" <<<"$output"$'\n'"$stderr" || fail "the password was printed"
}

@test "generate: logs one structured release-deps step line per step; the tar step names the tarball digest" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	grep -qE '^release-deps: .*step=vendor' <<<"$stderr" || fail "no step=vendor line"
	grep -qE "^release-deps: .*step=tar .*sha256=$(sha "dist/$TARBALL")" <<<"$stderr" || fail "no step=tar line with the tarball digest"
	grep -qE '^release-deps: .*step=sign' <<<"$stderr" || fail "no step=sign line"
}

@test "generate: a version not of the form X.Y.Z is refused and nothing is written (tag v1.2 exists)" {
	local v
	# The reason must stay on ONE release-deps: line even for a value holding a newline.
	for v in "" "1.2" "v1.2.3" "1.2.3.4" "1.2.3-rc1" " 1.2.3" $'1.2.3\nx' "1.2.3 "; do
		rd generate "$v" dist
		assert_status 2 || fail "version '$v' was not refused with exit 2"
		assert_reason 'X\.Y\.Z' || fail "version '$v': reason does not name X.Y.Z"
		[ "$(snapshot dist)" = "ABSENT" ] || fail "version '$v': dist was written"
	done
}

@test "generate: a branch named like the tag is not a tag: refused naming v9.9.9, nothing written" {
	fixture_git rev-parse -q --verify refs/heads/v9.9.9 >/dev/null
	rd generate 9.9.9 dist
	assert_status 2
	assert_reason 'v9\.9\.9'
	[ "$(snapshot dist)" = "ABSENT" ] || fail "dist was written"
}

@test "generate: a missing tag is refused naming it, and nothing is written" {
	rd generate 3.4.5 dist
	assert_status 2
	assert_reason 'v3\.4\.5'
	[ "$(snapshot dist)" = "ABSENT" ] || fail "dist was written"
}

@test "generate: a COSIGN_KEY inside the repository is refused under every spelling (direct, .., symlink, relative)" {
	mkdir -p keys
	cp "$FX/k/cosign.key" keys/cosign.key
	ln -s "$REPO" "$BATS_TEST_TMPDIR/repo-link"
	local k
	for k in "$REPO/keys/cosign.key" "$REPO/keys/../keys/cosign.key" \
		"$BATS_TEST_TMPDIR/repo-link/keys/cosign.key" "keys/cosign.key" "./keys/cosign.key"; do
		COSIGN_KEY="$k" rd generate "$VERSION" dist
		assert_status 2 || fail "key '$k' inside the repository was not refused"
		assert_reason 'key|repositor' || fail "key '$k': no reason line"
		[ "$(snapshot dist)" = "ABSENT" ] || fail "key '$k': dist was written"
	done
}

@test "generate: a key outside the repository whose path merely starts with the repository's path is accepted" {
	mkdir -p "$REPO-keys"
	cp "$FX/k/cosign.key" "$REPO-keys/cosign.key"
	COSIGN_KEY="$REPO-keys/cosign.key" rd generate "$VERSION" dist
	assert_status 0
	assert_dist_is_exactly_outputs dist
}

@test "generate: a missing key file is refused naming the path and cosign generate-key-pair, nothing written" {
	COSIGN_KEY="$BATS_TEST_TMPDIR/nowhere/cosign.key" rd generate "$VERSION" dist
	assert_status 2
	assert_reason "$BATS_TEST_TMPDIR/nowhere/cosign.key"
	assert_reason 'cosign generate-key-pair'
	[ "$(snapshot dist)" = "ABSENT" ] || fail "dist was written"
}

@test "generate: without COSIGN_KEY the key is \$HOME/.config/bentoolkit-release/cosign.key" {
	local home="$BATS_TEST_TMPDIR/home"
	mkdir -p "$home"
	unset COSIGN_KEY
	HOME="$home" rd generate "$VERSION" dist
	assert_status 2
	assert_reason "$home/.config/bentoolkit-release/cosign.key"
	[ "$(snapshot dist)" = "ABSENT" ] || fail "dist was written"

	mkdir -p "$home/.config/bentoolkit-release"
	cp "$FX/k2/cosign.key" "$home/.config/bentoolkit-release/cosign.key"
	HOME="$home" rd generate "$VERSION" dist
	assert_status 0
	cosign_verify "$FX/k2/cosign.pub" "dist/$TARBALL.sigstore.json" "dist/$TARBALL" ||
		fail "the tarball was not signed with the default key"
}

@test "generate: a missing syft, cosign or xz is refused naming the tool, and nothing is written" {
	local t bin
	for t in syft cosign xz; do
		bin="$(path_without "$t")"
		PATH="$bin" rd generate "$VERSION" dist
		assert_status 2 || fail "missing $t was not refused with exit 2"
		assert_reason "$t" || fail "missing $t: reason does not name it"
		[ "$(snapshot dist)" = "ABSENT" ] || fail "missing $t: dist was written"
	done
}

@test "generate: outputs of a version whose name extends this one (1.2.30) do not block, and survive untouched" {
	mkdir -p dist
	local f
	for f in bentoolkit-1.2.30-vendor.tar.xz bentoolkit-1.2.30.spdx.json bentoolkit-1.2.30-SHA256SUMS \
		bentoolkit-1.2.30-vendor.tar.xz.sigstore.json bentoolkit-1.2.3x.spdx.json; do
		printf 'other version %s\n' "$f" >"dist/$f"
	done
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	for f in bentoolkit-1.2.30-vendor.tar.xz bentoolkit-1.2.30.spdx.json bentoolkit-1.2.30-SHA256SUMS \
		bentoolkit-1.2.30-vendor.tar.xz.sigstore.json bentoolkit-1.2.3x.spdx.json; do
		grep -qx "other version $f" "dist/$f" || fail "dist/$f was modified"
	done
	[ "$(listing dist | grep -vE '1\.2\.30|1\.2\.3x')" = "$(expected_outputs)" ] || fail "unexpected dist content:"$'\n'"$(listing dist)"
}

@test "generate: any single existing output for the version (only the SBOM bundle) blocks, nothing else written" {
	mkdir -p dist
	printf 'previous\n' >"dist/$SBOM.sigstore.json"
	local before
	before="$(snapshot dist)"
	rd generate "$VERSION" dist
	assert_status 2
	assert_reason 'FORCE|exist'
	[ "$(snapshot dist)" = "$before" ] || fail "dist changed"
}

@test "generate: a second generation without FORCE (or FORCE=0/empty) fails and the first set stays byte-identical" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	local before f
	before="$(snapshot dist)"
	for f in unset 0 ""; do
		if [ "$f" = unset ]; then
			rd generate "$VERSION" dist
		else
			FORCE="$f" rd generate "$VERSION" dist
		fi
		assert_status 2 || fail "FORCE=$f: second generation was not refused"
		assert_reason 'FORCE|exist' || fail "FORCE=$f: no reason line"
		[ "$(snapshot dist)" = "$before" ] || fail "FORCE=$f: the first set changed"
	done
}

@test "generate: FORCE=1 regenerates over an existing set: same tarball bytes, exactly five files, bundles verify" {
	rd generate "$VERSION" dist
	assert_status 0
	assert_generated dist
	local first
	first="$(sha "dist/$TARBALL")"
	FORCE=1 rd generate "$VERSION" dist
	assert_status 0
	assert_dist_is_exactly_outputs dist
	[ "$(sha "dist/$TARBALL")" = "$first" ] || fail "FORCE=1 tarball differs from the first"
	cosign_verify "$FX/k/cosign.pub" "dist/$TARBALL.sigstore.json" "dist/$TARBALL" || fail "tarball bundle does not verify"
	cosign_verify "$FX/k/cosign.pub" "dist/$SBOM.sigstore.json" "dist/$SBOM" || fail "SBOM bundle does not verify"
	(cd dist && sha256sum --quiet -c "$SUMS") || fail "SHA256SUMS does not check"
}

@test "generate: a go mod vendor failure (corrupt go.sum on tag v2.0.0) leaves no output and no temp file" {
	mkdir -p dist
	printf 'keep\n' >dist/unrelated.txt
	local before
	before="$(snapshot dist)"
	rd generate 2.0.0 dist
	[ "$status" -ne 0 ] || fail "a corrupt go.sum was accepted"
	assert_reason '.'
	[ "$(snapshot dist)" = "$before" ] || fail "dist changed:"$'\n'"$(listing dist)"
}

@test "generate: a signing failure (wrong password) leaves no tarball, SBOM, sums or temp file" {
	mkdir -p dist
	local before
	before="$(snapshot dist)"
	COSIGN_KEY="$FX/kpw/cosign.key" COSIGN_PASSWORD="wrong-password" rd generate "$VERSION" dist
	[ "$status" -ne 0 ] || fail "signing with a wrong password succeeded"
	assert_reason '.'
	[ "$(snapshot dist)" = "$before" ] || fail "partial output left:"$'\n'"$(listing dist)"
}

# ================================================================ verify (1.2)

@test "verify: a validly signed and checksummed but different tarball still fails, printing both digests" {
	use_golden
	local good evil="$BATS_TEST_TMPDIR/evil" x="$BATS_TEST_TMPDIR/x"
	good="$(sha "dist/$TARBALL")"
	mkdir -p "$x"
	tar -xJf "dist/$TARBALL" -C "$x"
	printf '// injected\n' >>"$x/$NAME/vendor/golang.org/x/sync/errgroup/errgroup.go"
	tar -C "$x" --sort=name --mtime="@0" --owner=0 --group=0 --numeric-owner -cf - "$NAME/vendor" | xz -9 -T1 >"$evil"
	cp "$evil" "dist/$TARBALL"
	cosign_sign "$FX/k/cosign.key" "dist/$TARBALL" "dist/$TARBALL.sigstore.json"
	(cd dist && sha256sum "$TARBALL" "$SBOM" >"$SUMS")
	local before
	before="$(snapshot dist)"
	rd verify "$VERSION" dist
	assert_status 1
	assert_output_has "$good"
	assert_output_has "$(sha "$evil")"
	[ "$(snapshot dist)" = "$before" ] || fail "verify wrote in dist"
}

@test "verify: one byte appended to the tarball fails with exit 1 and prints both digests" {
	use_golden
	local good
	good="$(sha "dist/$TARBALL")"
	printf 'x' >>"dist/$TARBALL"
	rd verify "$VERSION" dist
	assert_status 1
	assert_output_has "$good"
	assert_output_has "$(sha "dist/$TARBALL")"
}

@test "verify: one byte flipped inside the tarball (same size) fails with exit 1 and prints both digests" {
	use_golden
	local good size
	good="$(sha "dist/$TARBALL")"
	size="$(stat -c %s "dist/$TARBALL")"
	printf '\x5a' | dd of="dist/$TARBALL" bs=1 seek=$((size / 2)) conv=notrunc status=none
	[ "$(sha "dist/$TARBALL")" != "$good" ] || printf '\xa5' | dd of="dist/$TARBALL" bs=1 seek=$((size / 2)) conv=notrunc status=none
	[ "$(stat -c %s "dist/$TARBALL")" -eq "$size" ]
	rd verify "$VERSION" dist
	assert_status 1
	assert_output_has "$good"
	assert_output_has "$(sha "dist/$TARBALL")"
}

@test "verify: a modified SBOM whose SHA256SUMS line was updated to match still fails on the signature" {
	use_golden
	printf '\n' >>"dist/$SBOM"
	(cd dist && sha256sum "$TARBALL" "$SBOM" >"$SUMS")
	rd verify "$VERSION" dist
	assert_status 1
	assert_output_has "$SBOM"
}

@test "verify: a modified SBOM fails with exit 1" {
	use_golden
	sed -i '0,/"spdxVersion"/s/"spdxVersion"/"spdxVersion" /' "dist/$SBOM"
	rd verify "$VERSION" dist
	assert_status 1
}

@test "verify: the tarball and SBOM bundles swapped fail with exit 1" {
	use_golden
	mv "dist/$TARBALL.sigstore.json" "$BATS_TEST_TMPDIR/t.json"
	mv "dist/$SBOM.sigstore.json" "dist/$TARBALL.sigstore.json"
	mv "$BATS_TEST_TMPDIR/t.json" "dist/$SBOM.sigstore.json"
	rd verify "$VERSION" dist
	assert_status 1
}

@test "verify: both files re-signed with another key fail against the repository's cosign.pub" {
	use_golden
	cosign_sign "$FX/k2/cosign.key" "dist/$TARBALL" "dist/$TARBALL.sigstore.json"
	cosign_sign "$FX/k2/cosign.key" "dist/$SBOM" "dist/$SBOM.sigstore.json"
	rd verify "$VERSION" dist
	assert_status 1
}

@test "verify: a different cosign.pub at the repository root fails with exit 1" {
	use_golden
	cp "$FX/k2/cosign.pub" cosign.pub
	rd verify "$VERSION" dist
	assert_status 1
}

@test "verify: a SHA256SUMS that drops the SBOM line fails, though every remaining line checks" {
	use_golden
	grep -vF "$SBOM" "dist/$SUMS" >"$BATS_TEST_TMPDIR/sums"
	cp "$BATS_TEST_TMPDIR/sums" "dist/$SUMS"
	(cd dist && sha256sum --quiet -c "$SUMS")
	rd verify "$VERSION" dist
	assert_status 1
}

@test "verify: a SHA256SUMS with one digest edited fails with exit 1" {
	use_golden
	awk -v s="$SBOM" '$2 == s { print "0000000000000000000000000000000000000000000000000000000000000000  " $2; next } { print }' \
		"dist/$SUMS" >"$BATS_TEST_TMPDIR/sums"
	cp "$BATS_TEST_TMPDIR/sums" "dist/$SUMS"
	grep -q '^0\{64\}  ' "dist/$SUMS"
	rd verify "$VERSION" dist
	assert_status 1
}

@test "verify: a missing cosign.pub is a precondition failure (exit 2) and nothing is written" {
	use_golden
	rm cosign.pub
	local before
	before="$(snapshot dist)"
	rd verify "$VERSION" dist
	assert_status 2
	assert_reason 'cosign\.pub'
	[ "$(snapshot dist)" = "$before" ] || fail "verify wrote in dist"
}

@test "verify: a bad version or a missing tag is refused with exit 2" {
	use_golden
	rd verify 1.2 dist
	assert_status 2
	assert_reason 'X\.Y\.Z'
	rd verify 9.9.9 dist
	assert_status 2
	assert_reason 'v9\.9\.9'
}

@test "verify: an empty dist (nothing generated) fails and writes nothing" {
	mkdir -p dist
	rd verify "$VERSION" dist
	[ "$status" -ne 0 ] || fail "verify passed with nothing to verify"
	assert_reason '.'
	[ -z "$(listing dist)" ] || fail "verify wrote in dist"
}

@test "verify: a post-tag commit, uncommitted edit and untracked vendor/ in the working tree do not break verification" {
	use_golden
	write_module "$REPO" "$MAIN_AFTER_TAG"
	fixture_git commit -q -am "after the tag"
	write_module "$REPO" "$MAIN_UNCOMMITTED"
	mkdir -p vendor/golang.org/x/evil
	printf 'package evil\n' >vendor/golang.org/x/evil/evil.go
	rd verify "$VERSION" dist
	assert_status 0
}

@test "verify: does not need the private key (COSIGN_KEY points nowhere, empty HOME)" {
	use_golden
	mkdir -p "$BATS_TEST_TMPDIR/home"
	COSIGN_KEY="$BATS_TEST_TMPDIR/nowhere/cosign.key" HOME="$BATS_TEST_TMPDIR/home" rd verify "$VERSION" dist
	assert_status 0
}

@test "verify: passes with no network at all" {
	use_golden
	run --separate-stderr unshare -rn "$SCRIPT" verify "$VERSION" dist
	assert_status 0
}

@test "verify: passes on a freshly generated set and writes nothing in dist" {
	use_golden
	local before
	before="$(snapshot dist)"
	rd verify "$VERSION" dist
	assert_status 0
	[ "$(snapshot dist)" = "$before" ] || fail "verify wrote in dist"
}
