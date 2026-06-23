#!/usr/bin/env bash
#
# qase-tunnel installer for macOS / Linux.
#
# Detects the host OS + architecture, downloads the matching binary from the
# latest qase-tunnel GitHub release, verifies the SHA256, installs it to
# /usr/local/bin (or ~/.local/bin if we lack root).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/qase-tms/qase-tunnel/main/script/install.sh | bash
#
# Optional environment variables:
#   QASE_TUNNEL_VERSION   release tag to install (default: latest)
#   QASE_TUNNEL_PREFIX    install prefix override (default: /usr/local/bin or ~/.local/bin)

set -euo pipefail

REPO="qase-tms/qase-tunnel"
VERSION="${QASE_TUNNEL_VERSION:-latest}"

# --- detect OS -------------------------------------------------------------
uname_s="$(uname -s)"
case "$uname_s" in
    Linux)  os="linux"  ;;
    Darwin) os="darwin" ;;
    *)
        echo "qase-tunnel: unsupported OS '$uname_s'. Use the Windows installer for Windows." >&2
        exit 1
        ;;
esac

# --- detect arch -----------------------------------------------------------
uname_m="$(uname -m)"
case "$uname_m" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *)
        echo "qase-tunnel: unsupported architecture '$uname_m'." >&2
        exit 1
        ;;
esac

# --- resolve tag -----------------------------------------------------------
if [ "$VERSION" = "latest" ]; then
    # Resolve the latest release WITHOUT the GitHub REST API: api.github.com is
    # rate-limited to 60 req/hour per IP for unauthenticated callers, which
    # broke `latest` installs once an IP ran out of budget. The releases Atom
    # feed is served from github.com (not REST-rate-limited) AND includes
    # prereleases, so /releases/latest's prerelease exclusion isn't a problem.
    # Entries are newest-first; the first /releases/tag/<tag> link is latest.
    tag="$(curl -fsSL -A "qase-tunnel-installer" "https://github.com/${REPO}/releases.atom" \
        | grep -oE '/releases/tag/[^"<]+' | head -n1 | sed -E 's#.*/releases/tag/##')"
    if [ -z "$tag" ]; then
        echo "qase-tunnel: could not resolve latest release tag from https://github.com/${REPO}/releases" >&2
        exit 1
    fi
else
    tag="$VERSION"
fi

# GoReleaser archive name_template: <project>_<version>_<os>_<arch>.tar.gz
# Tag is vX.Y.Z, filename carries bare X.Y.Z, so strip the leading 'v'.
version_for_file="${tag#v}"
asset="qase-tunnel_${version_for_file}_${os}_${arch}.tar.gz"
asset_url="https://github.com/${REPO}/releases/download/${tag}/${asset}"
checksums_url="https://github.com/${REPO}/releases/download/${tag}/checksums.txt"

# --- pick install prefix ---------------------------------------------------
if [ -n "${QASE_TUNNEL_PREFIX:-}" ]; then
    install_dir="$QASE_TUNNEL_PREFIX"
elif [ -w /usr/local/bin ] || ([ ! -e /usr/local/bin ] && [ -w /usr/local ]); then
    install_dir="/usr/local/bin"
else
    install_dir="$HOME/.local/bin"
    mkdir -p "$install_dir"
fi

cat <<EOF

qase-tunnel installer
  repo      : ${REPO}
  tag       : ${tag}
  arch      : ${os}/${arch}
  asset     : ${asset}
  installDir: ${install_dir}

EOF

# --- download --------------------------------------------------------------
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

echo "Downloading ${asset} ..."
curl -fsSL --proto '=https' --tlsv1.2 -o "${tmpdir}/${asset}" "${asset_url}"

# --- verify checksum -------------------------------------------------------
# GoReleaser ships a single checksums.txt; grab the line matching our archive.
expected="$(curl -fsSL "${checksums_url}" 2>/dev/null \
    | awk -v f="${asset}" '$2 == f { print $1; exit }' || true)"
if [ -n "$expected" ]; then
    if command -v shasum >/dev/null 2>&1; then
        actual="$(shasum -a 256 "${tmpdir}/${asset}" | awk '{print $1}')"
    else
        actual="$(sha256sum "${tmpdir}/${asset}" | awk '{print $1}')"
    fi
    if [ "$expected" != "$actual" ]; then
        echo "qase-tunnel: SHA256 mismatch for ${asset}" >&2
        echo "  expected: $expected" >&2
        echo "  actual:   $actual" >&2
        exit 1
    fi
    echo "SHA256 verified: $actual"
else
    echo "qase-tunnel: could not fetch ${asset} entry from ${checksums_url}; skipping verification" >&2
fi

# --- extract ---------------------------------------------------------------
tar -xzf "${tmpdir}/${asset}" -C "${tmpdir}"
if [ ! -f "${tmpdir}/qase-tunnel" ]; then
    echo "qase-tunnel: binary 'qase-tunnel' not found inside ${asset}" >&2
    exit 1
fi
# frpc is bundled in the archive (see script/fetch-frpc.sh in the release
# pipeline). Installing it next to qase-tunnel keeps both on the same PATH.
if [ ! -f "${tmpdir}/frpc" ]; then
    echo "qase-tunnel: bundled 'frpc' not found inside ${asset}" >&2
    exit 1
fi
chmod +x "${tmpdir}/qase-tunnel" "${tmpdir}/frpc"

# --- install ---------------------------------------------------------------
dest="${install_dir}/qase-tunnel"
dest_frpc="${install_dir}/frpc"
if [ -w "$install_dir" ]; then
    mv "${tmpdir}/qase-tunnel" "$dest"
    mv "${tmpdir}/frpc"        "$dest_frpc"
else
    echo "qase-tunnel: ${install_dir} is not writable; using sudo to install"
    sudo mv "${tmpdir}/qase-tunnel" "$dest"
    sudo mv "${tmpdir}/frpc"        "$dest_frpc"
fi
echo "Installed: $dest"
echo "Installed: $dest_frpc"

cat <<EOF

Next:
  qase-tunnel start -a <YOUR_QASE_API_TOKEN>

EOF
