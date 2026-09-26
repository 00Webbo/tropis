#!/bin/sh
# node-problem-detector custom plugin: SMART disk health.
#
# Protocol (fixed by NPD's CustomPluginMonitor):
#   exit 0  OK       no problem found
#   exit 1  NonOK    problem found; stdout describes it
#   exit 2  Unknown  could not determine
# and one short line on stdout, which NPD truncates at max_output_length.
#
# The rules live in tropis-collector, not here, so Tropis's own pre-filter and
# this plugin are one artifact and cannot drift apart. This wrapper only
# locates the binary and guarantees the protocol holds even when the binary is
# missing or misbehaves: NPD must never see an exit code outside 0-2.
#
# Configuration, all optional:
#   TROPIS_COLLECTOR  path to the tropis-collector binary
#   TROPIS_STATE_DIR  where previous readings are kept, for growth rules
#   TROPIS_DEVICES    space-separated device list; default is smartctl --scan
#   TROPIS_ARGS       extra flags for `tropis-collector prefilter`

OK=0
UNKNOWN=2
MAX_OUTPUT=80

BIN="${TROPIS_COLLECTOR:-/usr/local/bin/tropis-collector}"
STATE_DIR="${TROPIS_STATE_DIR:-/var/lib/tropis}"

say() {
	printf '%s\n' "$1" | head -n 1 | cut -c "1-${MAX_OUTPUT}"
}

if [ ! -x "$BIN" ]; then
	say "tropis-collector not found at $BIN"
	exit "$UNKNOWN"
fi

# Word splitting of TROPIS_ARGS and TROPIS_DEVICES is intended.
# shellcheck disable=SC2086
out=$("$BIN" prefilter --npd --state-dir "$STATE_DIR" $TROPIS_ARGS $TROPIS_DEVICES 2>/dev/null)
code=$?

case "$code" in
0 | 1 | 2) ;;
*)
	out="tropis-collector exited $code"
	code=$UNKNOWN
	;;
esac

if [ -z "$out" ]; then
	if [ "$code" -eq "$OK" ]; then
		out="SMART OK"
	else
		out="no output"
	fi
fi

say "$out"
exit "$code"
