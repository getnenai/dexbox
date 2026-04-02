#!/bin/sh
# Install script for dexbox.
#
# Usage:
#   curl -sSfL https://raw.githubusercontent.com/getnenai/dexbox/main/install.sh | sh
#   curl -sSfL https://raw.githubusercontent.com/getnenai/dexbox/main/install.sh | sh -s -- -b /usr/local/bin
#   curl -sSfL https://raw.githubusercontent.com/getnenai/dexbox/main/install.sh | sh -s -- -v v1.0.0

set -e

REPO="getnenai/dexbox"
BINARY="dexbox"
INSTALL_DIR="${HOME}/.local/bin"
VERSION=""

usage() {
  echo "Usage: install.sh [-b install_dir] [-v version]"
  echo "  -b    Install directory (default: ~/.local/bin)"
  echo "  -v    Version tag to install (default: latest)"
  exit 1
}

while getopts "b:v:h" opt; do
  case "$opt" in
    b) INSTALL_DIR="$OPTARG" ;;
    v) VERSION="$OPTARG" ;;
    h) usage ;;
    *) usage ;;
  esac
done

# Detect OS
detect_os() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in
    darwin) echo "darwin" ;;
    linux)  echo "linux" ;;
    *)
      echo "Error: unsupported OS: $os" >&2
      exit 1
      ;;
  esac
}

# Detect architecture
detect_arch() {
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64)   echo "amd64" ;;
    arm64|aarch64)   echo "arm64" ;;
    *)
      echo "Error: unsupported architecture: $arch" >&2
      exit 1
      ;;
  esac
}

# Download a URL to a file, using curl or wget
download() {
  url="$1"
  dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -sSfL "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$url" -O "$dest"
  else
    echo "Error: curl or wget is required" >&2
    exit 1
  fi
}

# Get the latest release tag from GitHub
get_latest_version() {
  url="https://api.github.com/repos/${REPO}/releases/latest"
  if command -v curl >/dev/null 2>&1; then
    tag=$(curl -sSfL "$url" | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
  elif command -v wget >/dev/null 2>&1; then
    tag=$(wget -qO- "$url" | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
  else
    echo "Error: curl or wget is required" >&2
    exit 1
  fi
  if [ -z "$tag" ]; then
    echo "Error: could not determine latest version. Check https://github.com/${REPO}/releases" >&2
    exit 1
  fi
  echo "$tag"
}

OS=$(detect_os)
ARCH=$(detect_arch)

echo "Detected: ${OS}/${ARCH}"

# Resolve version
if [ -z "$VERSION" ]; then
  echo "Fetching latest release..."
  VERSION=$(get_latest_version)
fi
echo "Version: ${VERSION}"

# Construct download URLs
ARCHIVE="dexbox_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
ARCHIVE_URL="${BASE_URL}/${ARCHIVE}"
CHECKSUM_URL="${BASE_URL}/checksums.txt"

# Create temp directory with cleanup
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "Downloading ${ARCHIVE}..."
download "$ARCHIVE_URL" "${TMPDIR}/${ARCHIVE}"

echo "Downloading checksums..."
download "$CHECKSUM_URL" "${TMPDIR}/checksums.txt"

# Verify checksum
echo "Verifying checksum..."
EXPECTED=$(awk -v f="${ARCHIVE}" '$2 == f {print $1}' "${TMPDIR}/checksums.txt")
if [ -z "$EXPECTED" ]; then
  echo "Error: checksum not found for ${ARCHIVE}" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')
else
  echo "Error: sha256sum or shasum is required for checksum verification" >&2
  exit 1
fi

if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "Error: checksum mismatch" >&2
  echo "  expected: ${EXPECTED}" >&2
  echo "  got:      ${ACTUAL}" >&2
  exit 1
fi
echo "Checksum OK"

# Extract
echo "Extracting..."
tar xzf "${TMPDIR}/${ARCHIVE}" -C "$TMPDIR"

# Install
mkdir -p "$INSTALL_DIR"
mv "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
chmod +x "${INSTALL_DIR}/${BINARY}"

# Codesign on macOS
if [ "$OS" = "darwin" ]; then
  if ! codesign -f -s - "${INSTALL_DIR}/${BINARY}" >/dev/null 2>&1; then
    echo "Warning: codesign failed; the binary may be blocked by macOS" >&2
  fi
fi

echo ""
echo "Installed ${BINARY} ${VERSION} to ${INSTALL_DIR}/${BINARY}"

# Check PATH
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo ""
    echo "Warning: ${INSTALL_DIR} is not in your PATH."
    echo "Add it with:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    echo ""
    echo "Or add that line to your ~/.bashrc, ~/.zshrc, or ~/.profile."
    ;;
esac

echo ""
echo "Get started:"
echo "  dexbox --version"
echo "  dexbox start"
