#!/usr/bin/env bash
# Standard verification: formatting, vet, tests, static cross-builds.
# Run from the repository root:  ./scripts/check.sh
# Extra arguments are passed to `go test` (e.g. -run TestX -v).
# RACE=1 runs the tests with the race detector (needs gcc).
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PATH:/usr/local/go/bin"
# Builds do not need VCS stamping (the version comes from build-release.sh).
export GOFLAGS="${GOFLAGS:-} -buildvcs=false"

# UI_CHECK=1 also type-checks and rebuilds the web UI (needs web/node_modules).
if [[ "${UI_CHECK:-}" == 1 ]]; then
  echo "== web: svelte-check + build"
  (cd web && npx svelte-check --tsconfig ./tsconfig.json --fail-on-warnings && npx vite build --logLevel warn)
fi

echo "== gofmt"
unformatted=$(find . -name '*.go' -not -path './web/*' -not -path './dist/*' -print0 | xargs -0 gofmt -l)
if [[ -n "$unformatted" ]]; then
  echo "not gofmt-ed:"; echo "$unformatted"; exit 1
fi

echo "== go vet"
go vet ./...

echo "== go test"
if [[ "${RACE:-}" == 1 ]]; then
  CGO_ENABLED=1 go test -race -count=1 "$@" ./...
else
  go test -count=1 "$@" ./...
fi

echo "== build (CGO_ENABLED=0)"
for target in linux/amd64 windows/amd64 darwin/arm64; do
  CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" go build -o /dev/null ./cmd/estopo
  CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" go build -o /dev/null ./cmd/essim
  echo "   $target ok"
done

echo "== OK"
