# Fault injection

Scripts that inject disk faults on the capture rig, so a fixture can record
what Tropis's collectors see while a known fault is active.

**These scripts destroy data.** They run as root on a node and act on a
block device. Read the safety section before running any of them.

## Faults

| Script | Fault type | Mechanism | SMART changes? |
|---|---|---|---|
| `read-errors.sh` | `disk.read_errors` | dm-dust (dm-error fallback) | No |
| `write-errors.sh` | `disk.write_errors` | dm-flakey `error_writes` | No |
| `io-timeouts.sh` | `disk.io_timeout` | dm-delay | No |
| `pending-sectors.sh` | `disk.pending_sectors` | `hdparm --make-bad-sector` on real SATA; dm-dust elsewhere | Real SATA only |
| `degradation.sh` | `disk.progressive_degradation` | growing bad-sector set, via hdparm or dm-dust | Real SATA only |
| `fs-corruption.sh` | `disk.fs_corruption` | overwrites an inode on a mounted ext4 | No |
| `disk-exhaustion.sh` | `disk.exhaustion` | fills the filesystem, root reserve included | No |
| `containerd-kill.sh` | `runtime.containerd_kill` | SIGKILL to containerd, once or repeatedly | No |

Every script takes `start`, `stop` and `status`. `start` and `stop` each print
one JSON record on stdout, shaped for `label.json`:

```json
{"faultType": "disk.read_errors",
 "injection": {"tool": "dm-dust", "device": "/dev/sdb",
               "params": {"badBlocks": "200000 200001", "smartEffect": "none",
                          "smartBefore": "reallocated=0 pending=0 uncorrectable=0",
                          "smartAfter":  "reallocated=0 pending=0 uncorrectable=0"},
               "startedAt": "...", "endedAt": "..."}}
```

### SMART is recorded, not assumed

Most of these faults reproduce the *I/O symptoms* of a failing disk without
touching its SMART data — only real hardware can change SMART counters. Every
record says which: `smartEffect` is `real`, `emulated` or `none`, and
`smartBefore`/`smartAfter` carry the counters as read through Tropis's own
SMART parser.

This matters for the eval corpus. A fixture whose label claims a SMART-visible
fault when the counters never moved would be a synthetic fixture wearing a
real one's clothes, and would void the accuracy numbers. The record makes the
difference impossible to lose.

## Setup

1. Describe the rig in an inventory (see [docs/inventory.md](../../docs/inventory.md)),
   with the sacrificial disk marked `destructible: true` and a `mountpoint`.
2. Put `tropis` and `tropis-collector` on the node's `PATH`, or point
   `TROPIS_BIN` and `TROPIS_COLLECTOR` at them.
3. Create the pass-through device and give the workload a filesystem on it:

   ```sh
   export TROPIS_INVENTORY=rig.yaml TROPIS_NODE=worker-02 TROPIS_DISK=sacrificial
   export TROPIS_INJECT_CONFIRM=worker-02/sacrificial
   hack/inject/prepare.sh start
   mkfs.ext4 /dev/mapper/tropis-target
   mount /dev/mapper/tropis-target /mnt/disks/sacrificial
   ```

   Workloads under test use the mountpoint through a `hostPath` or local
   PersistentVolume.

4. Inject, wait for symptoms, capture while the fault is active, then stop:

   ```sh
   hack/inject/read-errors.sh start > fault.json
   # ... let the workload hit the fault ...
   tropis capture --scenario read-errors-postgres --variant npd-absent \
     --node worker-02 --disk sacrificial --inventory rig.yaml \
     --injection fault.json --prometheus http://prometheus.monitoring:9090
   hack/inject/read-errors.sh stop
   ```

   Capture runs before `stop` because stopping removes the fault, and with
   it the state the fixture exists to record. `tropis capture` refuses to
   write a fixture that would misdescribe itself: a `real-sata` scenario
   whose injection only emulated SMART, an NPD variant that does not match
   what is on the node, or a fault type the scenario does not expect.
   Repeat with NPD installed for the `npd-present` variant.

The device-mapper faults swap the pass-through device's table live, so a
fault begins under a running workload, exactly as a disk failing in service
does, and `stop` swaps the pass-through back.

## Safety

- **The inventory decides.** A target disk is resolved only through
  `tropis inventory target`, which refuses any disk not explicitly marked
  `destructible: true`. There is no command-line override.
- **Confirmation.** Nothing runs unless `TROPIS_INJECT_CONFIRM` equals
  `<node>/<disk>` — or `<node>/containerd` for `containerd-kill.sh`.
- **System devices.** A device, or anything built on it, mounted at `/`,
  `/var`, `/var/lib/kubelet`, `/var/lib/containerd`, `/var/lib/etcd` or
  similar is refused, whatever the inventory says.
- **Filesystem faults check the mount.** Exhaustion and corruption refuse a
  mountpoint whose filesystem is not on the target device.
- `fs-corruption.sh` cannot be undone by the script. `stop` prints the
  `e2fsck` needed.

## Testing

`test.sh` runs every script end to end against a loop device and checks the
fault's actual effect — that a bad block really fails to read, a delayed read
really times out, the filesystem really reports corruption — that `stop`
removes it, and that the refusals hold. It must run as root in a disposable
privileged container, and refuses to run without `TROPIS_INJECT_TEST=1`:

```sh
docker run --rm --privileged -v /dev:/dev -v /lib/modules:/lib/modules:ro \
  -v "$PWD:/repo" -e TROPIS_INJECT_TEST=1 ubuntu:24.04 \
  bash /repo/hack/inject/test.sh /repo/bin/tropis /repo/bin/tropis-collector
```

CI runs it twice, the second time with `TROPIS_NO_DUST=1` to force the
dm-error fallback, so both bad-sector mechanisms stay covered whichever the
runner's kernel provides.
