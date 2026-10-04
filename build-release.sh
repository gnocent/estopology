#!/usr/bin/env bash
# Builds the static release binaries (estopo, essim) for Linux, Windows and
# macOS (Apple Silicon):
#   dist/<os>-<arch>/        estopo, essim, LICENSE, THIRD_PARTY_NOTICES.md,
#                            SHA256SUMS — ready to run
#   dist/release/            the same binaries named <cmd>-<os>-<arch>[.exe],
#                            plus one SHA256SUMS: the assets of a GitHub release
# VERSION defaults to `git describe`; override with VERSION=1.2.3 ./build-release.sh
set -euo pipefail
cd "$(dirname "$0")"
export PATH="$PATH:/usr/local/go/bin"
rm -rf dist

git_() { git -c safe.directory="$PWD" "$@"; }
VERSION="${VERSION:-$(git_ describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="$(git_ rev-parse --short HEAD 2>/dev/null || echo unknown)"
# The date of the commit, not of the build: two builds of one commit are identical.
DATE="$(git_ log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)"
PKG=estopo/internal/buildinfo
LDFLAGS="-s -w -X $PKG.Version=$VERSION -X $PKG.Commit=$COMMIT -X $PKG.Date=$DATE"

mkdir -p dist/release

# build <goos> <goarch> <name of the platform in file names> <extension>
build() {
  local goos=$1 goarch=$2 name=$3 ext=$4
  local outdir="dist/$name"
  mkdir -p "$outdir"
  for cmd in estopo essim; do
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o "$outdir/$cmd$ext" "./cmd/$cmd"
    cp "$outdir/$cmd$ext" "dist/release/$cmd-$name$ext"
  done
  cp LICENSE THIRD_PARTY_NOTICES.md "$outdir/"
  (cd "$outdir" && sha256sum "estopo$ext" "essim$ext" > SHA256SUMS)
}

build linux   amd64 linux-amd64   ""
build windows amd64 windows-amd64 ".exe"
build darwin  arm64 macos-arm64   ""

cp LICENSE THIRD_PARTY_NOTICES.md dist/release/
(cd dist/release && sha256sum estopo-* essim-* > SHA256SUMS)

echo "version $VERSION ($COMMIT)"
du -h dist/release/*
