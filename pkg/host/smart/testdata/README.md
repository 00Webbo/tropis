# SMART parser test data

Sample `smartctl -j` output the parser is tested against.

## Provenance

Two kinds of sample live here, and the distinction matters.

### Captured from a real `smartctl`

Produced by **smartctl 7.4** on Linux (`x86_64-linux-6.18.33.2`), running
against real block devices. These are verbatim tool output, not
reconstructions.

| File | Device | Notes |
|---|---|---|
| `nvme-healthy.json` | NVMe controller via the kernel's `nvme-loop` target | Full `nvme_smart_health_information_log`. `user_capacity.bytes` exceeds JSON number precision while `bytes_s` carries the exact value |
| `scsi-sas-healthy.json` | `scsi_debug` SCSI disk, SAS transport | `exit_status: 4`, SMART available but not enabled, protocol-independent `temperature` |
| `scsi-no-smart.json` | Virtual SCSI disk | Device present, reports no SMART data at all |
| `error-missing-device.json` | `/dev/nonexistent0` | `exit_status: 1`, `smartctl.messages[]` carries the error |
| `error-permission-denied.json` | Unprivileged open of `/dev/sda` | `exit_status: 2`, "Permission denied" |
| `error-wrong-devtype.json` | SCSI disk probed as `-d nvme` | `exit_status: 2` with a `device` block still present |

These cover the failure paths that matter operationally: the collector runs
unprivileged by accident, a configured device is gone after a reboot, or a
device simply has nothing to report. In all three, smartctl still emits valid
JSON with a non-zero `exit_status` — so a parser keying on process exit code
alone would misread them.

### Constructed

SATA attribute tables need physical SATA hardware, which the development
environment does not have. These samples are **constructed** to match the
exact structure emitted by `ataPrintSmartAttribWithThres` in
`smartmontools/src/ataprint.cpp` (release 7.4), field for field, including
the `flags` sub-object and the `raw.value`/`raw.string` pair.

| File | Description |
|---|---|
| `sata-healthy.json` | Healthy SATA SSD, no reallocated or pending sectors |
| `sata-failing.json` | Failing SATA drive: reallocated sectors, pending sectors, offline uncorrectable, overall health FAILED |

They are structurally faithful and are good enough to test parsing and rule
evaluation, which is what they are for.

**They are test data, not evaluation fixtures.** Nothing here may enter
`eval/fixtures/` or contribute to a published accuracy number. The eval corpus
is captured from real hardware only — see [docs/fixtures.md](../../../../docs/fixtures.md).

## Regenerating the captured samples

The captured samples were produced in a privileged container. `hack/capture-smart-samples.sh`
reproduces them.
