#!/usr/bin/env bash
# End-to-end test of every injection script against a loop device.
#
#   hack/inject/test.sh <tropis binary> <tropis-collector binary>
#
# Must run as root with device-mapper available, inside a disposable
# privileged container or VM — never on a machine you care about. CI runs it
# in a privileged container. It refuses to run otherwise.
#
# For each fault: start, check the JSON record, check the fault's actual
# effect on I/O, stop, check the effect is gone. Then check the refusals: a
# disk not marked destructible and a mismatched confirmation must both leave
# the device untouched.
set -uo pipefail

[ "${TROPIS_INJECT_TEST:-}" = 1 ] || {
	echo "refusing: set TROPIS_INJECT_TEST=1 and run only in a disposable privileged container" >&2
	exit 2
}
[ "$(id -u)" -eq 0 ] || { echo "must run as root" >&2; exit 2; }

HERE="$(cd "$(dirname "$0")" && pwd)"
export TROPIS_BIN="$1" TROPIS_COLLECTOR="$2"
WORK="$(mktemp -d)"
export TROPIS_INJECT_STATE="$WORK/state"
MNT="$WORK/mnt"
mkdir -p "$MNT"

failures=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; failures=$((failures + 1)); }

cleanup() {
	set +e
	for f in degradation containerd-kill; do [ -f "$TROPIS_INJECT_STATE/$f.pid" ] && kill "$(cat "$TROPIS_INJECT_STATE/$f.pid")" 2>/dev/null; done
	pkill -KILL -x tropis-fake-ctd 2>/dev/null
	umount "$MNT" 2>/dev/null
	dmsetup remove tropis-target 2>/dev/null
	[ -n "${LOOP:-}" ] && losetup -d "$LOOP" 2>/dev/null
	[ -n "${LOOP2:-}" ] && losetup -d "$LOOP2" 2>/dev/null
	rm -rf "$WORK"
}
trap cleanup EXIT

truncate -s 256M "$WORK/disk.img"
truncate -s 64M "$WORK/protected.img"
LOOP="$(losetup -f --show "$WORK/disk.img")" || { echo "losetup failed" >&2; exit 2; }
LOOP2="$(losetup -f --show "$WORK/protected.img")" || { echo "losetup failed" >&2; exit 2; }
DM=/dev/mapper/tropis-target

cat >"$WORK/inventory.yaml" <<EOF
apiVersion: tropis.io/v1alpha1
kind: Inventory
cluster: {name: ci, kubernetesVersion: v1.31.0}
nodes:
  - name: ci-node
    role: worker
    disks:
      - {id: scratch, device: $LOOP, transport: loop, destructible: true, mountpoint: $MNT}
      - {id: protected, device: $LOOP2, transport: loop}
EOF
export TROPIS_INVENTORY="$WORK/inventory.yaml" TROPIS_NODE=ci-node TROPIS_DISK=scratch
export TROPIS_INJECT_CONFIRM=ci-node/scratch

# check_record NAME JSON WANT_TYPE ENDED(yes|no)
check_record() {
	local name="$1" json="$2" type="$3" ended="$4"
	if ! printf '%s' "$json" | jq -e --arg t "$type" \
		'.faultType == $t and (.injection.tool | length > 0) and (.injection.startedAt | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T"))' >/dev/null 2>&1; then
		fail "$name: record malformed: $json"
		return
	fi
	if [ "$ended" = yes ]; then
		printf '%s' "$json" | jq -e '.injection.endedAt | test("^[0-9]{4}-")' >/dev/null 2>&1 ||
			{ fail "$name: stop record has no endedAt: $json"; return; }
	fi
	pass "$name: record ($type, ended=$ended)"
}

readable() { dd if="$DM" of=/dev/null bs=512 skip="$1" count=1 iflag=direct 2>/dev/null; }
writable() { dd if=/dev/zero of="$DM" bs=512 seek="$1" count=1 oflag=direct 2>/dev/null; }

# --- setup -------------------------------------------------------------------

