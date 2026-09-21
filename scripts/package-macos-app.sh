#!/usr/bin/env bash
# Split a universal (arm64 + x86_64) macOS .app into one thinned .app per
# architecture and package each as .zip and .dmg.
#
#   ./scripts/package-macos-app.sh <path/to/App.app> <outdir> <name-prefix>
#   -> <outdir>/<prefix>-macos-arm64.{zip,dmg}  <outdir>/<prefix>-macos-amd64.{zip,dmg}
set -euo pipefail

app=$1; out=$2; prefix=$3
[ -d "$app" ] || { echo "no .app at $app" >&2; exit 1; }
mkdir -p "$out"
appname=$(basename "$app")

# every Mach-O inside the bundle (main executable, frameworks, dylibs, helpers)
machos() {
  find "$1" -type f \( -perm -u+x -o -name '*.dylib' \) -print0 |
    while IFS= read -r -d '' f; do
      if file -b "$f" | grep -q 'Mach-O'; then printf '%s\0' "$f"; fi
    done
}

main="$app/Contents/MacOS/$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$app/Contents/Info.plist")"
archs=$(lipo -archs "$main")
echo "bundle archs: $archs"

for pair in arm64:arm64 x86_64:amd64; do
  arch=${pair%%:*}; label=${pair##*:}
  if ! grep -qw "$arch" <<<"$archs"; then
    echo "missing $arch slice in $main; build must be universal" >&2
    exit 1
  fi
  work=$(mktemp -d)
  cp -R "$app" "$work/$appname"
  machos "$work/$appname" | while IFS= read -r -d '' f; do
    if lipo -archs "$f" 2>/dev/null | grep -qw "$arch" && [ "$(lipo -archs "$f" | wc -w)" -gt 1 ]; then
      lipo -thin "$arch" "$f" -output "$f.thin" && mv "$f.thin" "$f"
    fi
  done
  # ad-hoc re-sign so the thinned bundle still launches (real signing is a later step)
  codesign --force --deep --sign - "$work/$appname" >/dev/null 2>&1 || true

  name="$prefix-macos-$label"
  ditto -c -k --sequesterRsrc --keepParent "$work/$appname" "$out/$name.zip"
  staging=$(mktemp -d)
  cp -R "$work/$appname" "$staging/"
  ln -s /Applications "$staging/Applications"
  hdiutil create -quiet -volname "Monitor" -srcfolder "$staging" -ov -format UDZO "$out/$name.dmg"
  rm -rf "$work" "$staging"
  echo "packaged $name"
done
ls -l "$out"
