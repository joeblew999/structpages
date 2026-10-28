#!/usr/bin/env bash
# The test suite as TinyGo builds it: method code generated for TinyGo's own set of files (-tags tinygo, since some
# test files are !tinygo), then tinygo test. CI's TinyGo job runs exactly this; run it locally the same way.
set -euo pipefail
cd "$(dirname "$0")/.."

go run -C tools/gen ./cmd/structpages-gen -C "$PWD" -test -tags tinygo ./...
tinygo test "$@" . ./internal/...
