#!/usr/bin/env bash
# Write errors, via dm-flakey's error_writes feature.
#
# Writes fail with EIO while reads keep working — the signature of a drive
# that can no longer remap. By default writes fail continuously; set UP_SECS
# and DOWN_SECS for an intermittent fault, which is harder to diagnose and
# worth having in the corpus.
#
# SMART counters are not affected; the label records smartEffect=none.
#
# Parameters:
#   UP_SECS    seconds of normal operation per cycle (default 0)
#   DOWN_SECS  seconds of failing writes per cycle (default 86400)
. "$(dirname "$0")/lib.sh"

FAULT=write-errors

case "${1:-}" in
start)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt
	resolve_target
	require_prepared
	state_active "$FAULT" && die "$FAULT is already active"

	up="${UP_SECS:-0}"
	down="${DOWN_SECS:-86400}"
	before="$(smart_snapshot)"
	started="$(now)"
	dm_swap "0 $(sectors "$TARGET") flakey $TARGET 0 $up $down 1 error_writes"

	state_save "$FAULT" "STARTED=$started" "UP=$up" "DOWN=$down" "SMART_BEFORE=$before"
	emit disk.write_errors dm-flakey "$TARGET" "$started" "" \
		"upSecs=$up" "downSecs=$down" "mode=error_writes" "dmDevice=$(dm_path)" \
		"smartBefore=$before" "smartEffect=none"
	;;
stop)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt
	resolve_target
	state_load "$FAULT"
	dm_restore
	ended="$(now)"
	after="$(smart_snapshot)"
	state_clear "$FAULT"
	emit disk.write_errors dm-flakey "$TARGET" "$STARTED" "$ended" \
		"upSecs=$UP" "downSecs=$DOWN" "mode=error_writes" "dmDevice=$(dm_path)" \
		"smartBefore=$SMART_BEFORE" "smartAfter=$after" "smartEffect=none"
	;;
status)
	state_active "$FAULT" && echo "$FAULT: active ($(dm_table_type))" || { echo "$FAULT: inactive"; exit 1; }
	;;
*) usage_exit ;;
esac
