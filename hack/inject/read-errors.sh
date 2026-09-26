#!/usr/bin/env bash
# Read errors on specific blocks, via dm-dust, or dm-error where the kernel
# lacks dm-dust (see lib.sh; the label records which).
#
# Reads of the listed blocks fail with EIO. Under dm-dust, writing a bad
# block clears it, which is dm-dust's model of a sector being remapped on
# write — the same behaviour a real drive shows for a pending sector.
#
# SMART counters are not affected: this emulates the I/O symptom of a bad
# sector, and the label records smartEffect=none so no fixture claims
# otherwise. For SMART-visible faults on real hardware, see
# pending-sectors.sh.
#
# Parameters:
#   BAD_BLOCKS  space-separated 512-byte block numbers (default: four blocks
#               in the middle of the device)
. "$(dirname "$0")/lib.sh"

FAULT=read-errors

case "${1:-}" in
start)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt
	resolve_target
	require_prepared
	state_active "$FAULT" && die "$FAULT is already active"

	blocks="${BAD_BLOCKS:-}"
	if [ -z "$blocks" ]; then
		mid=$(($(sectors "$TARGET") / 2))
		blocks="$mid $((mid + 1)) $((mid + 2)) $((mid + 3))"
	fi
	before="$(smart_snapshot)"
	started="$(now)"

	# shellcheck disable=SC2086 # word splitting of the block list is intended
	bad_sectors_start $blocks

	state_save "$FAULT" "STARTED=$started" "BLOCKS=$blocks" "TOOL=$BAD_TOOL" "SMART_BEFORE=$before"
	emit disk.read_errors "$BAD_TOOL" "$TARGET" "$started" "" \
		"badBlocks=$blocks" "blockSize=512" "dmDevice=$(dm_path)" \
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
	emit disk.read_errors "$TOOL" "$TARGET" "$STARTED" "$ended" \
		"badBlocks=$BLOCKS" "blockSize=512" "dmDevice=$(dm_path)" \
		"smartBefore=$SMART_BEFORE" "smartAfter=$after" "smartEffect=none"
	;;
status)
	state_active "$FAULT" && echo "$FAULT: active ($(dm_table_type))" || { echo "$FAULT: inactive"; exit 1; }
	;;
*) usage_exit ;;
esac
