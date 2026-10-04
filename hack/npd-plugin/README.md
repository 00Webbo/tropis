# SMART plugin for node-problem-detector

A [custom plugin](https://github.com/kubernetes/node-problem-detector/blob/master/docs/custom_plugin_monitor.md)
that raises a `DiskSMARTProblem` node condition from SMART data.

It is the same artifact as Tropis's own deterministic pre-filter: the wrapper
calls `tropis-collector prefilter --npd`, so the rules exist once and cannot
drift apart. It is useful on its own, without the rest of Tropis.

| File | Purpose |
|---|---|
| `tropis-smart-check.sh` | Wrapper NPD invokes. Guarantees exit 0/1/2 and one line of at most 80 characters, even if the binary is missing or misbehaves |
| `smart-plugin-monitor.json` | CustomPluginMonitor config: a temporary event plus a permanent `DiskSMARTProblem` condition |
| `test.sh` | Protocol conformance test, run in CI |

## Install

On each node, alongside NPD:

1. Place `tropis-collector` at `/usr/local/bin/tropis-collector`, and
   `smartctl` (smartmontools) on `PATH`.
2. Place `tropis-smart-check.sh` at `/usr/local/bin/tropis-smart-check.sh`.
3. Add `smart-plugin-monitor.json` to NPD's
   `--config.custom-plugin-monitor` list.

The plugin needs the same access as the Tropis collector: `SYS_RAWIO` and
read access to the block devices. See [SECURITY.md](../../SECURITY.md).

Growth rules (reallocated or uncorrectable counts rising) keep the previous
reading in `TROPIS_STATE_DIR`, default `/var/lib/tropis`. Without a writable
state directory they stay silent and the absolute rules still apply.

## Why the message is short

NPD's own example config caps plugin output at 80 characters, and NPD folds
it into a boolean condition. That is fine for a trigger and useless as
evidence — which is exactly how Tropis treats NPD conditions: they may raise a
node for analysis, but the reasoning layer reads the raw SMART data itself.

## Upstream

This is intended to be contributed to node-problem-detector, which has no
SMART plugin. Upstream may prefer a self-contained script over a wrapper
around a Go binary; if so, the rules in `pkg/host/prefilter` are the
specification to port. The PR waits until Tropis has published accuracy
numbers.
