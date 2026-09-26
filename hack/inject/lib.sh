#!/usr/bin/env bash
# Shared library for Tropis fault-injection scripts. Sourced, not executed.
#
# Every script follows the same contract:
#
#   <script> start    inject the fault; print a JSON record on stdout
#   <script> stop     remove the fault; print the completed JSON record
#   <script> status   report whether the fault is active
#
# The JSON record is shaped for label.json: {"faultType": ..., "injection":
# {"tool", "device", "params", "startedAt", "endedAt"}}.
#
# Targeting is deliberately narrow. The target disk comes only from the
# environment inventory, through `tropis inventory target`, which refuses any
# disk not marked destructible. On top of that, these scripts refuse a device
# that backs / or any system mount, and refuse to run at all unless
# TROPIS_INJECT_CONFIRM names the node and disk being targeted.
#
# Environment:
#   TROPIS_INVENTORY       inventory file (required)
#   TROPIS_NODE            node name in the inventory (required)
#   TROPIS_DISK            disk id in the inventory (required for disk faults)
#   TROPIS_INJECT_CONFIRM  must equal "$TROPIS_NODE/$TROPIS_DISK" (required)
#   TROPIS_BIN             tropis binary (default: tropis on PATH)
#   TROPIS_INJECT_STATE    state directory (default: /run/tropis-inject)

set -euo pipefail

TROPIS_BIN="${TROPIS_BIN:-tropis}"
STATE_DIR="${TROPIS_INJECT_STATE:-/run/tropis-inject}"
DM_NAME="${TROPIS_DM_NAME:-tropis-target}"

die() {
	echo "$(basename "$0"): $*" >&2
	exit 1
}

log() {
	echo "$(basename "$0"): $*" >&2
}

now() {
	date -u +%Y-%m-%dT%H:%M:%SZ
}

require_root() {
	[ "$(id -u)" -eq 0 ] || die "must run as root"
}

require_cmd() {
	local c
	for c in "$@"; do
		command -v "$c" >/dev/null 2>&1 || die "required command not found: $c"
	done
}

# confirm checks the operator named exactly what is about to be hit.
confirm() {
	local want="$1"
	[ "${TROPIS_INJECT_CONFIRM:-}" = "$want" ] ||
		die "refusing to run: set TROPIS_INJECT_CONFIRM=$want to confirm the target"
}

# inventory_field prints one field of the destructible target disk, or fails.
inventory_field() {
	[ -n "${TROPIS_INVENTORY:-}" ] || die "TROPIS_INVENTORY is not set"
	[ -n "${TROPIS_NODE:-}" ] || die "TROPIS_NODE is not set"
	[ -n "${TROPIS_DISK:-}" ] || die "TROPIS_DISK is not set"
	"$TROPIS_BIN" inventory target \
		--inventory "$TROPIS_INVENTORY" --node "$TROPIS_NODE" --disk "$TROPIS_DISK" \
		--field "$1" || die "inventory refused the target"
}

# system_mounts lists mount targets that must never sit on a target device.
system_mounts() {
	printf '%s\n' / /boot /boot/efi /usr /var /etc /home /var/lib \
		/var/lib/kubelet /var/lib/containerd /var/lib/docker /var/lib/etcd
}

# refuse_if_system_device fails if $1, or any of its partitions or holders,
# is mounted at a system path. Belt and braces over the inventory check: the
# inventory can be wrong about what a device path points at today.
refuse_if_system_device() {
	local dev real node mnt sys
	dev="$1"
	real="$(readlink -f "$dev")"
	[ -b "$real" ] || die "$dev is not a block device"

	# Every device in the tree rooted at the target: partitions, and any
	# device-mapper or other holders built on top of it. Written with explicit
	# ifs: under set -e, a trailing `[ ... ] && die` that tests false would
	# make the whole function return failure.
	while read -r node; do
		[ -n "$node" ] || continue
		while read -r mnt; do
			[ -n "$mnt" ] || continue
			while read -r sys; do
				if [ "$mnt" = "$sys" ]; then
					die "refusing: $node is mounted at system path $mnt"
				fi
			done < <(system_mounts)
		done < <(findmnt -nro TARGET -S "$node" 2>/dev/null || true)
	done < <(lsblk -nrpo NAME "$real" 2>/dev/null || echo "$real")
	return 0
}

