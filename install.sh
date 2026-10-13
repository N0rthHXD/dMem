#!/usr/bin/env sh
set -eu

DMEM_REPO="${DMEM_REPO:-N0rthHXD/dMem}"
case "$DMEM_REPO" in
    http://*|https://*) REPO_URL="$DMEM_REPO" ;;
    *) REPO_URL="https://github.com/$DMEM_REPO" ;;
esac

die() {
    printf '%s\n' "install: $*" >&2
    exit 1
}

usage() {
    cat <<'EOF'
Usage: install.sh [--daemon]

Install dMem for the current user:
  - binary  -> ${BINDIR:-$HOME/.local/bin}/dmemd
  - config  -> ${CONFIGDIR:-$HOME/.config/dmem}/config.toml

When run from a source checkout the binary is built locally; otherwise a
prebuilt release archive is downloaded (no Go or C compiler required).

Options:
  --daemon    start the daemon in the background after installing
  -h, --help  show this help

Environment:
  BINDIR        binary install directory (default: $HOME/.local/bin)
  CONFIGDIR     config install directory (default: $HOME/.config/dmem)
  FORCE=1       overwrite an existing config.toml
  DMEM_VERSION  release tag to download, or "latest" (default: latest)
  DMEM_PREBUILT set to 1 to always download a prebuilt release
  DMEM_REPO     repository override for forks/tests (default: N0rthHXD/dMem)
  DMEM_DOWNLOAD_BASE  override the asset base URL (mirror/proxy), e.g.
                      https://ghproxy.com/https://github.com/N0rthHXD/dMem/releases/latest/download
  https_proxy   standard proxy honored by curl/wget
  VERSION       version stamped when building from source (default: git describe)
EOF
}

fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --retry 4 --retry-delay 2 --retry-connrefused "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget --tries=4 --timeout=30 -qO "$2" "$1"
    else
        die "curl or wget is required to download the release"
    fi
}

download() {
    # Retry, then fall back to HTTP/1.1 which avoids some TLS resets on
    # networks that terminate HTTP/2 connections.
    if fetch "$1" "$2"; then
        return 0
    fi
    if command -v curl >/dev/null 2>&1; then
        printf '%s\n' "install: retrying $1 over HTTP/1.1" >&2
        curl -fsSL --http1.1 --retry 4 --retry-delay 2 --retry-connrefused "$1" -o "$2" && return 0
    fi
    return 1
}

install_config() {
    if [ -f "$CONFIGDIR/config.toml" ] && [ -z "$FORCE" ]; then
        printf '%s\n' "keeping existing $CONFIGDIR/config.toml (set FORCE=1 to overwrite)"
    else
        install -m 0644 "$1" "$CONFIGDIR/config.toml"
        printf '%s\n' "installed $CONFIGDIR/config.toml"
    fi
}

START_DAEMON=0
while [ "$#" -gt 0 ]; do
    case "$1" in
        --daemon) START_DAEMON=1 ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown argument: $1 (try --help)" ;;
    esac
    shift
done

BINDIR="${BINDIR:-$HOME/.local/bin}"
CONFIGDIR="${CONFIGDIR:-$HOME/.config/dmem}"
FORCE="${FORCE:-}"
DMEM_VERSION="${DMEM_VERSION:-latest}"
DMEM_PREBUILT="${DMEM_PREBUILT:-}"

PATH_HINT=0
case ":${PATH:-}:" in
    *":$BINDIR:"*) ;;
    *) PATH_HINT=1 ;;
esac

mkdir -p "$BINDIR" "$CONFIGDIR"

source_dir=""
if [ -f "$0" ]; then
    script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd || printf '%s' "$PWD")
    if [ -f "$script_dir/Makefile" ] && [ -d "$script_dir/cmd/dmemd" ]; then
        source_dir="$script_dir"
    fi
fi

if [ -n "$source_dir" ] && [ -z "$DMEM_PREBUILT" ]; then
    VERSION="${VERSION:-$(git -C "$source_dir" describe --tags --always --dirty 2>/dev/null || true)}"
    [ -n "$VERSION" ] || VERSION=0.1.0
    case "$VERSION" in
        ''|*[!0-9A-Za-z.+:~_-]*) die "invalid VERSION: $VERSION" ;;
    esac
    command -v go >/dev/null 2>&1 || die "go not found; install Go 1.24 or newer"
    command -v make >/dev/null 2>&1 || die "make not found"
    cc=""
    for candidate in cc gcc clang; do
        if command -v "$candidate" >/dev/null 2>&1; then
            cc="$candidate"
            break
        fi
    done
    [ -n "$cc" ] || die "C compiler not found; install cc, gcc, or clang"
    make -C "$source_dir" install VERSION="$VERSION" BINDIR="$BINDIR" CONFIGDIR="$CONFIGDIR" FORCE="$FORCE"
else
    machine=$(uname -m)
    case "$machine" in
        x86_64|amd64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) die "unsupported architecture: $machine (prebuilt releases: amd64, arm64)" ;;
    esac
    if [ -n "${DMEM_DOWNLOAD_BASE:-}" ]; then
        base="${DMEM_DOWNLOAD_BASE%/}"
    elif [ "$DMEM_VERSION" = "latest" ]; then
        base="$REPO_URL/releases/latest/download"
    else
        tag="$DMEM_VERSION"
        case "$tag" in v*) ;; *) tag="v$tag" ;; esac
        base="$REPO_URL/releases/download/$tag"
    fi
    command -v tar >/dev/null 2>&1 || die "tar is required to unpack the release"
    asset="dmem_linux_$arch.tar.gz"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT INT TERM
    printf '%s\n' "downloading $base/$asset"
    download "$base/$asset" "$tmp/$asset" || die "failed to download $base/$asset; check your network or set https_proxy/DMEM_DOWNLOAD_BASE"
    if download "$base/$asset.sha256" "$tmp/$asset.sha256" 2>/dev/null; then
        if command -v sha256sum >/dev/null 2>&1; then
            ( cd "$tmp" && sha256sum -c "$asset.sha256" >/dev/null ) || die "checksum verification failed for $asset"
        fi
    fi
    mkdir -p "$tmp/pkg"
    tar -xzf "$tmp/$asset" -C "$tmp/pkg"
    [ -f "$tmp/pkg/dmemd" ] || die "release archive is missing dmemd"
    install -m 0755 "$tmp/pkg/dmemd" "$BINDIR/dmemd"
    printf '%s\n' "installed $BINDIR/dmemd"
    if [ -f "$tmp/pkg/config.toml" ]; then
        install_config "$tmp/pkg/config.toml"
    fi
fi

CONFIG_FILE="$CONFIGDIR/config.toml"
"$BINDIR/dmemd" --config "$CONFIG_FILE" config validate

if "$BINDIR/dmemd" --config "$CONFIG_FILE" init; then
    :
else
    printf '%s\n' "install: init skipped (database may already exist); continuing" >&2
fi

if [ "$PATH_HINT" = 1 ]; then
    printf '%s\n' "add to PATH:"
    printf '%s\n' "  export PATH=\"$BINDIR:\$PATH\""
fi

if [ "$START_DAEMON" = 1 ]; then
    cache="${XDG_CACHE_HOME:-$HOME/.cache}/dmem"
    mkdir -p "$cache"
    nohup "$BINDIR/dmemd" --config "$CONFIG_FILE" daemon >>"$cache/dmem.log" 2>&1 &
    printf '%s\n' "started dmemd daemon (log: $cache/dmem.log)"
fi

printf '%s\n' "run the daemon:"
printf '%s\n' "  $BINDIR/dmemd daemon"
