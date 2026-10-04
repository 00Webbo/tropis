#!/usr/bin/env bash
# I/O timeouts, via dm-delay.
#
# Every read and write is held for DELAY_MS before completing — the
# behaviour of a drive retrying internally on a marginal sector, which is
# often how a failing disk first shows itself to a workload: not errors, but
# hangs, timeouts, and probes failing.
#
# SMART counters are not affected; the label records smartEffect=none.
#
# Parameters:
#   DELAY_MS  delay applied to every I/O (default 30000)
. "$(dirname "$0")/lib.sh"

FAULT=io-timeouts

case "${1:-}" in
start)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt
	resolve_target
	require_prepared
	state_active "$FAULT" && die "$FAULT is already active"

	delay="${DELAY_MS:-30000}"
	before="$(smart_snapshot)"
	started="$(now)"
	dm_swap "0 $(sectors "$TARGET") delay $TARGET 0 $delay"

	state_save "$FAULT" "STARTED=$started" "DELAY=$delay" "SMART_BEFORE=$before"
	emit disk.io_timeout dm-delay "$TARGET" "$started" "" \
		"delayMs=$delay" "dmDevice=$(dm_path)" "smartBefore=$before" "smartEffect=none"
	;;
stop)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt
	resolve_target
	state_load "$FAULT"
	# Held I/O drains at the old delay before the swap completes.
	dm_restore
	ended="$(now)"
	after="$(smart_snapshot)"
	state_clear "$FAULT"
	emit disk.io_timeout dm-delay "$TARGET" "$STARTED" "$ended" \
		"delayMs=$DELAY" "dmDevice=$(dm_path)" "smartBefore=$SMART_BEFORE" \
		"smartAfter=$after" "smartEffect=none"
	;;
status)
	state_active "$FAULT" && echo "$FAULT: active ($(dm_table_type))" || { echo "$FAULT: inactive"; exit 1; }
	;;
*) usage_exit ;;
esac
