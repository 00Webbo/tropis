#!/usr/bin/env bash
# Pending sectors.
#
# On a real SATA disk this uses `hdparm --make-bad-sector`, which issues ATA
# WRITE UNCORRECTABLE EXT: the drive itself marks the sectors unreadable, and
# reading them makes it count them as pending. SMART genuinely changes, which
# is what the eval corpus needs. `stop` rewrites the sectors with
# `hdparm --repair-sector`, clearing them.
#
# On anything else — a loop device, NVMe, SAS — there is no portable way to
# change SMART counters, so the I/O effect is emulated with dm-dust (or
# dm-error; see lib.sh) and the label records smartEffect=emulated. A fixture
# captured that way must not be labelled as a SMART-visible fault; the before
# and after counts in the label show which it was.
#
# Parameters:
#   SECTORS  space-separated LBAs (default: four in the middle of the disk)
. "$(dirname "$0")/lib.sh"

FAULT=pending-sectors
HDPARM_OK="--yes-i-know-what-i-am-doing"

read_sectors() {
	local s
	for s in $1; do
		dd if="$TARGET" of=/dev/null bs=512 skip="$s" count=1 iflag=direct 2>/dev/null || true
	done
}

case "${1:-}" in
start)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt dd
	resolve_target
	state_active "$FAULT" && die "$FAULT is already active"

	sectors_list="${SECTORS:-}"
	if [ -z "$sectors_list" ]; then
		mid=$(($(sectors "$TARGET") / 2))
		sectors_list="$mid $((mid + 8)) $((mid + 16)) $((mid + 24))"
	fi
	before="$(smart_snapshot)"
	started="$(now)"

	if is_real_ata; then
		mode=real
		tool=hdparm
		for s in $sectors_list; do
			hdparm "$HDPARM_OK" --make-bad-sector "$s" "$TARGET" >/dev/null
		done
		# The drive counts a sector as pending when a read of it fails.
		read_sectors "$sectors_list"
	else
		require_prepared
		mode=emulated
		# shellcheck disable=SC2086 # word splitting of the sector list is intended
		bad_sectors_start $sectors_list
		tool="$BAD_TOOL"
	fi
	after_start="$(smart_snapshot)"

	state_save "$FAULT" "STARTED=$started" "SECTORS_LIST=$sectors_list" "MODE=$mode" \
		"TOOL=$tool" "SMART_BEFORE=$before"
	emit disk.pending_sectors "$tool" "$TARGET" "$started" "" \
		"sectors=$sectors_list" "smartEffect=$mode" \
		"smartBefore=$before" "smartAfterInjection=$after_start"
	;;
stop)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt dd
	resolve_target
	state_load "$FAULT"
	if [ "$MODE" = real ]; then
		for s in $SECTORS_LIST; do
			hdparm "$HDPARM_OK" --repair-sector "$s" "$TARGET" >/dev/null
		done
	else
		dm_restore
	fi
	ended="$(now)"
	after="$(smart_snapshot)"
	state_clear "$FAULT"
	emit disk.pending_sectors "$TOOL" "$TARGET" "$STARTED" "$ended" \
		"sectors=$SECTORS_LIST" "smartEffect=$MODE" \
		"smartBefore=$SMART_BEFORE" "smartAfter=$after"
	;;
status)
	state_active "$FAULT" && echo "$FAULT: active" || { echo "$FAULT: inactive"; exit 1; }
	;;
*) usage_exit ;;
esac
