#!/usr/bin/env bash
# Compatibility entry point; all reproduction and result checks live in Go.
set -euo pipefail

export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off
export GOENV=off GOFLAGS= GOVCS='*:off' GOTELEMETRY=off GO111MODULE=on
export GOOS= GOARCH= GOEXPERIMENT=
export GOPRIVATE= GONOPROXY=none GONOSUMDB=none GOINSECURE= GOAUTH=off GOCACHEPROG=
if [[ $# != 1 || -z $1 ]]; then
	printf 'usage: %s <prepared-local-source>\n' "$0" >&2
	exit 2
fi
if ! SOURCE=$(CDPATH= cd -- "$1" && pwd -P); then
	printf 'upstreamrepro: supply an existing prepared local source directory; no clone or fetch is attempted\n' >&2
	exit 1
fi
HERE=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
cd -- "$HERE/../.."
if ! command -v go >/dev/null; then
	printf 'upstreamrepro: install a compatible local Go toolchain and put it on PATH; downloads are disabled\n' >&2
	exit 1
fi

# Build once so the runner receives signals and its exit status is preserved.
TEMP_ROOT=$(CDPATH= cd -- "${TMPDIR:-/tmp}" && pwd -P)
case "$TEMP_ROOT/" in
	"$SOURCE/"*|"$PWD/"*)
		printf 'upstreamrepro: set TMPDIR to a directory outside the source and project\n' >&2
		exit 1
		;;
esac
LAUNCH=$(mktemp -d "$TEMP_ROOT/upstreamrepro-launch.XXXXXXXX")
CHILD=
BOOTSTRAP=
cleanup() {
	trap '' INT TERM
	if [[ -n $BOOTSTRAP ]]; then
		# The build and all compiler descendants have their own process group.
		if kill -KILL -- "-$BOOTSTRAP" 2>/dev/null; then :; fi
		if wait "$BOOTSTRAP" 2>/dev/null; then :; fi
	fi
	rm -rf -- "$LAUNCH"
}
trap cleanup EXIT
stop() {
	trap '' INT TERM
	if [[ -n $CHILD && -z $BOOTSTRAP ]]; then
		if kill -TERM "$CHILD" 2>/dev/null; then :; fi
		if wait "$CHILD"; then :; fi
	fi
	exit "$1"
}
# Defer interruptions until the separate build group has been recorded.
PENDING=
trap 'PENDING=130' INT
trap 'PENDING=143' TERM
set -m
GOCACHE="$LAUNCH/build-cache" GOMODCACHE="$LAUNCH/module-cache" GOTMPDIR="$LAUNCH" \
	go build -buildvcs=false -o "$LAUNCH/upstreamrepro" ./scripts/upstreamrepro &
BOOTSTRAP=$!
set +m
trap 'stop 130' INT
trap 'stop 143' TERM
if [[ -n $PENDING ]]; then stop "$PENDING"; fi
wait "$BOOTSTRAP"
BOOTSTRAP=
"$LAUNCH/upstreamrepro" "$SOURCE" &
CHILD=$!
wait "$CHILD"
