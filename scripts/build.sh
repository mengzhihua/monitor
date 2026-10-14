#!/usr/bin/env bash
# Make-free build of the embedded dashboard and/or monitord.
# Mirrors the VERSION / -dev+hash / ldflags rules in the root Makefile.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DEV_MODE=0
ARGS=()
for arg in "$@"; do
  case "$arg" in
    --dev) DEV_MODE=1 ;;
    -h|--help|help) ARGS+=("$arg") ;;
    *) ARGS+=("$arg") ;;
  esac
done
set -- "${ARGS[@]+"${ARGS[@]}"}"

VERSION="$(tr -d '[:space:]' < VERSION)"
GIT_COMMIT="$(git rev-parse HEAD 2>/dev/null || true)"
GIT_COMMIT_SHORT="$(git rev-parse --short=12 HEAD 2>/dev/null || true)"
GIT_EXACT_TAG="$(git describe --exact-match --match "v${VERSION}" --tags 2>/dev/null || true)"
RELEASE_BUILD=0
BUILD_VERSION="$VERSION"
if [[ "$GIT_EXACT_TAG" != "v${VERSION}" ]]; then
  if [[ -n "$GIT_COMMIT_SHORT" ]]; then
    BUILD_VERSION="${VERSION}-dev+${GIT_COMMIT_SHORT}"
  fi
else
  RELEASE_BUILD=1
fi
# --dev always produces a development binary even on an exact release tag.
if [[ "$DEV_MODE" -eq 1 ]]; then
  RELEASE_BUILD=0
  if [[ -n "$GIT_COMMIT_SHORT" ]]; then
    BUILD_VERSION="${VERSION}-dev+${GIT_COMMIT_SHORT}"
  else
    BUILD_VERSION="${VERSION}-dev"
  fi
fi
BUILD_DIRTY=false
if [[ -n "$(git status --porcelain --untracked-files=no 2>/dev/null || true)" ]]; then
  BUILD_DIRTY=true
fi
if [[ -n "${SOURCE_DATE_EPOCH:-}" ]]; then
  BUILD_TIME="$(date -u -d "@${SOURCE_DATE_EPOCH}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "${SOURCE_DATE_EPOCH}" +%Y-%m-%dT%H:%M:%SZ)"
else
  BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
fi
LDFLAGS="-s -w -X main.version=${BUILD_VERSION} -X main.commit=${GIT_COMMIT} -X main.releaseBuild=${RELEASE_BUILD} -X main.buildTime=${BUILD_TIME} -X main.dirty=${BUILD_DIRTY}"
BIN="core/bin"

usage() {
  cat <<'EOF'
Usage: scripts/build.sh [--dev] [web|core|all|cross|check-version]

  --dev         allow VERSION/tag drift: warn and still build -dev+sha
  web           build Vue dashboard into core/internal/api/ui/dist
  core          build monitord for the current platform (default)
  all           web then core
  cross         build server binaries for all supported platforms
  check-version run scripts/check-release-version.py
EOF
}

check_version() {
  if [[ "$DEV_MODE" -eq 1 ]]; then
    if ! python3 scripts/check-release-version.py; then
      echo "warning: VERSION check failed; continuing with --dev (${BUILD_VERSION})" >&2
    fi
    return 0
  fi
  python3 scripts/check-release-version.py
}

build_web() {
  check_version
  (cd web && npm ci && npm run build)
}

build_core() {
  if ! python3 scripts/check-release-version.py; then
    echo "warning: VERSION check failed; continuing core build (${BUILD_VERSION})" >&2
  fi
  mkdir -p "$BIN"
  (cd core && go build -trimpath -ldflags "$LDFLAGS" -o "../${BIN}/monitord" ./cmd/monitord)
  echo "built ${BIN}/monitord (${BUILD_VERSION})"
}

build_cross() {
  mkdir -p "$BIN"
  (
    cd core
    GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o "../${BIN}/monitord-linux-amd64" ./cmd/monitord
    GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o "../${BIN}/monitord-linux-arm64" ./cmd/monitord
    GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o "../${BIN}/monitord-darwin-amd64" ./cmd/monitord
    GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o "../${BIN}/monitord-darwin-arm64" ./cmd/monitord
    GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o "../${BIN}/monitord-windows-amd64.exe" ./cmd/monitord
    GOOS=freebsd GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o "../${BIN}/monitord-freebsd-amd64" ./cmd/monitord
    GOOS=freebsd GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o "../${BIN}/monitord-freebsd-arm64" ./cmd/monitord
    GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "../${BIN}/monitord-android-arm64" ./cmd/monitord
  )
  echo "cross-built under ${BIN}/ (${BUILD_VERSION})"
}

target="${1:-core}"
case "$target" in
  -h|--help|help) usage ;;
  check-version) check_version ;;
  web) build_web ;;
  core) build_core ;;
  all) build_web; build_core ;;
  cross) build_cross ;;
  *) usage; exit 2 ;;
esac