# resolve_target sets TARGET to the verified destructible device.
resolve_target() {
	confirm "${TROPIS_NODE:-}/${TROPIS_DISK:-}"
	TARGET="$(inventory_field target)"
	[ -n "$TARGET" ] || die "inventory returned no target"
	refuse_if_system_device "$TARGET"
	TARGET="$(readlink -f "$TARGET")"
	export TARGET
}

# on_target reports whether device $1 is the target or sits on it — a
# partition, or a device-mapper device built over it. Names are compared
# canonically, since findmnt and lsblk spell the same dm device differently
# (/dev/dm-0 versus /dev/mapper/tropis-target).
on_target() {
	local want n
	want="$(readlink -f "$1")"
	while read -r n; do
		[ -n "$n" ] && [ "$(readlink -f "$n")" = "$want" ] && return 0
	done < <(lsblk -nrpo NAME "$TARGET" 2>/dev/null)
	return 1
}

# sectors prints a device's size in 512-byte sectors.
sectors() {
	blockdev --getsz "$1"
}

# dm_path is the pass-through device workloads mount.
dm_path() {
	echo "/dev/mapper/$DM_NAME"
}

require_prepared() {
	dmsetup info "$DM_NAME" >/dev/null 2>&1 ||
		die "$DM_NAME does not exist; run prepare.sh first and mount $(dm_path) for the workload"
}

# dm_swap replaces the live table of the pass-through device. The table may be
# several lines. I/O in flight is held across the swap, so a running workload
# sees the fault begin mid-stream exactly as it would on failing hardware.
dm_swap() {
	printf '%s\n' "$1" | dmsetup load "$DM_NAME"
	dmsetup suspend "$DM_NAME"
	dmsetup resume "$DM_NAME"
}

# dm_create makes the pass-through device and its node. Without udev — in a
# container, for instance — dmsetup does not create /dev/mapper nodes itself.
dm_create() {
	dmsetup create "$DM_NAME" --table "0 $(sectors "$TARGET") linear $TARGET 0"
	dmsetup mknodes "$DM_NAME" 2>/dev/null || true
}

# dm_restore puts the pass-through table back.
dm_restore() {
	dm_swap "0 $(sectors "$TARGET") linear $TARGET 0"
}

# --- bad sectors -------------------------------------------------------------
#
# Two ways to make specific sectors fail, used by every bad-sector fault:
#
#   dm-dust   reads of a bad sector fail; writing it clears it, as a real
#             drive clears a pending sector by remapping it on write.
#   dm-error  the bad sectors are mapped to device-mapper's built-in error
#             target, so reads and writes both fail and nothing clears them.
#
# dm-dust is the better model and is used when the kernel has it. Some do not
# (WSL2's, for one), so dm-error is the fallback, and the label records which
# tool produced the fault.

dust_available() {
	# TROPIS_NO_DUST=1 forces the dm-error path, so tests cover both.
	[ "${TROPIS_NO_DUST:-}" = 1 ] && return 1
	dmsetup targets 2>/dev/null | grep -q '^dust' && return 0
	modprobe dm_dust 2>/dev/null || true
	dmsetup targets 2>/dev/null | grep -q '^dust'
}

# error_table SECTOR... prints a table passing the target through except for
# the listed sectors, which map to the error target.
error_table() {
	local total prev=0 s
	total="$(sectors "$TARGET")"
	for s in $(printf '%s\n' "$@" | sort -n -u); do
		[ "$s" -lt "$total" ] || continue
		[ "$s" -gt "$prev" ] && echo "$prev $((s - prev)) linear $TARGET $prev"
		echo "$s 1 error"
		prev=$((s + 1))
	done
	[ "$prev" -lt "$total" ] && echo "$prev $((total - prev)) linear $TARGET $prev"
	return 0
}

# bad_sectors_start SECTOR... makes the sectors fail and sets BAD_TOOL.
bad_sectors_start() {
	if dust_available; then
		BAD_TOOL=dm-dust
		dm_swap "0 $(sectors "$TARGET") dust $TARGET 0 512"
		local s
		for s in "$@"; do
			dmsetup message "$DM_NAME" 0 addbadblock "$s" >/dev/null
		done
		dmsetup message "$DM_NAME" 0 enable >/dev/null
	else
		BAD_TOOL=dm-error
		dm_swap "$(error_table "$@")"
	fi
}

