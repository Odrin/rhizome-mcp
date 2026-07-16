#!/bin/sh
set -eu

REPO="${RHIZOME_REPO:-Odrin/rhizome-mcp}"
BIN_NAME="rhizome-mcp"
INSTALL_DIR="${RHIZOME_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${RHIZOME_VERSION:-latest}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *)
    echo "Unsupported architecture: $arch" >&2
    exit 1
    ;;
esac

case "$os" in
  linux|darwin) ;;
  *)
    echo "Unsupported OS: $os" >&2
    exit 1
    ;;
esac

if [ "$VERSION" = "latest" ]; then
  release_json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")"
else
  release_json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/tags/${VERSION}")"
fi
tag="$(printf '%s' "$release_json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
if [ -z "$tag" ]; then
  echo "Failed to resolve release tag from GitHub API." >&2
  exit 1
fi
version="${tag#v}"
asset="${BIN_NAME}_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/${REPO}/releases/download/${tag}"
archive_url="${base_url}/${asset}"
checksum_url="${archive_url}.sha256"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

archive_path="${tmp_dir}/${asset}"
checksum_path="${archive_path}.sha256"
curl -fsSL "$archive_url" -o "$archive_path"
curl -fsSL "$checksum_url" -o "$checksum_path"

expected="$(cat "$checksum_path" | tr -d ' \n\r')"
actual="$(shasum -a 256 "$archive_path" | awk '{print $1}')"
if [ "$expected" != "$actual" ]; then
  echo "Checksum verification failed for ${asset}" >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
tar -xzf "$archive_path" -C "$tmp_dir"
install -m 0755 "${tmp_dir}/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"

echo "Installed ${BIN_NAME} ${tag} to ${INSTALL_DIR}/${BIN_NAME}"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) echo "PATH already includes ${INSTALL_DIR}" ;;
  *)
    echo "${INSTALL_DIR} is not in PATH."
    echo "Add it manually, for example:"
    echo "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.profile"
    ;;
esac

