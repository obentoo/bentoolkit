#!/usr/bin/env bash
# Release assets for a tag: the vendor tarball the overlay ebuilds fetch so
# they build without network access, an SPDX SBOM, SHA256SUMS, and a cosign
# bundle for the tarball and the SBOM. See "Release assets" in
# docs/development.md.
#
#   release-deps.sh generate <version> <dist-dir>   write the five files
#   release-deps.sh verify   <version> <dist-dir>   regenerate and check them
#
# The tarball is built from `git archive v<version>`, never from the working
# tree, and is byte-reproducible. Signing uses a key outside the repository
# (COSIGN_KEY) and a signing config with no Rekor log, so nothing is uploaded.
# Exit codes: 0 ok, 1 a check or a step failed, 2 a precondition failed.
set -euo pipefail

die() { printf 'release-deps: %s\n' "$2" >&2; exit "$1"; }
log() { printf 'release-deps: %s\n' "$*" >&2; }

mode=${1:-}
version=${2:-}
dist=${3:-}

[[ $mode == generate || $mode == verify ]] || die 2 "usage: release-deps.sh generate|verify <version> <dist-dir>"
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die 2 "version $(printf '%q' "$version") is not of the form X.Y.Z"
[[ -n $dist ]] || die 2 "dist-dir is empty"
tag="v$version"
git rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null || die 2 "tag $tag does not exist"
for t in git go tar xz syft cosign sha256sum; do
	command -v "$t" >/dev/null 2>&1 || die 2 "required tool $t is not on PATH"
done
tar --sort=name -cf /dev/null --files-from /dev/null 2>/dev/null || die 2 "tar does not support --sort=name (GNU tar >= 1.28 needed)"

top=$(git rev-parse --show-toplevel)
name="bentoolkit-$version"
tarball="$name-vendor.tar.xz"
sbom="$name.spdx.json"
sums="$name-SHA256SUMS"
outputs=("$tarball" "$sbom" "$sums" "$tarball.sigstore.json" "$sbom.sigstore.json")

