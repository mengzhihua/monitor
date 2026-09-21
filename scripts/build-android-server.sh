#!/usr/bin/env bash
# Builds monitord for Android and packages it into the Kotlin shell APK.
# The Go binary is static (CGO_ENABLED=0) and shipped as jniLibs/<abi>/libmonitord.so.
#
#   VERSION=v1.2.3 ./scripts/build-android-server.sh [assembleRelease|assembleDebug]
#
# Requires: Go, JDK 17, ANDROID_HOME (SDK 35 / build-tools 35). Output: android/app/build/outputs/apk/
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
TASK=${1:-assembleRelease}
LDFLAGS="-s -w -X main.version=$VERSION"
JNI=android/app/src/main/jniLibs

# arm64 only: Go can link android/arm64 statically without the NDK; android/amd64
# needs cgo, and x86_64 devices/emulators (API 30+) run arm64 via translation.
rm -rf "$JNI"
mkdir -p "$JNI/arm64-v8a"
(cd core && GOOS=android GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "$LDFLAGS" -o "../$JNI/arm64-v8a/libmonitord.so" ./cmd/monitord)

# versionCode: monotonically increasing from the tag (v1.2.3 -> 1020003; minor < 100,
# patch < 10000), else 1. Must match the build number in .github/workflows/release.yml.
code=1
if [[ "$VERSION" =~ ^v?([0-9]+)\.([0-9]+)\.([0-9]+) ]]; then
  (( ${BASH_REMATCH[2]} < 100 && ${BASH_REMATCH[3]} < 10000 )) || { echo "version fields out of range: $VERSION" >&2; exit 1; }
  code=$(( ${BASH_REMATCH[1]} * 1000000 + ${BASH_REMATCH[2]} * 10000 + ${BASH_REMATCH[3]} ))
fi

(cd android && ./gradlew --no-daemon -q "$TASK" -PmonitorVersion="${VERSION#v}" -PmonitorVersionCode="$code")
ls -1 android/app/build/outputs/apk/*/*.apk
