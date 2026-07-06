#!/bin/sh

set -eu

PROJECT="zen-proxy"
REPO="${ZEN_PROXY_REPO:-azain47/zen-proxy}"
CMD_PATH="cmd/${PROJECT}"
VERSION="${ZEN_PROXY_VERSION:-latest}"
BINDIR="${ZEN_PROXY_BINDIR:-}"
FROM_SOURCE="${ZEN_PROXY_FROM_SOURCE:-}"
GITHUB="https://github.com/${REPO}"

if [ -t 1 ]; then
	BOLD="$(printf '\033[1m')"
	GREEN="$(printf '\033[32m')"
	YELLOW="$(printf '\033[33m')"
	RED="$(printf '\033[31m')"
	NC="$(printf '\033[0m')"
else
	BOLD=""
	GREEN=""
	YELLOW=""
	RED=""
	NC=""
fi

info() { printf '%s\n' "$*"; }
ok() { printf '%sOK%s %s\n' "$GREEN" "$NC" "$*"; }
warn() { printf '%sWARN%s %s\n' "$YELLOW" "$NC" "$*"; }
die() {
	printf '%sERR%s %s\n' "$RED" "$NC" "$*" >&2
	exit 1
}

cleanup() {
	if [ -n "${TMPDIR_ZEN_PROXY:-}" ] && [ -d "$TMPDIR_ZEN_PROXY" ]; then
		rm -rf "$TMPDIR_ZEN_PROXY"
	fi
}
trap cleanup EXIT INT TERM

detect_platform() {
	case "$(uname -s)" in
	Darwin) OS="darwin" ;;
	Linux) OS="linux" ;;
	*) die "unsupported OS: $(uname -s)" ;;
	esac

	case "$(uname -m)" in
	x86_64 | amd64) ARCH="amd64" ;;
	arm64 | aarch64) ARCH="arm64" ;;
	*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

find_download_tool() {
	if command -v curl >/dev/null 2>&1; then
		DL="curl"
	elif command -v wget >/dev/null 2>&1; then
		DL="wget"
	else
		die "curl or wget is required"
	fi
}

download() {
	if [ "$DL" = "curl" ]; then
		curl -fsSL "$1" -o "$2"
	else
		wget -q -O "$2" "$1"
	fi
}

choose_bindir() {
	USE_SUDO=0
	if [ -n "$BINDIR" ]; then
		:
	elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
		BINDIR="/usr/local/bin"
	elif [ -d /usr/local/bin ] && command -v sudo >/dev/null 2>&1; then
		BINDIR="/usr/local/bin"
		USE_SUDO=1
	else
		BINDIR="${HOME}/.local/bin"
	fi

	if [ "$USE_SUDO" = "1" ]; then
		sudo mkdir -p "$BINDIR"
	else
		mkdir -p "$BINDIR"
	fi

	case ":${PATH:-}:" in
	*":${BINDIR}:"*) ;;
	*) warn "${BINDIR} is not in PATH" ;;
	esac
}

install_binary() {
	SRC="$1"
	DEST="${BINDIR}/${PROJECT}"
	if [ "$USE_SUDO" = "1" ]; then
		sudo cp "$SRC" "$DEST"
		sudo chmod 0755 "$DEST"
	else
		cp "$SRC" "$DEST"
		chmod 0755 "$DEST"
	fi
	ok "installed ${PROJECT} to ${DEST}"
}

release_url() {
	ASSET="$1"
	if [ "$VERSION" = "latest" ]; then
		printf '%s/releases/latest/download/%s' "$GITHUB" "$ASSET"
	else
		printf '%s/releases/download/%s/%s' "$GITHUB" "$VERSION" "$ASSET"
	fi
}

try_release_install() {
	ASSET_TGZ="${PROJECT}_${OS}_${ARCH}.tar.gz"
	ASSET_BIN="${PROJECT}_${OS}_${ARCH}"
	ARCHIVE="${TMPDIR_ZEN_PROXY}/${ASSET_TGZ}"
	RAWBIN="${TMPDIR_ZEN_PROXY}/${ASSET_BIN}"

	info "Trying release asset ${BOLD}${ASSET_TGZ}${NC}"
	if download "$(release_url "$ASSET_TGZ")" "$ARCHIVE" 2>/dev/null; then
		tar -xzf "$ARCHIVE" -C "$TMPDIR_ZEN_PROXY"
		if [ -x "${TMPDIR_ZEN_PROXY}/${PROJECT}" ]; then
			install_binary "${TMPDIR_ZEN_PROXY}/${PROJECT}"
			return 0
		fi
		FOUND="$(find "$TMPDIR_ZEN_PROXY" -type f -name "$PROJECT" -perm -111 | head -1)"
		if [ -n "$FOUND" ]; then
			install_binary "$FOUND"
			return 0
		fi
	fi

	info "Trying release asset ${BOLD}${ASSET_BIN}${NC}"
	if download "$(release_url "$ASSET_BIN")" "$RAWBIN" 2>/dev/null; then
		chmod 0755 "$RAWBIN"
		install_binary "$RAWBIN"
		return 0
	fi

	return 1
}

build_from_source() {
	[ -f go.mod ] || die "source install requires running from the ${PROJECT} repository"
	command -v go >/dev/null 2>&1 || die "Go is required for source install"

	BIN="${TMPDIR_ZEN_PROXY}/${PROJECT}"
	BUILD_VERSION="$VERSION"
	if [ "$BUILD_VERSION" = "latest" ]; then
		BUILD_VERSION="dev"
	fi

	info "Building ${PROJECT} from source"
	go build -ldflags "-s -w -X main.version=${BUILD_VERSION}" -o "$BIN" "./${CMD_PATH}"
	install_binary "$BIN"
}

go_install() {
	command -v go >/dev/null 2>&1 || return 1

	GOBIN_DIR="${TMPDIR_ZEN_PROXY}/gobin"
	mkdir -p "$GOBIN_DIR"
	PKG="github.com/${REPO}/${CMD_PATH}@${VERSION}"
	info "Trying ${BOLD}go install ${PKG}${NC}"
	if GOBIN="$GOBIN_DIR" go install "$PKG"; then
		if [ -x "${GOBIN_DIR}/${PROJECT}" ]; then
			install_binary "${GOBIN_DIR}/${PROJECT}"
			return 0
		fi
	fi
	return 1
}

verify_install() {
	CMD="${BINDIR}/${PROJECT}"
	if [ -x "$CMD" ]; then
		"$CMD" --version || true
	else
		warn "installed binary was not found at ${CMD}"
	fi
}

main() {
	detect_platform
	find_download_tool
	choose_bindir
	TMPDIR_ZEN_PROXY="$(mktemp -d "${TMPDIR:-/tmp}/${PROJECT}.XXXXXX")"

	if [ "$FROM_SOURCE" = "1" ]; then
		build_from_source
	elif ! try_release_install; then
		if [ -f go.mod ]; then
			warn "release download failed; falling back to local source build"
			build_from_source
		elif go_install; then
			:
		else
			die "release download failed; install Go or run from a source checkout"
		fi
	fi

	verify_install
	info ""
	info "Run: ${BOLD}${PROJECT}${NC}"
	info "Health: http://127.0.0.1:8788/health"
}

main "$@"
