#!/bin/sh
# Verify tropis-smart-check.sh honours the NPD plugin protocol.
#
#   hack/npd-plugin/test.sh <tropis-collector binary> <smart testdata dir>
#
# Runs on any POSIX shell; CI runs it on Linux against a freshly built binary.
set -u

BIN=$1
DATA=$2
HERE=$(cd "$(dirname "$0")" && pwd)
PLUGIN="$HERE/tropis-smart-check.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail=0

# check <name> <want exit> <want output prefix>
check() {
	name=$1 want=$2 prefix=$3
	shift 3
	out=$(env "$@" sh "$PLUGIN")
	code=$?
	len=$(printf '%s' "$out" | wc -c)
	lines=$(printf '%s\n' "$out" | wc -l)
	status=ok
	[ "$code" -eq "$want" ] || status="exit $code, want $want"
	case "$out" in "$prefix"*) ;; *) status="output '$out' lacks prefix '$prefix'" ;; esac
	[ "$len" -le 80 ] || status="output is $len bytes"
	[ "$lines" -eq 1 ] || status="output is $lines lines"
	if [ "$status" = ok ]; then
		echo "PASS $name"
	else
		echo "FAIL $name: $status"
		fail=1
	fi
}

mkcase() {
	dir="$WORK/$1"
	shift
	mkdir -p "$dir"
	while [ $# -gt 0 ]; do
		cp "$DATA/$2" "$dir/$1.json"
		shift 2
	done
	echo "$dir"
}

healthy=$(mkcase healthy sda sata-healthy.json nvme0n1 nvme-healthy.json)
failing=$(mkcase failing sda sata-healthy.json sdb sata-failing.json)
unreadable=$(mkcase unreadable sda error-permission-denied.json)

common="TROPIS_COLLECTOR=$BIN TROPIS_STATE_DIR=$WORK/state"

# shellcheck disable=SC2086
check "healthy disks exit 0" 0 "SMART OK" $common TROPIS_ARGS="--from-dir $healthy"
# shellcheck disable=SC2086
check "failing disk exits 1" 1 "sdb: overall health FAILED" $common TROPIS_ARGS="--from-dir $failing"
# shellcheck disable=SC2086
check "unreadable disk exits 2" 2 "SMART unreadable" $common TROPIS_ARGS="--from-dir $unreadable"
check "missing binary exits 2" 2 "tropis-collector not found" TROPIS_COLLECTOR="$WORK/nope"

# A binary exiting outside 0-2 must still yield a protocol-conformant result.
printf '#!/bin/sh\necho "panic: something broke\nwith a stack trace"\nexit 7\n' >"$WORK/bad"
chmod +x "$WORK/bad"
check "misbehaving binary exits 2" 2 "tropis-collector exited 7" TROPIS_COLLECTOR="$WORK/bad"

# Growth across sweeps. Every call loads and saves the same state, so if the
# monitor config ran the wrapper from more than one rule per interval, the
# later calls would compare the new reading with itself and miss the growth.
# Run one call per rule in the config: each must still see it.
grown=$(mkcase grown)
sed '/"Reallocated_Sector_Ct"/,/"string": "0"/{s/"value": 0,/"value": 8,/;s/"string": "0"/"string": "8"/;}' \
	"$DATA/sata-healthy.json" >"$grown/sda.json"
baseline=$(mkcase baseline sda sata-healthy.json)
calls=$(grep -c 'tropis-smart-check.sh' "$HERE/smart-plugin-monitor.json")
growth="TROPIS_COLLECTOR=$BIN TROPIS_STATE_DIR=$WORK/growth-state"
# shellcheck disable=SC2086
check "baseline reading exits 0" 0 "SMART OK" $growth TROPIS_ARGS="--from-dir $baseline"
i=1
while [ "$i" -le "$calls" ]; do
	# shellcheck disable=SC2086
	check "reallocated growth, call $i of $calls per interval" 1 "sda:" $growth TROPIS_ARGS="--from-dir $grown"
	i=$((i + 1))
done

exit $fail
