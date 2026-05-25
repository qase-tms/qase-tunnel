#!/usr/bin/env bash
# Downloads `frpc` for every (os, arch) qase-tunnel ships, verifies SHA256
# against frp's official checksums file, and places each binary in
# `dist/frpc/<os>_<arch>/` so GoReleaser's `archives.files` can embed it.
#
# Run from the repo root before `goreleaser release` (wired in via the
# `before.hooks` block in .goreleaser.yaml).
#
# Override the pinned upstream version with `FRP_VERSION=0.x.y` if you need
# to bump.

set -euo pipefail

FRP_VERSION="${FRP_VERSION:-0.69.0}"
BASE="https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}"
DEST="dist/frpc"

CHECKSUMS="$(mktemp)"
trap 'rm -f "$CHECKSUMS"' EXIT

echo "==> frp v${FRP_VERSION} (fetching checksum manifest)"
curl -fsSL --proto '=https' --tlsv1.2 -o "$CHECKSUMS" "${BASE}/frp_sha256_checksums.txt"

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

fetch_one() {
    local os="$1" arch="$2" ext="$3"
    local stem="frp_${FRP_VERSION}_${os}_${arch}"
    local archive="${stem}.${ext}"
    local outdir="${DEST}/${os}_${arch}"
    local frpc_name="frpc"
    [ "$os" = "windows" ] && frpc_name="frpc.exe"

    mkdir -p "$outdir"
    echo "==> ${archive} -> ${outdir}/${frpc_name}"

    local tmp
    tmp="$(mktemp -d)"

    curl -fsSL --proto '=https' --tlsv1.2 -o "${tmp}/${archive}" "${BASE}/${archive}"

    local expected
    expected="$(awk -v f="${archive}" '$2 == f { print $1; exit }' "$CHECKSUMS")"
    if [ -z "$expected" ]; then
        echo "fetch-frpc: checksum entry missing for ${archive}" >&2
        rm -rf "$tmp"; return 1
    fi
    local actual
    actual="$(sha256_of "${tmp}/${archive}")"
    if [ "$expected" != "$actual" ]; then
        echo "fetch-frpc: SHA256 mismatch for ${archive}" >&2
        echo "  expected: $expected" >&2
        echo "  actual:   $actual" >&2
        rm -rf "$tmp"; return 1
    fi

    if [ "$ext" = "tar.gz" ]; then
        tar -xzf "${tmp}/${archive}" -C "$tmp" "${stem}/${frpc_name}" "${stem}/LICENSE"
    else
        (cd "$tmp" && unzip -q "${archive}" "${stem}/${frpc_name}" "${stem}/LICENSE")
    fi

    mv "${tmp}/${stem}/${frpc_name}" "${outdir}/${frpc_name}"
    mv "${tmp}/${stem}/LICENSE"     "${outdir}/LICENSE-frp"
    chmod +x "${outdir}/${frpc_name}" || true
    rm -rf "$tmp"
}

fetch_one linux   amd64 tar.gz
fetch_one linux   arm64 tar.gz
fetch_one darwin  amd64 tar.gz
fetch_one darwin  arm64 tar.gz
fetch_one windows amd64 zip
fetch_one windows arm64 zip

echo "✓ frpc binaries ready in ${DEST}/"