echo "bad-sector tool: $( [ "${TROPIS_NO_DUST:-}" = 1 ] && echo "dm-error (forced)" || echo "dm-dust if available, else dm-error")"
"$HERE/prepare.sh" start >/dev/null && [ -b "$DM" ] && pass "prepare: $DM created" || { fail "prepare"; exit 1; }

# --- read errors -------------------------------------------------------------

rec="$(BAD_BLOCKS="200000 200001" "$HERE/read-errors.sh" start)" && check_record read-errors "$rec" disk.read_errors no || fail "read-errors start"
echo "bad-sector mechanism in use: $(printf '%s' "$rec" | jq -r .injection.tool)"
readable 200000 && fail "read-errors: bad block was readable" || pass "read-errors: bad block returns an error"
readable 100000 && pass "read-errors: other blocks readable" || fail "read-errors: healthy block unreadable"
rec="$("$HERE/read-errors.sh" stop)" && check_record read-errors "$rec" disk.read_errors yes || fail "read-errors stop"
readable 200000 && pass "read-errors: cleared after stop" || fail "read-errors: still failing after stop"

# --- write errors ------------------------------------------------------------

rec="$("$HERE/write-errors.sh" start)" && check_record write-errors "$rec" disk.write_errors no || fail "write-errors start"
writable 300000 && fail "write-errors: write succeeded" || pass "write-errors: writes fail"
readable 300000 && pass "write-errors: reads still work" || fail "write-errors: reads failing too"
rec="$("$HERE/write-errors.sh" stop)" && check_record write-errors "$rec" disk.write_errors yes || fail "write-errors stop"
writable 300000 && pass "write-errors: cleared after stop" || fail "write-errors: still failing after stop"

# --- I/O timeouts ------------------------------------------------------------

rec="$(DELAY_MS=4000 "$HERE/io-timeouts.sh" start)" && check_record io-timeouts "$rec" disk.io_timeout no || fail "io-timeouts start"
timeout 1 dd if="$DM" of=/dev/null bs=512 skip=12345 count=1 iflag=direct 2>/dev/null
[ $? -eq 124 ] && pass "io-timeouts: read exceeded a 1s timeout" || fail "io-timeouts: read was not delayed"
rec="$("$HERE/io-timeouts.sh" stop)" && check_record io-timeouts "$rec" disk.io_timeout yes || fail "io-timeouts stop"
timeout 1 dd if="$DM" of=/dev/null bs=512 skip=12346 count=1 iflag=direct 2>/dev/null && pass "io-timeouts: cleared after stop" || fail "io-timeouts: still delayed after stop"

# --- pending sectors (emulated on a loop device) -----------------------------

rec="$(SECTORS=400000 "$HERE/pending-sectors.sh" start)" && check_record pending-sectors "$rec" disk.pending_sectors no || fail "pending-sectors start"
printf '%s' "$rec" | jq -e '.injection.params.smartEffect == "emulated"' >/dev/null &&
	pass "pending-sectors: label records smartEffect=emulated on a loop device" ||
	fail "pending-sectors: a loop device must be labelled emulated: $rec"
readable 400000 && fail "pending-sectors: sector readable" || pass "pending-sectors: sector unreadable"
rec="$("$HERE/pending-sectors.sh" stop)" && check_record pending-sectors "$rec" disk.pending_sectors yes || fail "pending-sectors stop"

# --- progressive degradation -------------------------------------------------

rec="$(STEP=2 INTERVAL=1 MAX=6 "$HERE/degradation.sh" start)" && check_record degradation "$rec" disk.progressive_degradation no || fail "degradation start"
sleep 4
status="$("$HERE/degradation.sh" status)"
n="$(echo "$status" | sed -n 's/.*active, \([0-9]*\) sectors.*/\1/p')"
[ "${n:-0}" -ge 4 ] && pass "degradation: bad sectors grew to $n" || fail "degradation: did not grow ($status)"
base=$(($(blockdev --getsz "$LOOP") / 3))
readable "$base" && fail "degradation: first bad sector readable" || pass "degradation: grown sectors unreadable"
rec="$("$HERE/degradation.sh" stop)" && check_record degradation "$rec" disk.progressive_degradation yes || fail "degradation stop"
readable "$base" && pass "degradation: cleared after stop" || fail "degradation: still failing after stop"

