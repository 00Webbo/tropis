#!/usr/bin/env bash
# Put a pass-through device-mapper device over the target disk.
#
#   prepare.sh start   create /dev/mapper/tropis-target (linear, no fault)
#   prepare.sh stop    remove it (fails if still in use)
#   prepare.sh status
#
# Run once when setting up a scenario. Workloads under test then use
# /dev/mapper/tropis-target — typically make a filesystem on it and mount it at
# the inventory's mountpoint. The dm-based faults swap this device's table
# live, so a fault can begin under a running workload exactly as it would on
# failing hardware, and stopping a fault swaps the pass-through back.
. "$(dirname "$0")/lib.sh"

case "${1:-}" in
start)
	require_root
	require_cmd dmsetup blockdev lsblk findmnt
	resolve_target
	if dmsetup info "$DM_NAME" >/dev/null 2>&1; then
		log "$DM_NAME already exists"
	else
		dm_create
		log "created $(dm_path) over $TARGET"
	fi
	dm_path
	;;
stop)
	require_root
	require_cmd dmsetup
	dmsetup remove "$DM_NAME"
	log "removed $DM_NAME"
	;;
status)
	if dmsetup info "$DM_NAME" >/dev/null 2>&1; then
		echo "$DM_NAME: $(dm_table_type)"
	else
		echo "$DM_NAME: absent"
		exit 1
	fi
	;;
*) usage_exit ;;
esac
