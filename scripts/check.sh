#!/usr/bin/env bash
#
# check.sh — every gate this project has, in one place, in the order CI runs
# them. `.github/workflows/ci.yml` calls exactly this script, so "green here"
# and "green in CI" cannot drift apart: a gate added to one is added to both.
#
#   scripts/check.sh
#
# PKGS is not ./... on purpose. The sandbox this repo is developed in denies
# reads under data/, and `go list ./...` walks every directory before it
# decides which are packages — so ./... fails with "open data/cache: operation
# not permitted" before a single test runs. The list below is the same twelve
# packages ./... resolves to; override it to narrow a run:
#
#   PKGS='.' scripts/check.sh
#
set -euo pipefail

cd "$(dirname "$0")/.."

PKGS=${PKGS:-"./internal/... ./tools/... ."}
# staticcheck writes its cache under XDG_CACHE_HOME by default, which the
# sandbox does not grant; point it somewhere writable instead of failing on
# the first run.
STATICCHECK_CACHE=${STATICCHECK_CACHE:-${TMPDIR:-/tmp}/staticcheck}
export STATICCHECK_CACHE
mkdir -p "$STATICCHECK_CACHE"

VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

group() { printf '::group::%s\n' "$1"; }
endgroup() { printf '::endgroup::\n'; }

group "gofmt"
# git ls-files rather than `gofmt -l .`: the recursive walk enters data/ for
# the same reason ./... does, and there is no Go there to format anyway.
# shellcheck disable=SC2046  # the file list must word-split; no path here has spaces
unformatted=$(gofmt -l $(git ls-files '*.go'))
if [ -n "$unformatted" ]; then
    printf 'gofmt needs to run on:\n%s\n' "$unformatted" >&2
    exit 1
fi
endgroup

group "go vet"
# shellcheck disable=SC2086  # PKGS is a list of patterns and must word-split
go vet $PKGS
endgroup

group "staticcheck"
# Pinned, not @latest: a linter that updates itself decides on its own day
# when the build goes red. It gates only because it read 0 findings on the
# whole tree first (the one real hit, SA4000, is fixed).
# shellcheck disable=SC2086
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 $PKGS
endgroup

group "go test"
# shellcheck disable=SC2086
go test $PKGS
endgroup

group "go build"
go build -ldflags "-X main.version=$VERSION" -o youtubehistii .
endgroup

group "pagecheck"
# The Go tests build the page as a STRING and never execute a line of its
# JavaScript. pagecheck does, and it caught a syntax error that kills the page
# before its first line. It could only ever run by hand, because the real page
# is somebody's watch history and gitignored — so a fixture stands in.
#
# The fixture is sized past 300 chains and 300 days on purpose: the virtual
# list once capped its rows there, and a smaller fixture passes whether the cap
# is back or not (measured — putting the cap back left a 45-chain fixture green
# and turned the 620-chain one red).
if ! command -v node >/dev/null 2>&1; then
    printf 'check: node not found — the page fixture cannot run its own JavaScript.\n' >&2
    printf 'check: install node 24+ (brew install node), then re-run.\n' >&2
    exit 1
fi
go run ./tools/pagefixture >"$tmp/page.html"
node tools/pagecheck/pagecheck.js "$tmp/page.html"
endgroup

printf '\nall gates green (%s)\n' "$VERSION"