# --- disk exhaustion ---------------------------------------------------------

mkfs.ext4 -q -F "$DM" && mount "$DM" "$MNT" || { fail "mkfs/mount"; exit 1; }
rec="$("$HERE/disk-exhaustion.sh" start)" && check_record disk-exhaustion "$rec" disk.exhaustion no || fail "disk-exhaustion start"
dd if=/dev/zero of="$MNT/probe" bs=1M count=8 status=none 2>/dev/null && fail "disk-exhaustion: write succeeded" || pass "disk-exhaustion: writes hit ENOSPC"
rm -f "$MNT/probe"
rec="$("$HERE/disk-exhaustion.sh" stop)" && check_record disk-exhaustion "$rec" disk.exhaustion yes || fail "disk-exhaustion stop"
dd if=/dev/zero of="$MNT/probe" bs=1M count=8 status=none && pass "disk-exhaustion: space back after stop" || fail "disk-exhaustion: still full after stop"
rm -f "$MNT/probe"

# --- filesystem corruption ---------------------------------------------------

rec="$("$HERE/fs-corruption.sh" start)" && check_record fs-corruption "$rec" disk.fs_corruption no || fail "fs-corruption start"
rec="$("$HERE/fs-corruption.sh" stop 2>/dev/null)" && check_record fs-corruption "$rec" disk.fs_corruption yes || fail "fs-corruption stop"
umount "$MNT"
e2fsck -fn "$DM" >/dev/null 2>&1
[ $? -ge 4 ] && pass "fs-corruption: e2fsck finds the damage" || fail "fs-corruption: filesystem is clean"

# --- containerd kill ---------------------------------------------------------

cp "$(command -v sleep)" "$WORK/tropis-fake-ctd"
"$WORK/tropis-fake-ctd" 600 &
sleep 0.5
rec="$(TROPIS_INJECT_CONFIRM=ci-node/containerd TROPIS_CONTAINERD_UNIT=tropis-no-such-unit \
	TROPIS_CONTAINERD_PROCESS=tropis-fake-ctd "$HERE/containerd-kill.sh" start)" &&
	check_record containerd-kill "$rec" runtime.containerd_kill no || fail "containerd-kill start"
sleep 0.5
pgrep -x tropis-fake-ctd >/dev/null && fail "containerd-kill: process survived" || pass "containerd-kill: process killed"
rec="$("$HERE/containerd-kill.sh" stop)" && check_record containerd-kill "$rec" runtime.containerd_kill yes || fail "containerd-kill stop"

# --- refusals ----------------------------------------------------------------

table_before="$(dmsetup table tropis-target)"
if TROPIS_DISK=protected TROPIS_INJECT_CONFIRM=ci-node/protected "$HERE/read-errors.sh" start >/dev/null 2>&1; then
	fail "refusal: a disk not marked destructible was injected"
	"$HERE/read-errors.sh" stop >/dev/null 2>&1
else
	pass "refusal: disk not marked destructible"
fi
if TROPIS_INJECT_CONFIRM=ci-node/protected "$HERE/read-errors.sh" start >/dev/null 2>&1; then
	fail "refusal: mismatched confirmation was accepted"
	"$HERE/read-errors.sh" stop >/dev/null 2>&1
else
	pass "refusal: mismatched confirmation"
fi
if TROPIS_INJECT_CONFIRM= "$HERE/write-errors.sh" start >/dev/null 2>&1; then
	fail "refusal: missing confirmation was accepted"
	"$HERE/write-errors.sh" stop >/dev/null 2>&1
else
	pass "refusal: missing confirmation"
fi
[ "$(dmsetup table tropis-target)" = "$table_before" ] && pass "refusal: device untouched" || fail "refusal: device table changed"

"$HERE/prepare.sh" stop >/dev/null 2>&1 && pass "prepare: removed" || fail "prepare: remove"

echo
if [ "$failures" -eq 0 ]; then
	echo "all injection tests passed"
else
	echo "$failures injection test(s) failed"
fi
exit "$failures"