# bad_sectors_set TOOL SECTOR... brings the fault to exactly these sectors,
# for a fault whose set grows over time.
bad_sectors_set() {
	local tool="$1"
	shift
	if [ "$tool" = dm-dust ]; then
		local s
		for s in "$@"; do
			dmsetup message "$DM_NAME" 0 addbadblock "$s" >/dev/null 2>&1 || true
		done
	else
		dm_swap "$(error_table "$@")"
	fi
}

dm_table_type() {
	dmsetup table "$DM_NAME" 2>/dev/null | awk '{print $3; exit}'
}

# smart_snapshot prints the target's reallocated, pending and uncorrectable
# counts, read through the real SMART parser, or "unavailable" where the
# device has no SMART (a loop device, for instance).
#
# Taken before and after every fault, so a label records what actually
# happened to the SMART data rather than what the injection hoped for.
smart_snapshot() {
	local collector="${TROPIS_COLLECTOR:-tropis-collector}"
	if ! command -v smartctl >/dev/null 2>&1 || ! command -v "$collector" >/dev/null 2>&1; then
		echo unavailable
		return
	fi
	local tmp out
	tmp="$(mktemp -d)"
	smartctl -j -a "$TARGET" >"$tmp/target.json" 2>/dev/null || true
	out="$("$collector" collect --from-dir "$tmp" 2>/dev/null)" || out=""
	rm -rf "$tmp"
	local r p u
	r="$(printf '%s\n' "$out" | sed -n 's/.*"reallocatedSectors": *\([0-9]*\).*/\1/p' | head -n1)"
	p="$(printf '%s\n' "$out" | sed -n 's/.*"pendingSectors": *\([0-9]*\).*/\1/p' | head -n1)"
	u="$(printf '%s\n' "$out" | sed -n 's/.*"uncorrectableErrors": *\([0-9]*\).*/\1/p' | head -n1)"
	if [ -z "$r$p$u" ]; then
		echo unavailable
		return
	fi
	echo "reallocated=${r:-na} pending=${p:-na} uncorrectable=${u:-na}"
}

# is_real_ata reports whether the target is a physical ATA disk, on which
# SMART-affecting injection (hdparm --make-bad-sector) is possible.
is_real_ata() {
	[ "$(inventory_field transport)" = "sata" ] && command -v hdparm >/dev/null 2>&1
}

# --- state -----------------------------------------------------------------

state_file() {
	echo "$STATE_DIR/$1.env"
}

# state_save FAULT KEY=VALUE... records a started fault.
state_save() {
	local fault="$1"
	shift
	mkdir -p "$STATE_DIR"
	: >"$(state_file "$fault")"
	local kv
	for kv in "$@"; do
		printf '%q\n' "$kv" >>"$(state_file "$fault")"
	done
}

# state_load FAULT exports the saved variables; fails if none.
state_load() {
	local f
	f="$(state_file "$1")"
	[ -f "$f" ] || die "$1 is not active (no state in $STATE_DIR)"
	local line
	while IFS= read -r line; do
		eval "export $line"
	done <"$f"
}

state_clear() {
	rm -f "$(state_file "$1")"
}

state_active() {
	[ -f "$(state_file "$1")" ]
}

# --- output ----------------------------------------------------------------

json_escape() {
	local s="$1"
	s="${s//\\/\\\\}"
	s="${s//\"/\\\"}"
	s="${s//$'\n'/\\n}"
	s="${s//$'\t'/\\t}"
	printf '%s' "$s"
}

# emit FAULT_TYPE TOOL DEVICE STARTED ENDED KEY=VALUE... prints a label
# fragment. ENDED may be empty for a fault that has not ended.
emit() {
	local fault_type="$1" tool="$2" device="$3" started="$4" ended="$5"
	shift 5
	local params="" kv k v
	for kv in "$@"; do
		k="${kv%%=*}"
		v="${kv#*=}"
		[ -n "$params" ] && params="$params,"
		params="$params\"$(json_escape "$k")\":\"$(json_escape "$v")\""
	done
	printf '{"faultType":"%s","injection":{"tool":"%s","device":"%s","params":{%s},"startedAt":"%s"' \
		"$(json_escape "$fault_type")" "$(json_escape "$tool")" "$(json_escape "$device")" "$params" "$started"
	[ -n "$ended" ] && printf ',"endedAt":"%s"' "$ended"
	printf '}}\n'
}

usage_exit() {
	echo "usage: $(basename "$0") start|stop|status" >&2
	exit 64
}
