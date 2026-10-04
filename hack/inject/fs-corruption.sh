#!/usr/bin/env bash
# Filesystem corruption: overwrite a file's on-disk inode under a mounted
# ext4 filesystem.
#
# The inode block is rewritten with random bytes directly on the device and
# the kernel's caches are dropped, so the next access reads the damaged
# inode. With metadata checksums (the ext4 default) the kernel reports an
# EXT4-fs error and, under errors=remount-ro, remounts the filesystem
# read-only — which is what a workload then sees.
#
# This cannot be undone by the script. `stop` records the end of the window
# and prints the repair steps; repair is a deliberate, manual e2fsck.
#
# Requires the inventory disk to have a mountpoint whose filesystem lives on
# the target device, which is what prepare.sh sets up.
#
# Parameters:
#   VICTIM  file under the mountpoint whose inode to damage (default: a file
#           the script creates for the purpose)
. "$(dirname "$0")/lib.sh"

FAULT=fs-corruption

case "${1:-}" in
start)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt debugfs dumpe2fs dd stat
	resolve_target
	state_active "$FAULT" && die "$FAULT is already active"

	mnt="$(inventory_field mountpoint)"
	[ -n "$mnt" ] || die "the inventory gives no mountpoint for this disk"
	source="$(findmnt -nro SOURCE --target "$mnt")" || die "$mnt is not mounted"
	# The filesystem must be on the target device, never anywhere else.
	on_target "$source" ||
		die "$mnt is mounted from $source, which is not on the target $TARGET"
	[ "$(findmnt -nro FSTYPE --target "$mnt")" = ext4 ] || die "$mnt is not ext4"

	victim="${VICTIM:-$mnt/.tropis-corruption-victim}"
	if [ -z "${VICTIM:-}" ]; then
		head -c 65536 /dev/urandom >"$victim"
		sync
	fi
	[ -f "$victim" ] || die "victim $victim does not exist"

	inode="$(stat -c %i "$victim")"
	bs="$(dumpe2fs -h "$source" 2>/dev/null | awk -F: '/^Block size/ {gsub(/ /, "", $2); print $2}')"
	loc="$(debugfs -R "imap <$inode>" "$source" 2>/dev/null | sed -n 's/.*located at block \([0-9]*\), offset \(0x[0-9a-f]*\).*/\1 \2/p')"
	[ -n "$bs" ] && [ -n "$loc" ] || die "could not locate inode $inode on $source"
	block="${loc% *}"
	offset=$((${loc#* }))

	before="$(smart_snapshot)"
	started="$(now)"

	# Read the whole inode-table block, damage the inode inside it, write it
	# back aligned with O_DIRECT so the write bypasses and invalidates the
	# page cache.
	tmp="$(mktemp)"
	dd if="$source" of="$tmp" bs="$bs" skip="$block" count=1 iflag=direct status=none
	dd if=/dev/urandom of="$tmp" bs=1 seek="$offset" count=128 conv=notrunc status=none
	dd if="$tmp" of="$source" bs="$bs" seek="$block" count=1 oflag=direct conv=notrunc status=none
	rm -f "$tmp"
	sync
	echo 3 >/proc/sys/vm/drop_caches

	state_save "$FAULT" "STARTED=$started" "VICTIM=$victim" "INODE=$inode" "BLOCK=$block" \
		"MNT=$mnt" "SOURCE=$source" "SMART_BEFORE=$before"
	emit disk.fs_corruption dd "$TARGET" "$started" "" \
		"victim=$victim" "inode=$inode" "inodeBlock=$block" "mountpoint=$mnt" \
		"filesystem=ext4" "smartBefore=$before" "smartEffect=none"
	;;
stop)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt
	resolve_target
	state_load "$FAULT"
	ended="$(now)"
	after="$(smart_snapshot)"
	state_clear "$FAULT"
	log "the damage persists; to repair: umount $MNT && e2fsck -fy $SOURCE"
	emit disk.fs_corruption dd "$TARGET" "$STARTED" "$ended" \
		"victim=$VICTIM" "inode=$INODE" "inodeBlock=$BLOCK" "mountpoint=$MNT" \
		"filesystem=ext4" "smartBefore=$SMART_BEFORE" "smartAfter=$after" "smartEffect=none"
	;;
status)
	state_active "$FAULT" && echo "$FAULT: active" || { echo "$FAULT: inactive"; exit 1; }
	;;
*) usage_exit ;;
esac
