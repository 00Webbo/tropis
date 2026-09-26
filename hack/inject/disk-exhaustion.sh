#!/usr/bin/env bash
# Disk exhaustion: fill the target filesystem.
#
# Allocates a single file until KEEP_BYTES remain free, including the space
# ext4 reserves for root. Workloads writing to the filesystem see ENOSPC; if
# it backs kubelet ephemeral storage, the node reports DiskPressure and starts
# evicting pods. The disk hardware is fine throughout, which makes this the
# corpus's case of a Kubernetes-layer cause for a host-layer symptom.
#
# Parameters:
#   KEEP_BYTES  bytes to leave free (default 0: completely full)
. "$(dirname "$0")/lib.sh"

FAULT=disk-exhaustion

case "${1:-}" in
start)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt df fallocate stat dd
	resolve_target
	state_active "$FAULT" && die "$FAULT is already active"

	mnt="$(inventory_field mountpoint)"
	[ -n "$mnt" ] || die "the inventory gives no mountpoint for this disk"
	source="$(findmnt -nro SOURCE --target "$mnt")" || die "$mnt is not mounted"
	on_target "$source" ||
		die "$mnt is mounted from $source, which is not on the target $TARGET"

	keep="${KEEP_BYTES:-0}"
	fill="$mnt/.tropis-exhaustion"
	before="$(smart_snapshot)"
	started="$(now)"

	# Fill the filesystem's true free space, including the blocks ext4
	# reserves for root. df's "available" excludes that reserve, but
	# containers commonly run as root and would carry on writing into it, so
	# a fault that left it free would not be an exhaustion fault for them.
	free=$(($(stat -f -c '%f' "$mnt") * $(stat -f -c '%S' "$mnt")))
	size=$((free - keep))
	[ "$size" -gt 0 ] || die "only $free bytes free; nothing to fill"
	if [ "$keep" -gt 0 ]; then
		fallocate -l "$size" "$fill"
	else
		# Allocation metadata means fallocate cannot claim every last block,
		# so allocate most of it and top up with writes until the kernel
		# says ENOSPC.
		fallocate -l $((size > 8388608 ? size - 8388608 : 0)) "$fill" 2>/dev/null || true
		dd if=/dev/zero of="$fill" bs=64K oflag=append conv=notrunc status=none 2>/dev/null || true
	fi
	sync
	used="$(df --output=pcent "$mnt" | tail -n1 | tr -d ' %')"

	state_save "$FAULT" "STARTED=$started" "FILL=$fill" "MNT=$mnt" "KEEP=$keep" "SMART_BEFORE=$before"
	emit disk.exhaustion fallocate "$TARGET" "$started" "" \
		"mountpoint=$mnt" "keepBytes=$keep" "usedPercent=$used" \
		"smartBefore=$before" "smartEffect=none"
	;;
stop)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt df
	resolve_target
	state_load "$FAULT"
	rm -f "$FILL"
	sync
	ended="$(now)"
	after="$(smart_snapshot)"
	state_clear "$FAULT"
	emit disk.exhaustion fallocate "$TARGET" "$STARTED" "$ended" \
		"mountpoint=$MNT" "keepBytes=$KEEP" "smartBefore=$SMART_BEFORE" \
		"smartAfter=$after" "smartEffect=none"
	;;
status)
	state_active "$FAULT" && echo "$FAULT: active" || { echo "$FAULT: inactive"; exit 1; }
	;;
*) usage_exit ;;
esac