if [[ $mode == generate ]]; then
	key=${COSIGN_KEY:-$HOME/.config/bentoolkit-release/cosign.key}
	[[ -f $key ]] || die 2 "key $key does not exist; create it with: cosign generate-key-pair --output-key-prefix ${key%.key}"
	rkey=$(realpath -e -- "$key")
	rtop=$(realpath -e -- "$top")
	[[ $rkey != "$rtop"/* ]] || die 2 "key $key is inside the repository; keep it outside"
	if [[ ${FORCE:-} != 1 ]]; then
		for o in "${outputs[@]}"; do
			[[ ! -e $dist/$o ]] || die 2 "$dist/$o already exists; set FORCE=1 to overwrite"
		done
	fi
else
	[[ -f $top/cosign.pub ]] || die 2 "public key $top/cosign.pub does not exist"
fi

tmp=$(mktemp -d)
cleanup() {
	rm -rf -- "$tmp"
	if [[ -d $dist ]]; then rm -f -- "$dist"/.release-deps.* ; fi
}
trap cleanup EXIT

build_tarball() { # $1 = output path
	mkdir -p "$tmp/src"
	git archive --prefix="$name/" "$tag" | tar -x -C "$tmp/src"
	local gomod="$tmp/src/$name/go.mod" tc
	tc=$(sed -n 's/^toolchain[[:space:]]\{1,\}\(go[^[:space:]]*\).*/\1/p' "$gomod" | head -n1)
	if [[ -z $tc ]]; then
		tc=go$(sed -n 's/^go[[:space:]]\{1,\}\([^[:space:]]*\).*/\1/p' "$gomod" | head -n1)
	fi
	# The toolchain the tag's go.mod names, so the vendor tree matches a build
	# of that tag rather than whatever Go is on PATH.
	(cd "$tmp/src/$name" && GOTOOLCHAIN=$tc GOFLAGS=-mod=mod go mod vendor) || die 1 "go mod vendor failed for $tag"
	log "step=vendor version=$version modules=$(grep -c '^# ' "$tmp/src/$name/vendor/modules.txt" || true)"
	local epoch
	epoch=$(git log -1 --format=%ct "$tag^{commit}")
	LC_ALL=C tar -C "$tmp/src" --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner \
		--mode=u=rwX,go=rX --format=gnu -cf - "$name/vendor" | xz -9 -T1 >"$1"
	log "step=tar sha256=$(sha256sum <"$1" | cut -d' ' -f1) bytes=$(stat -c %s "$1")"
}

if [[ $mode == generate ]]; then
	mkdir -p -- "$dist"
	build_tarball "$dist/.release-deps.tar"
	SYFT_CHECK_FOR_APP_UPDATE=false syft scan "dir:$tmp/src/$name" --source-name bentoolkit \
		--source-version "$version" -q -o "spdx-json=$dist/.release-deps.spdx"
	log "step=sbom file=$sbom"
	# cosign 3 uploads to the public Rekor log by default, even with --key; a
	# signing config that lists no log is the supported way to keep it local.
	printf '%s\n' '{"mediaType":"application/vnd.dev.sigstore.signingconfig.v0.2+json","caUrls":[],"oidcUrls":[],"rekorTlogUrls":[],"rekorTlogConfig":{"selector":"ANY"},"tsaUrls":[],"tsaConfig":{"selector":"ANY"}}' >"$tmp/signing-config.json"
	t_hash=$(sha256sum <"$dist/.release-deps.tar" | cut -d' ' -f1)
	s_hash=$(sha256sum <"$dist/.release-deps.spdx" | cut -d' ' -f1)
	printf '%s  %s\n%s  %s\n' "$t_hash" "$tarball" "$s_hash" "$sbom" >"$dist/.release-deps.sums"
	for pair in "tar:$tarball" "spdx:$sbom"; do
		cosign sign-blob --yes --key "$rkey" --signing-config "$tmp/signing-config.json" \
			--bundle "$dist/.release-deps.${pair%%:*}.bundle" "$dist/.release-deps.${pair%%:*}" >/dev/null 2>"$tmp/cosign.err" \
			|| { cat "$tmp/cosign.err" >&2; die 1 "cosign sign-blob failed for ${pair#*:}"; }
		log "step=sign file=${pair#*:}"
	done
	mv -f -- "$dist/.release-deps.tar" "$dist/$tarball"
	mv -f -- "$dist/.release-deps.spdx" "$dist/$sbom"
	mv -f -- "$dist/.release-deps.tar.bundle" "$dist/$tarball.sigstore.json"
	mv -f -- "$dist/.release-deps.spdx.bundle" "$dist/$sbom.sigstore.json"
	mv -f -- "$dist/.release-deps.sums" "$dist/$sums"
	exit 0
fi

# verify
for o in "${outputs[@]}"; do
	[[ -f $dist/$o ]] || die 1 "$dist/$o is missing"
done
build_tarball "$tmp/regen.tar.xz"
if ! cmp -s "$tmp/regen.tar.xz" "$dist/$tarball"; then
	die 1 "tarball mismatch: dist sha256=$(sha256sum <"$dist/$tarball" | cut -d' ' -f1) regenerated sha256=$(sha256sum <"$tmp/regen.tar.xz" | cut -d' ' -f1)"
fi
for f in "$tarball" "$sbom"; do
	cosign verify-blob --key "$top/cosign.pub" --insecure-ignore-tlog=true \
		--bundle "$dist/$f.sigstore.json" "$dist/$f" >/dev/null 2>&1 || die 1 "signature of $f does not verify"
done
listed=$(awk '{print $2}' "$dist/$sums" | LC_ALL=C sort | tr '\n' ' ')
want=$(printf '%s\n' "$tarball" "$sbom" | LC_ALL=C sort | tr '\n' ' ')
[[ $listed == "$want" ]] || die 1 "$sums does not list exactly $tarball and $sbom"
(cd "$dist" && sha256sum --quiet -c "$sums") >/dev/null 2>&1 || die 1 "$sums does not match"
log "step=verify version=$version result=ok"
