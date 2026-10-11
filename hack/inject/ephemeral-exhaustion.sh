#!/usr/bin/env bash
# Ephemeral-storage exhaustion: a workload overruns its own local storage
# allowance and the kubelet evicts it.
#
# This script injects nothing. The fault is the workload (log-spam or
# emptydir-filler under eval/scenarios/workloads), applied right after
# `start`: it writes until it exceeds its container ephemeral-storage limit
# or its emptyDir sizeLimit, and the kubelet's eviction manager evicts it.
# That is a Kubernetes-layer cause with a storage symptom while the disk
# hardware is healthy, which is what the exhaustion scenarios need.
#
# Ephemeral storage lives on the kubelet's and the container runtime's
# filesystem, never on the test disk. What this script does is make that
# safe and true before the workload runs, and record the fault window:
#
#   - refuse if the kubelet root, the runtime root or the pod log directory
#     sits on the target disk (the rig's safety rules forbid it, and the
#     fault would then not be what the label says);
#   - refuse unless the runtime root shares the kubelet's filesystem. With a
#     dedicated image filesystem the kubelet does not count a container's
#     writable layer toward its limit, so log-spam would never be evicted;
#   - refuse unless that filesystem keeps HEADROOM_BYTES free, and at least
#     MIN_FREE_PCT of its size, after the workloads' hard write cap
#     (CAP_BYTES). The kubelet's default hard eviction threshold is
#     nodefs.available<10%, so the runaway can never push the node into
#     DiskPressure or fill the kubelet's root directory: only the per-pod
#     limit can fire.
#
# The target disk's SMART is recorded before and after to show it was
# healthy and untouched.
#
# Parameters:
#   CAP_BYTES       most the scenario's workloads can write (default 1 GiB;
#                   each workload caps itself at 512 MiB)
#   HEADROOM_BYTES  free space to keep beyond the cap (default 10 GiB)
#   MIN_FREE_PCT    share of the filesystem to keep free beyond the cap
#                   (default 20; the kubelet's hard threshold is 10)
#   TROPIS_KUBELET_DIR   kubelet root (default /var/lib/kubelet)
#   TROPIS_RUNTIME_DIR   container runtime root (default /var/lib/containerd)
#   TROPIS_POD_LOG_DIR   container log directory (default /var/log/pods)
. "$(dirname "$0")/lib.sh"

FAULT=ephemeral-exhaustion
KUBELET_DIR="${TROPIS_KUBELET_DIR:-/var/lib/kubelet}"
RUNTIME_DIR="${TROPIS_RUNTIME_DIR:-/var/lib/containerd}"
POD_LOG_DIR="${TROPIS_POD_LOG_DIR:-/var/log/pods}"

# refuse_on_target fails if directory $1 is stored on the target disk.
refuse_on_target() {
	local dir="$1" source
	[ -d "$dir" ] || die "$dir does not exist; is this a Kubernetes node? (set TROPIS_KUBELET_DIR etc.)"
	source="$(findmnt -nro SOURCE --target "$dir")" || die "cannot find the filesystem holding $dir"
	if on_target "$source"; then
		die "refusing: $dir is on the target disk $TARGET; kubelet and runtime storage must never be"
	fi
	return 0
}

# nodefs_avail prints the bytes available to unprivileged writers on the
# kubelet's filesystem, as the kubelet's nodefs.available signal counts them.
nodefs_avail() {
	df -B1 --output=avail "$KUBELET_DIR" | tail -n1 | tr -d ' '
}

nodefs_size() {
	df -B1 --output=size "$KUBELET_DIR" | tail -n1 | tr -d ' '
}

case "${1:-}" in
start)
	require_root
	require_cmd lsblk findmnt df stat
	resolve_target
	state_active "$FAULT" && die "$FAULT is already active"

	refuse_on_target "$KUBELET_DIR"
	refuse_on_target "$RUNTIME_DIR"
	refuse_on_target "$POD_LOG_DIR"
	if [ "$(stat -c %d "$RUNTIME_DIR")" != "$(stat -c %d "$KUBELET_DIR")" ]; then
		die "refusing: $RUNTIME_DIR is not on the same filesystem as $KUBELET_DIR; with a dedicated image filesystem the kubelet does not count container writable layers toward their limits"
	fi

	cap="${CAP_BYTES:-1073741824}"
	headroom="${HEADROOM_BYTES:-10737418240}"
	pct="${MIN_FREE_PCT:-20}"
	avail="$(nodefs_avail)"
	size="$(nodefs_size)"
	left=$((avail - cap))
	if [ "$left" -lt "$headroom" ] || [ $((left * 100)) -lt $((size * pct)) ]; then
		die "refusing: $KUBELET_DIR has $avail of $size bytes available; after a $cap-byte runaway it must keep $headroom bytes and $pct% free"
	fi

	before="$(smart_snapshot)"
	started="$(now)"
	state_save "$FAULT" "STARTED=$started" "CAP=$cap" "AVAIL_BEFORE=$avail" "SIZE=$size" "SMART_BEFORE=$before"
	log "checks passed; apply the scenario's workload now: it is the fault"
	emit storage.ephemeral_exhaustion workload "" "$started" "" \
		"mechanism=ephemeral-storage-limit" "kubeletDir=$KUBELET_DIR" \
		"nodefsBytes=$size" "nodefsAvailBefore=$avail" "capBytes=$cap" \
		"smartBefore=$before" "smartEffect=none"
	;;
stop)
	require_root
	require_cmd lsblk findmnt df
	resolve_target
	state_load "$FAULT"
	ended="$(now)"
	avail="$(nodefs_avail)"
	after="$(smart_snapshot)"
	state_clear "$FAULT"
	emit storage.ephemeral_exhaustion workload "" "$STARTED" "$ended" \
		"mechanism=ephemeral-storage-limit" "kubeletDir=$KUBELET_DIR" \
		"nodefsBytes=$SIZE" "nodefsAvailBefore=$AVAIL_BEFORE" "nodefsAvailAfter=$avail" \
		"capBytes=$CAP" "smartBefore=$SMART_BEFORE" "smartAfter=$after" "smartEffect=none"
	;;
status)
	state_active "$FAULT" && echo "$FAULT: active" || { echo "$FAULT: inactive"; exit 1; }
	;;
*) usage_exit ;;
esac
