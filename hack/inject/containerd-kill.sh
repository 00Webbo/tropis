#!/usr/bin/env bash
# Kill the container runtime.
#
# SIGKILLs containerd, KILLS times, INTERVAL seconds apart. One kill is a
# brief runtime outage; repeated kills trip node-problem-detector's
# FrequentContainerdRestart and leave pods failing to start or stuck
# terminating. No disk is involved, which makes this a useful negative
# control beside a disk that happens to carry old defects.
#
# Confirmation is TROPIS_INJECT_CONFIRM="$TROPIS_NODE/containerd".
#
# Parameters:
#   KILLS     number of kills (default 1)
#   INTERVAL  seconds between kills (default 30)
#   TROPIS_CONTAINERD_UNIT     systemd unit (default containerd)
#   TROPIS_CONTAINERD_PROCESS  process name when systemd is absent
#                              (default containerd)
. "$(dirname "$0")/lib.sh"

FAULT=containerd-kill
UNIT="${TROPIS_CONTAINERD_UNIT:-containerd}"
PROC="${TROPIS_CONTAINERD_PROCESS:-containerd}"

kill_once() {
	if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "$UNIT" 2>/dev/null; then
		systemctl kill --signal=SIGKILL "$UNIT"
	else
		pkill -KILL -x "$PROC"
	fi
}

worker() {
	state_load "$FAULT"
	local i=1
	while [ "$i" -lt "$KILLS" ] && state_active "$FAULT"; do
		sleep "$INTERVAL"
		kill_once || true
		i=$((i + 1))
		echo "$i" >"$STATE_DIR/$FAULT.count"
	done
}

case "${1:-}" in
start)
	require_root
	require_cmd pkill setsid
	[ -n "${TROPIS_INVENTORY:-}" ] && [ -n "${TROPIS_NODE:-}" ] || die "TROPIS_INVENTORY and TROPIS_NODE are required"
	confirm "$TROPIS_NODE/containerd"
	"$TROPIS_BIN" inventory node --inventory "$TROPIS_INVENTORY" --node "$TROPIS_NODE" >/dev/null ||
		die "node $TROPIS_NODE is not in the inventory"
	state_active "$FAULT" && die "$FAULT is already active"

	kills="${KILLS:-1}"
	interval="${INTERVAL:-30}"
	started="$(now)"
	kill_once || die "no $UNIT unit or $PROC process to kill"
	mkdir -p "$STATE_DIR"
	echo 1 >"$STATE_DIR/$FAULT.count"

	state_save "$FAULT" "STARTED=$started" "KILLS=$kills" "INTERVAL=$interval" "NODE=$TROPIS_NODE"
	if [ "$kills" -gt 1 ]; then
		setsid "$0" _worker </dev/null >/dev/null 2>&1 &
		echo $! >"$STATE_DIR/$FAULT.pid"
	fi
	emit runtime.containerd_kill signal "" "$started" "" \
		"node=$TROPIS_NODE" "signal=SIGKILL" "kills=$kills" "intervalSecs=$interval"
	;;
_worker)
	worker
	;;
stop)
	require_root
	state_load "$FAULT"
	[ -f "$STATE_DIR/$FAULT.pid" ] && kill "$(cat "$STATE_DIR/$FAULT.pid")" 2>/dev/null || true
	done_kills="$(cat "$STATE_DIR/$FAULT.count" 2>/dev/null || echo 1)"
	ended="$(now)"
	state_clear "$FAULT"
	rm -f "$STATE_DIR/$FAULT.pid" "$STATE_DIR/$FAULT.count"
	emit runtime.containerd_kill signal "" "$STARTED" "$ended" \
		"node=$NODE" "signal=SIGKILL" "kills=$KILLS" "killsDelivered=$done_kills" "intervalSecs=$INTERVAL"
	;;
status)
	state_active "$FAULT" && echo "$FAULT: active" || { echo "$FAULT: inactive"; exit 1; }
	;;
*) usage_exit ;;
esac
