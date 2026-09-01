#!/usr/bin/env bash
#
# Golden parity harness (plan §9.1).
#
# The reference implementation is fully deterministic. With detection disabled
# and an explicit --ranges, this Go port must produce byte-identical output to
# gyrofix.cli on the same input. Anything else is a bug, not a rounding
# difference — which is why the numeric core forbids math libraries and writes
# every a*b+c with an explicit rounding barrier.
#
# usage: testdata/golden/parity.sh <path-to-DJIGyroFix-checkout> [workdir]
#
# The reference is not vendored: it is GPL-3.0 like this port, but pinning a
# copy of someone else's tree in this repo would rot. Clone it yourself:
#   git clone --depth 1 https://github.com/kim2160/DJIGyroFix.git
#
# Real DJI footage is the better input if you have it — drop .MP4 files into
# $WORKDIR/corpus and they are picked up automatically alongside the synthetic
# fixtures.

set -euo pipefail

REFERENCE=${1:?usage: parity.sh <path-to-DJIGyroFix-checkout> [workdir]}
WORKDIR=${2:-$(mktemp -d)}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)

if [[ ! -f "$REFERENCE/gyrofix/cli.py" ]]; then
	echo "not a DJIGyroFix checkout: $REFERENCE" >&2
	exit 2
fi

mkdir -p "$WORKDIR/corpus"
echo "workdir: $WORKDIR"

go build -o "$WORKDIR/djgyrofix" "$ROOT/cmd/djgyrofix"
go build -o "$WORKDIR/mkfixture" "$ROOT/tools/mkfixture"

# Synthetic inputs covering every variant and both defect shapes. The reference
# and the port must agree on all of them.
for variant in wm169 wa530 oq101; do
	for kind in mixed jitter impact clean; do
		name="$WORKDIR/corpus/synth_${variant}_${kind}.MP4"
		[[ -f $name ]] || "$WORKDIR/mkfixture" -o "$name" -variant "$variant" -kind "$kind" -seconds 30 >/dev/null
	done
done

# Each case is "START END SMOOTHING_MS STRENGTH".
CASES=(
	"8.0 9.5 180 1.0"
	"8.0 9.5 60 1.0"
	"8.0 9.5 400 1.0"
	"8.0 9.5 180 0.35"
	"14.9 15.2 90 1.0"
	"0.5 29.0 180 1.0"
	"12.5 14.0 180 0.75"
	"00:00:20.0 00:00:21.5 220 1.0"
)

pass=0
fail=0
for input in "$WORKDIR"/corpus/*.MP4 "$WORKDIR"/corpus/*.mp4 "$WORKDIR"/corpus/*.MOV; do
	[[ -e $input ]] || continue
	for case in "${CASES[@]}"; do
		read -r start end smoothing strength <<<"$case"
		py="$WORKDIR/py.MP4"
		go="$WORKDIR/go.MP4"
		rm -f "$py" "$go" "$go.gyrofix.json"

		if ! (cd "$REFERENCE" && PYTHONPATH=. python3 -m gyrofix.cli "$input" "$start" "$end" \
			--smoothing-ms "$smoothing" --output "$py" >/dev/null 2>&1); then
			echo "SKIP $(basename "$input") $start-$end sm=$smoothing st=$strength (reference declined)"
			continue
		fi
		# The reference CLI has no strength flag, so only strength 1.0 can be
		# compared end to end. Other values are covered by the Go-side fixture
		# test against smooth_quaternions directly.
		if [[ $strength != "1.0" ]]; then
			continue
		fi
		if ! "$WORKDIR/djgyrofix" fix --apply --ranges "${start}-${end}" \
			--smoothing-ms "$smoothing" --strength "$strength" --out "$go" "$input" >/dev/null 2>&1; then
			echo "FAIL $(basename "$input") $start-$end sm=$smoothing (port declined)"
			fail=$((fail + 1))
			continue
		fi
		if cmp -s "$py" "$go"; then
			pass=$((pass + 1))
		else
			echo "FAIL $(basename "$input") $start-$end sm=$smoothing — output differs"
			cmp "$py" "$go" | head -3
			fail=$((fail + 1))
		fi
	done
done

echo
echo "parity: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
