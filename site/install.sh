#!/bin/sh
# dokk installer: downloads the latest release, installs /usr/local/bin/dokk
# and starts it as a systemd service.
#
#   curl -fsSL https://eduardotorresdev.github.io/dokk/install.sh | sudo sh
#
# Options (env vars):
#   DOKK_VERSION=v0.1.0      install a specific version (default: latest)
#   DOKK_ADDR=0.0.0.0:7070   address the panel listens on (default: 127.0.0.1:7070)
#   DOKK_NO_SERVICE=1        only install the binary, no systemd unit
set -eu

REPO="eduardotorresdev/dokk"
BIN="/usr/local/bin/dokk"
UNIT="/etc/systemd/system/dokk.service"
ADDR="${DOKK_ADDR:-127.0.0.1:7070}"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = "Linux" ] || die "dokk runs on Linux only."
[ "$(id -u)" -eq 0 ] || die "run as root: curl -fsSL https://eduardotorresdev.github.io/dokk/install.sh | sudo sh"

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m) (amd64 and arm64 only)" ;;
esac

# Dokku itself (which dokk can install for you) only supports Ubuntu/Debian.
if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  case "${ID:-}" in
    ubuntu | debian) ;;
    *) warn "${PRETTY_NAME:-this distro} is not supported by the Dokku installer. dokk will run, but needs an existing Dokku. Use Ubuntu or Debian for a new server." ;;
  esac
fi

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
else
  die "curl or wget is required."
fi

if [ -n "${DOKK_BASE_URL:-}" ]; then
  BASE="$DOKK_BASE_URL"
elif [ -n "${DOKK_VERSION:-}" ]; then
  BASE="https://github.com/$REPO/releases/download/$DOKK_VERSION"
else
  BASE="https://github.com/$REPO/releases/latest/download"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

FILE="dokk_linux_$ARCH.tar.gz"
say "Downloading $FILE (${DOKK_VERSION:-latest})"
fetch "$BASE/$FILE" "$TMP/$FILE" || die "download failed: $BASE/$FILE"
fetch "$BASE/checksums.txt" "$TMP/checksums.txt" || die "download failed: $BASE/checksums.txt"

say "Verifying checksum"
EXPECTED="$(grep " $FILE\$" "$TMP/checksums.txt" | cut -d' ' -f1)"
[ -n "$EXPECTED" ] || die "$FILE not found in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "$TMP/$FILE" | cut -d' ' -f1)"
else
  ACTUAL="$(openssl dgst -sha256 "$TMP/$FILE" | awk '{print $NF}')"
fi
[ "$EXPECTED" = "$ACTUAL" ] || die "checksum mismatch for $FILE"

tar -xzf "$TMP/$FILE" -C "$TMP" dokk
install -m 0755 "$TMP/dokk" "$BIN.new"
mv "$BIN.new" "$BIN"
say "Installed $("$BIN" -version) to $BIN"

if [ "${DOKK_NO_SERVICE:-}" = "1" ] || ! command -v systemctl >/dev/null 2>&1 || [ ! -d /run/systemd/system ]; then
  say "Skipping systemd service. Start it with: $BIN -addr $ADDR"
  exit 0
fi

# Upgrades keep the existing unit (and its -addr) unless DOKK_ADDR is set.
if [ ! -f "$UNIT" ] || [ -n "${DOKK_ADDR:-}" ]; then
  cat >"$UNIT" <<EOF
[Unit]
Description=dokk - web panel for Dokku
After=network-online.target docker.service
Wants=network-online.target

[Service]
ExecStart=$BIN -addr $ADDR
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
fi

systemctl daemon-reload
systemctl enable dokk >/dev/null 2>&1
systemctl restart dokk
say "dokk is running (systemctl status dokk)"

PORT="${ADDR##*:}"
case "$ADDR" in
  127.0.0.1:* | localhost:*)
    printf '\nOpen the panel through an SSH tunnel:\n  ssh -L %s:127.0.0.1:%s root@<server>  ->  http://localhost:%s\n' "$PORT" "$PORT" "$PORT"
    printf 'Or expose it: curl -fsSL https://eduardotorresdev.github.io/dokk/install.sh | sudo DOKK_ADDR=0.0.0.0:%s sh\n\n' "$PORT"
    ;;
  *)
    printf '\nOpen the panel: http://<server-ip>:%s\n\n' "$PORT"
    ;;
esac
