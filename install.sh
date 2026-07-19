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
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

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

verify_checksum() {
	FILE="$1"
	ASSET_NAME="$2"
	CHECKSUMS="${TMPDIR_ZEN_PROXY}/checksums.txt"

	if [ ! -f "$CHECKSUMS" ]; then
		if ! download "$(release_url checksums.txt)" "$CHECKSUMS" 2>/dev/null; then
			warn "checksums.txt is unavailable; continuing without checksum verification"
			return 0
		fi
	fi

	EXPECTED="$(awk -v asset="$ASSET_NAME" '$2 == asset { print $1; exit }' "$CHECKSUMS")"
	if [ -z "$EXPECTED" ]; then
		warn "no published checksum for ${ASSET_NAME}; continuing without verification"
		return 0
	fi

	if command -v sha256sum >/dev/null 2>&1; then
		ACTUAL="$(sha256sum "$FILE" | awk '{ print $1 }')"
	elif command -v shasum >/dev/null 2>&1; then
		ACTUAL="$(shasum -a 256 "$FILE" | awk '{ print $1 }')"
	else
		warn "sha256sum or shasum is required for checksum verification; continuing without it"
		return 0
	fi

	if [ "$ACTUAL" != "$EXPECTED" ]; then
		warn "checksum mismatch for ${ASSET_NAME}"
		return 1
	fi
	ok "verified checksum for ${ASSET_NAME}"
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
	TMP_DEST="${DEST}.tmp.$$"
	if [ "$USE_SUDO" = "1" ]; then
		sudo cp "$SRC" "$TMP_DEST"
		sudo chmod 0755 "$TMP_DEST"
		sudo mv -f "$TMP_DEST" "$DEST"
	else
		cp "$SRC" "$TMP_DEST"
		chmod 0755 "$TMP_DEST"
		mv -f "$TMP_DEST" "$DEST"
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
		if verify_checksum "$ARCHIVE" "$ASSET_TGZ"; then
			if tar -xzf "$ARCHIVE" -C "$TMPDIR_ZEN_PROXY"; then
				if [ -x "${TMPDIR_ZEN_PROXY}/${PROJECT}" ]; then
					install_binary "${TMPDIR_ZEN_PROXY}/${PROJECT}"
					return 0
				fi
				FOUND="$(find "$TMPDIR_ZEN_PROXY" -type f -name "$PROJECT" -perm -111 | head -1)"
				if [ -n "$FOUND" ]; then
					install_binary "$FOUND"
					return 0
				fi
				warn "release archive does not contain an executable ${PROJECT}"
			else
				warn "release archive is invalid"
			fi
		fi
	fi

	info "Trying release asset ${BOLD}${ASSET_BIN}${NC}"
	if download "$(release_url "$ASSET_BIN")" "$RAWBIN" 2>/dev/null; then
		if verify_checksum "$RAWBIN" "$ASSET_BIN"; then
			chmod 0755 "$RAWBIN"
			install_binary "$RAWBIN"
			return 0
		fi
	fi

	return 1
}

build_from_source() {
	[ -f go.mod ] && [ -f "${CMD_PATH}/main.go" ] || die "source install requires running from the ${PROJECT} repository"
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
	[ -x "$CMD" ] || die "installed binary was not found at ${CMD}"
	"$CMD" --version >/dev/null || die "installed binary failed its version check"
	"$CMD" --version
}

main() {
	detect_platform
	choose_bindir
	TMPDIR_ZEN_PROXY="$(mktemp -d "${TMPDIR:-/tmp}/${PROJECT}.XXXXXX")"

	if [ "$FROM_SOURCE" = "1" ]; then
		build_from_source
	else
		find_download_tool
		if try_release_install; then
			:
		elif [ -f go.mod ] && [ -f "${CMD_PATH}/main.go" ]; then
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
