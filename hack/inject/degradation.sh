#!/usr/bin/env bash
# Progressive degradation: a growing set of bad sectors over time.
#
# This is the gradual-failure scenario — reallocated and pending counts that
# climb across a capture window — as distinct from the sudden faults the
# other scripts inject. A background worker adds STEP new bad sectors every
# INTERVAL seconds, up to MAX.
#
# On a real SATA disk each step marks sectors with hdparm --make-bad-sector
# and reads them (pending grows), then rewrites the previous step's sectors
# with --repair-sector, which the drive may satisfy by reallocating
# (reallocated grows). Whether it reallocates is the drive's decision, so the
# label records the SMART counts before and after instead of assuming.
#
# Elsewhere the effect is emulated with a growing dm-dust (or dm-error)
# bad-sector list, and
# the label records smartEffect=emulated.
#
# Parameters:
#   STEP      sectors added per step (default 4)
#   INTERVAL  seconds between steps (default 60)
#   MAX       stop growing after this many sectors (default 256)
. "$(dirname "$0")/lib.sh"

FAULT=degradation
HDPARM_OK="--yes-i-know-what-i-am-doing"

worker() {
	state_load "$FAULT"
	local base added=0 prev="" all="" step s cur
	base=$(($(sectors "$TARGET") / 3))
	while [ "$added" -lt "$MAX" ] && state_active "$FAULT"; do
		cur=""
		for ((step = 0; step < STEP && added < MAX; step++)); do
			s=$((base + added * 8))
			cur="$cur $s"
			added=$((added + 1))
		done
		if [ "$MODE" = real ]; then
			for s in $cur; do
				hdparm "$HDPARM_OK" --make-bad-sector "$s" "$TARGET" >/dev/null 2>&1 || true
				dd if="$TARGET" of=/dev/null bs=512 skip="$s" count=1 iflag=direct 2>/dev/null || true
			done
			for s in $prev; do
				hdparm "$HDPARM_OK" --repair-sector "$s" "$TARGET" >/dev/null 2>&1 || true
			done
		else
			all="$all $cur"
			# shellcheck disable=SC2086 # word splitting of the sector list is intended
			bad_sectors_set "$TOOL" $all
		fi
		prev="$cur"
		echo "$added" >"$STATE_DIR/$FAULT.count"
		echo "$cur" >>"$STATE_DIR/$FAULT.sectors"
		sleep "$INTERVAL"
	done
}

case "${1:-}" in
start)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt dd setsid
	resolve_target
	state_active "$FAULT" && die "$FAULT is already active"

	step="${STEP:-4}"
	interval="${INTERVAL:-60}"
	max="${MAX:-256}"
	before="$(smart_snapshot)"
	started="$(now)"

	if is_real_ata; then
		mode=real
		tool=hdparm
	else
		require_prepared
		mode=emulated
		if dust_available; then
			tool=dm-dust
			dm_swap "0 $(sectors "$TARGET") dust $TARGET 0 512"
			dmsetup message "$DM_NAME" 0 enable >/dev/null
		else
			# dm-error starts as a pass-through; the worker rebuilds the
			# table with the growing sector list at each step.
			tool=dm-error
		fi
	fi

	mkdir -p "$STATE_DIR"
	: >"$STATE_DIR/$FAULT.sectors"
	state_save "$FAULT" "STARTED=$started" "MODE=$mode" "TOOL=$tool" "STEP=$step" \
		"INTERVAL=$interval" "MAX=$max" "SMART_BEFORE=$before" "TARGET=$TARGET"
	setsid "$0" _worker </dev/null >/dev/null 2>&1 &
	echo $! >"$STATE_DIR/$FAULT.pid"

	emit disk.progressive_degradation "$tool" "$TARGET" "$started" "" \
		"step=$step" "intervalSecs=$interval" "max=$max" "smartEffect=$mode" "smartBefore=$before"
	;;
_worker)
	worker
	;;
stop)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt
	resolve_target
	state_load "$FAULT"
	if [ -f "$STATE_DIR/$FAULT.pid" ]; then
		kill "$(cat "$STATE_DIR/$FAULT.pid")" 2>/dev/null || true
	fi
	count="$(cat "$STATE_DIR/$FAULT.count" 2>/dev/null || echo 0)"
	if [ "$MODE" = real ]; then
		for s in $(cat "$STATE_DIR/$FAULT.sectors" 2>/dev/null); do
			hdparm "$HDPARM_OK" --repair-sector "$s" "$TARGET" >/dev/null 2>&1 || true
		done
	else
		dm_restore
	fi
	ended="$(now)"
	after="$(smart_snapshot)"
	state_clear "$FAULT"
	rm -f "$STATE_DIR/$FAULT.pid" "$STATE_DIR/$FAULT.count" "$STATE_DIR/$FAULT.sectors"
	emit disk.progressive_degradation "$TOOL" "$TARGET" "$STARTED" "$ended" \
		"step=$STEP" "intervalSecs=$INTERVAL" "max=$MAX" "sectorsAdded=$count" \
		"smartEffect=$MODE" "smartBefore=$SMART_BEFORE" "smartAfter=$after"
	;;
status)
	if state_active "$FAULT"; then
		echo "$FAULT: active, $(cat "$STATE_DIR/$FAULT.count" 2>/dev/null || echo 0) sectors"
	else
		echo "$FAULT: inactive"
		exit 1
	fi
	;;
*) usage_exit ;;
esac
