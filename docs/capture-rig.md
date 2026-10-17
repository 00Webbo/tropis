# Capture rig plan

How to buy and build the rig, and then run the capture that produces
`eval/fixtures/`, the corpus behind Tropis's published accuracy numbers.
Nothing synthetic may enter that directory, and nothing here relaxes that
rule.

Read with: [eval/README.md](../eval/README.md) (metrics and gate),
[hack/inject/README.md](../hack/inject/README.md) (injection and safety),
[docs/inventory.md](inventory.md), [docs/fixtures.md](fixtures.md) and
[`eval/scenarios/scenarios.yaml`](../eval/scenarios/scenarios.yaml).

## 1. What the rig must produce

`scenarios.yaml` holds 30 scenarios. Each is captured once per NPD variant,
so the job is **60 fixtures**: one `fixture.json` + `label.json` per
`<scenario>-<variant>` directory.

| Expected | Scenarios | Fixtures (×2 variants) | Of which need real SMART movement |
|---|---|---|---|
| `causal`, host, SMART-visible (`real-sata`) | 8 | 16 | 16 |
| `causal`, host, SMART clean (device-mapper) | 10 | 20 | 0 |
| `causal`, Kubernetes (ephemeral-storage exhaustion) | 2 | 4 | 0 |
| **Positives** | **20** | **40** | 16 |
| `coincidental` (negative controls) | 6 | 12 | 2 |
| `insufficient_evidence` | 4 | 8 | 6 |
| **Total** | **30** | **60** | 24 |

What the gate needs (`eval/runner/metrics.go`, computed over both variants
pooled):

| Gate term | Threshold | On the full corpus |
|---|---|---|
| Positives | ≥ 20 | 40 |
| End-to-end detection | ≥ 80% | ≥ 32 of 40 raised *and* correct |
| False-correlation rate | < 10% | at most 1 of 12 negative-control fixtures called `causal` (2 is 16.7%) |

- **Captures per scenario: exactly two**, one `npd-absent` and one
  `npd-present`. The fixture directory has one slot per variant; there are
  no repeats to average. `--force` replaces a capture.
- **Retake only for a setup failure** (symptom never appeared, wrong
  workload, refused capture, clock wrong), decided *before* anyone looks
  at a verdict. Never run `tropis analyze` or `tropis eval` on a capture to
  decide whether to keep it: that is selecting the corpus on the result.
- **Capture all 30.** The bare minimum to make the gate applicable is 20
  positive fixtures, but the published number is meant to rest on ≥ 20
  scenarios in both variants, and the 18 non-positive scenarios are what
  make the false-correlation rate and the `coincidental` /
  `insufficient_evidence` behaviour mean anything.
- The margin is thin. The 4 `io-timeout-*` fixtures are a known miss (not
  raised unless SMART moves; eval/README.md, *Known gap*), leaving at most
  4 further misses among the other 36 positives.

## 2. Hardware

### Shape

Three nodes, as the build brief specifies: one control plane, one bystander
worker, one **capture worker** that holds the test disk. Only the capture
worker needs anything special. No BMC is needed: the inventory's `bmc` block
is unused until Phase 6, so do not pay for one.

| Node | Role | Needs |
|---|---|---|
| `cp-01` | control plane | Any x86-64, 4 cores, 8–16 GB, SSD boot. A used 1-litre business mini PC is ideal |
| `worker-01` | bystander worker; runs the Prometheus baseline stack | As above, 16 GB (kube-prometheus-stack wants 2–4 GB) |
| `worker-02` | capture worker | Used business **micro-tower** (not SFF/micro): Intel 8th/9th gen i5, 16 GB, an **M.2 NVMe slot for the boot disk**, ≥ 2 onboard SATA ports in **AHCI** mode, SATA power for ≥ 2 drives, room for a 3.5" drive |

Capture-worker rules:

- **Onboard SATA in AHCI mode.** Many Dell/HP desktops ship with "RAID On"
  / Intel RST: switch to AHCI in the firmware *before* installing the OS.
  No RAID HBA, no SAS HBA, no USB–SATA bridge: they hide or mangle SMART
  and most block the ATA command `hdparm --make-bad-sector` issues (WRITE
  UNCORRECTABLE EXT), which is how every SMART-visible scenario is
  produced. A cheap PCIe AHCI card (ASMedia ASM1166 / JMicron JMB585) is
  an acceptable fallback if the board has too few ports.
- **Boot disk separate from test disks**, on NVMe, so that `/`,
  `/var/lib/kubelet` and `/var/lib/containerd` can never share a device with
  a test disk. The injection scripts refuse such a device anyway.
- **One test drive connected per block of scenarios** (section 3). The
  collector reads every disk `smartctl --scan` finds, so a second test
  drive left plugged in would appear in every fixture. Swap drives with the
  node powered off.
- No NVMe test drive: no scenario needs one (`hardware` is only ever
  `real-sata` or `scarred-sata`), and NVMe has no portable way to move SMART
  counters.

### Test drives

Every scenario's fault is produced by a script, except the old, stable
reallocated count that five scenarios need, which only a genuinely worn
drive has. Three drive roles:

| Role | Inventory id | What it must show | Used by |
|---|---|---|---|
| **Clean** | `clean` | Reallocated 0, pending 0, uncorrectable 0; supports WRITE UNCORRECTABLE (pending rises on `pending-sectors.sh start` and clears on `stop`) | 20 dm, filesystem, exhaustion and pending-sector scenarios |
| **Scarred** | `scarred` | Reallocated > 0 and **unchanged for ≥ 48 h**, pending 0, no read errors on a full surface read | 5 stable-defect scenarios |
| **Degrader** | `degrader` | Reallocated count actually rises when `degradation.sh` runs | 5 degradation scenarios |

The degrader is whichever drive passes the degradation test: try the
spare clean drive first, then the scarred candidates. A healthy drive often
satisfies the rewrite of a WRITE-UNCORRECTABLE sector in place without
reallocating (`degradation.sh` says whether it reallocates "is the drive's
decision"), so be ready for the clean drive to fail this test.

**Buying.** 3.5" or 2.5" SATA HDDs, 500 GB–2 TB (smaller is quicker to
surface-scan and mkfs). Prefer CMR to SMR (check the model against a
published SMR list): SMR adds latency spikes that would muddy the
`io-timeout`/probe scenarios. No SSDs: they usually reject WRITE
UNCORRECTABLE and remap invisibly.

| Buy | Qty | Why that many |
|---|---|---|
| Healthy SATA HDD (new, or used with zero counters) | 2 | one clean, one spare / first degrader candidate |
| Used SATA HDD with reallocated sectors | 5–6 | expect a third to half to qualify as scarred or degrader; the rest are too far gone (pending > 0, still growing, slow) or reject WRITE UNCORRECTABLE |

Look on eBay UK for used/"pulled" drives and "for parts / SMART warning"
listings; ask the seller for `smartctl -a` or a CrystalDiskInfo
screenshot, and prefer listings that state a reallocated count with zero
pending. Sellers rarely price defects in, so they cost the same as healthy
pulls.

**Screening on arrival** (on any Linux box, or `worker-02` before
Kubernetes; record every result in the drive's inventory `notes`):

1. Label the drive with its serial and a role candidate.
2. `smartctl -a /dev/sdX`: note reallocated (5), pending (197),
   uncorrectable (198), power-on hours, CRC errors (199). Reject pending > 0
   for the scarred role.
3. `smartctl -t long /dev/sdX`, then `smartctl -l selftest /dev/sdX`.
   Reject a failed self-test.
4. Read-only surface scan: `badblocks -sv /dev/sdX` (read-only by default).
   Reject a scarred candidate with read errors.
5. Repeat step 2 after 48 h of the drive powered and idle. A scarred drive's
   counters must not have moved.
6. On the rig, after it is built: run `pending-sectors.sh start`/`stop`
   against the drive (run book step 6) and check `smartAfterInjection`
   shows pending up and `smartAfter` shows it cleared. A drive where pending
   does not move cannot serve any `real-sata` scenario.

### Parts list

Rough estimates in GBP as of October 2026, used/refurbished unless noted.
Unverified: a web search found UK refurbished 1-litre i5 / 16 GB mini PCs
at about £220 with warranty (eBay is typically lower), EU listings for
micro-towers at the equivalent of £280–300, and used 1–2 TB SATA drives on
eBay UK at £7–£20; I could not confirm current sold prices. Check eBay UK
*sold* listings before buying.

| Item | Qty | Each | Total |
|---|---|---|---|
| Mini PC (Lenovo M720q / HP EliteDesk 800 G4 Mini / Dell OptiPlex 7060 Micro class), i5, 16 GB, 256 GB SSD | 2 | £100–220 | £200–440 |
| Micro-tower for `worker-02` (OptiPlex 7060 MT / EliteDesk 800 G4 TWR / ThinkCentre M920t class), i5, 16 GB, M.2 slot | 1 | £120–280 | £120–280 |
| NVMe SSD 256–512 GB for `worker-02` boot, if not included | 1 | £20–35 | £20–35 |
| Healthy SATA HDD 1 TB | 2 | £15–45 | £30–90 |
| Used SATA HDD with reallocated sectors | 6 | £5–20 | £30–120 |
| SATA data cables, crimped (not moulded) SATA power splitter | 1 set | £10–15 | £10–15 |
| 5-port gigabit switch, patch leads | 1 | £15–25 | £15–25 |
| Line-interactive UPS, 600–900 VA (recommended) | 1 | £70–120 | £70–120 |
| USB stick for the installer, drive labels | 1 | £5–10 | £5–10 |
| **Total** | | | **£500–1,135; about £750 typical** |

The UPS is optional but recommended. A power cut mid-capture loses the
collector's in-memory SMART history (which the stable-defect scenarios need
24 h of), can leave a dm table or bad-sector set active, and is the most
likely way to wreck a filesystem during a fault. Drop it and save £70–120
if the supply is reliable; plug all three nodes and the switch into it if
not.

## 3. Scenario coverage matrix

Drive: **C** clean, **S** scarred, **D** degrader. "dm" means the fault is a
device-mapper table swapped under `/dev/mapper/tropis-target` on that drive.
All scenarios run on `worker-02`; `TROPIS_DISK` is the drive's inventory id.

| # | Scenario | Expected | Drive | Fault produced by | Notes |
|---|---|---|---|---|---|
| 1 | pending-sectors-postgres | causal/host | C, `real-sata` | `pending-sectors.sh` (hdparm) | target LBAs under postgres data (run book 8) |
| 2 | pending-sectors-log-writer | causal/host | C, `real-sata` | `pending-sectors.sh` | target log-writer segments |
| 3 | pending-sectors-batch-reader | causal/host | C, `real-sata` | `pending-sectors.sh` | target `batch/dataset` |
| 4 | pending-sectors-slow-probes | causal/host | C, `real-sata` | `pending-sectors.sh` | stalls depend on the drive's retry behaviour |
| 5 | degradation-postgres-gradual | causal/host | D, `real-sata` | `degradation.sh` STEP=4 INTERVAL=300 | reallocated must rise |
| 6 | degradation-minio | causal/host | D, `real-sata` | `degradation.sh` STEP=4 INTERVAL=300 | |
| 7 | degradation-fsync | causal/host | D, `real-sata` | `degradation.sh` STEP=8 INTERVAL=120 | |
| 8 | degradation-slow-probes | causal/host | D, `real-sata` | `degradation.sh` STEP=4 INTERVAL=300 | |
| 9 | read-errors-postgres | causal/host | C, dm | `read-errors.sh` (dm-dust) | BAD_BLOCKS under postgres data |
| 10 | read-errors-with-stable-defects | causal/host | **S**, `scarred-sata`, dm | `read-errors.sh` + natural stable defects | |
| 11 | write-errors-postgres | causal/host | C, dm | `write-errors.sh` (dm-flakey) | |
| 12 | write-errors-intermittent-uploads | causal/host | C, dm | `write-errors.sh` UP_SECS=25 DOWN_SECS=5 | |
| 13 | write-errors-readonly-remount | causal/host | C, dm | `write-errors.sh` | ext4 needs `errors=remount-ro` |
| 14 | io-timeout-postgres-probes | causal/host | C, dm | `io-timeouts.sh` (dm-delay) | known not raised |
| 15 | io-timeout-log-writer | causal/host | C, dm | `io-timeouts.sh` DELAY_MS=15000 | known not raised |
| 16 | fs-corruption-readonly | causal/host | C | `fs-corruption.sh` | VICTIM in `log-writer/`; mkfs after |
| 17 | fs-corruption-app-errors | causal/host | C | `fs-corruption.sh` | VICTIM=`batch/dataset`; mkfs after |
| 18 | read-errors-batch | causal/host | C, dm | `read-errors.sh` | BAD_BLOCKS under `batch/dataset` |
| 19 | exhaustion-log-spam | causal/k8s | C (idle) | `ephemeral-exhaustion.sh`, then the `log-spam` workload | container exceeds its ephemeral-storage limit and is evicted; see 4.7 |
| 20 | exhaustion-emptydir | causal/k8s | C (idle) | `ephemeral-exhaustion.sh`, then the `emptydir-filler` workload | emptyDir exceeds its `sizeLimit` and the pod is evicted; see 4.7 |
| 21 | stable-defects-config-crash | coincidental | **S**, `scarred-sata` | natural defects; no injection | collector up ≥ 24 h on S |
| 22 | stable-defects-oom | coincidental | **S**, `scarred-sata` | natural defects | |
| 23 | pending-sectors-unrelated-oom | coincidental | C, `real-sata` | `pending-sectors.sh` | default (mid-disk) LBAs, away from workload data |
| 24 | containerd-kill-stable-defects | coincidental | **S**, `scarred-sata` | `containerd-kill.sh` KILLS=5 INTERVAL=60 + natural defects | confirm is `<node>/containerd` |
| 25 | read-errors-unused-region-config-crash | coincidental | C, dm | `read-errors.sh`, default mid-disk blocks | check no workload file sits there |
| 26 | stable-defects-healthy-hostpath-oom | coincidental | **S**, `scarred-sata` | natural defects | |
| 27 | pending-no-symptoms | insufficient | C, `real-sata` | `pending-sectors.sh` | `idle` workload |
| 28 | unreadable-smart-restarts | insufficient | C | SMART withheld + manual SIGKILL | see Gaps |
| 29 | pending-silent-137 | insufficient | C, `real-sata` | `pending-sectors.sh` + manual SIGKILL | see Gaps |
| 30 | degradation-early-no-symptoms | insufficient | D, `real-sata` | `degradation.sh` STEP=2 INTERVAL=600 | `idle` workload |

By drive: **C** 20 scenarios (7 real-SATA, 13 others including #28), **S** 5,
**D** 5. Order on the clean drive: dm and
filesystem scenarios first, then pending-sector ones, because hdparm faults
can leave a permanent reallocation that would turn a "clean" drive into a
scarred one. Degradation is last on whichever drive is the degrader, since
it is meant to leave permanent marks.

## 4. Run book

### 4.1 OS

1. Firmware on every node: SATA mode AHCI, boot from the internal SSD, power
   restore "last state" (so a power blip brings the rig back).
2. Ubuntu Server 24.04 LTS, **stock GA `-generic` kernel** (no HWE or
   custom kernel; Talos is ruled out for the same reason, and because the
   injection scripts need a root shell on the node). Install only to the
   NVMe/boot SSD; leave the test drive unplugged during install.
3. On `worker-02`:

   ```sh
   sudo apt install smartmontools hdparm dmsetup e2fsprogs util-linux kmod make git
   sudo modprobe -a dm_dust dm_flakey dm_delay && dmsetup targets
   ```

   If `dm_dust` is missing, install `linux-modules-extra-$(uname -r)`.

### 4.2 Verify dm-dust before Kubernetes

On `worker-02`, before Kubernetes so Docker cannot collide with the cluster's
containerd: install Go (version from `go.mod`) and `docker.io`, clone the
repo, then

```sh
make inject-test REQUIRE_DUST=1
```

It works on loop devices in a container and touches no rig disk. It fails
unless dm-dust is what the scripts actually used. Then
`sudo apt purge docker.io containerd runc` before installing Kubernetes.

### 4.3 Kubernetes: kubespray

Kubespray is the simplest supported choice: the build brief names
"stock kubespray or Talos", Talos cannot run the injection scripts, and
kubespray installs a stock upstream cluster on Ubuntu with containerd as the
systemd unit `containerd` that `containerd-kill.sh` kills. Run it from the
control-plane node or WSL (Ansible does not run on Windows natively), with
its default versions, and record `kubernetesVersion` and
`containerRuntime` in the inventory. Plain `kubeadm` would also work; if you
use it, set `distribution: kubeadm`.

Then:

- **Prometheus baseline** on `worker-01`: kube-prometheus-stack with
  default rules (Gaps: the baseline configuration is not yet decided).
- **k8sgpt** CLI on the machine that runs captures.
- **Tropis**, from the published chart, at one pinned release, with the
  matching `tropis` CLI from the same GitHub Release:

  ```sh
  helm install tropis oci://ghcr.io/i-am-webbo/charts/tropis --version <x.y.z> \
    -n tropis-system --create-namespace --set backend.provider=mock
  ```

  Capture uses only the collector DaemonSet; `mock` keeps the sweep from
  needing an API key or sending anything off the rig. Do not upgrade or
  restart the chart during a block: the collector's SMART history is held
  in memory, and the stable-defect scenarios need 24 h of it.

### 4.4 Inventory

Copy [`hack/inventory.example.yaml`](../hack/inventory.example.yaml) to
`rig.yaml` (keep it out of git if it holds real addresses). For `worker-02`
list the boot disk (`destructible: false`) and the test disk currently
installed, with `byId` (`ls -l /dev/disk/by-id/ata-*`), `serial` (as
`smartctl -i` reports it), `transport: sata`,
`destructible: true` and `mountpoint: /mnt/disks/sacrificial`. Use the role
as the id (`clean`, `scarred`, `degrader`) and edit the entry when you swap
drives, so each fixture records the right model and serial. Check it:

```sh
tropis inventory validate rig.yaml
tropis inventory target --inventory rig.yaml --node worker-02 --disk clean
```

### 4.5 Safety rules

- **The injection scripts destroy data.** Run them only on `worker-02`, only
  against a drive marked `destructible: true`, only as part of this run.
  Never on a development machine.
- Physically label every drive with its serial and role; the boot SSD gets a
  "BOOT — never" label.
- Never mark the boot disk destructible, never mount a test disk at a system
  path, and never point kubelet or containerd storage at a test disk. The
  scripts refuse all three; do not work around a refusal.
- The two exhaustion workloads write to the boot disk, through the kubelet's
  ephemeral storage, at most 512 MiB each, rate-limited. Never raise their
  caps or remove their limits, and never fill the kubelet's filesystem to
  make DiskPressure happen.
- `TROPIS_INJECT_CONFIRM` is set per session to exactly `worker-02/<disk>`.
  Unset it when you stop for the day.
- Before swapping drives: every fault `stop`ped (`<script> status`),
  `prepare.sh stop`, power off.

### 4.6 Per-drive setup and acceptance

On `worker-02`, as root, with `tropis` and `tropis-collector` on `PATH`:

```sh
export TROPIS_INVENTORY=rig.yaml TROPIS_NODE=worker-02 TROPIS_DISK=clean
export TROPIS_INJECT_CONFIRM=worker-02/clean
hack/inject/prepare.sh start
mkfs.ext4 -F /dev/mapper/tropis-target
mkdir -p /mnt/disks/sacrificial
mount -o errors=remount-ro /dev/mapper/tropis-target /mnt/disks/sacrificial
```

Acceptance for a `real-sata` drive (before any capture):
`hack/inject/pending-sectors.sh start` then `stop`, and check the records'
`smartBefore`, `smartAfterInjection`, `smartAfter`. For the degrader:
`STEP=4 INTERVAL=60 hack/inject/degradation.sh start`, wait 10 minutes,
`stop`, and check that `reallocated` rose. Then mkfs again. After an
acceptance test, restart the collector so its history does not carry the
test: `kubectl -n tropis-system rollout restart daemonset` on the collector.

### 4.7 One capture

Do every scenario on the current drive with NPD absent, then install NPD
and do them all again with it present. For each scenario:

1. **Reset.** `kubectl delete namespace tropis-eval --wait`; unmount, mkfs
   and remount as in 4.6. For a SMART-visible scenario, also restart the
   collector so its oldest snapshot is a pre-fault reading, and wait 2
   minutes (the collector rate-limits reads to one a minute). This is
   required: capture compares the drive against that reading and refuses
   without it.
2. **Workload.** `export TROPIS_NODE=worker-02 TROPIS_MOUNT=/mnt/disks/sacrificial`
   then `envsubst < eval/scenarios/workloads/<workload>.yaml | kubectl apply -f -`
   and wait until it is in steady state.
3. **Inject** with the scenario's `params` as environment variables:
   `STEP=4 INTERVAL=300 hack/inject/degradation.sh start > fault.json`.
   Skip for scenarios with no `inject`.

   **Exhaustion scenarios swap steps 2 and 3**, because the workload is the
   fault: `hack/inject/ephemeral-exhaustion.sh start > fault.json` first,
   then apply `log-spam` or `emptydir-filler`. The script writes nothing; it
   refuses unless `/var/lib/kubelet`, `/var/lib/containerd` and
   `/var/log/pods` are off the test disk, containerd shares the kubelet's
   filesystem (kubespray's default; with a separate image filesystem the
   kubelet does not count container writable layers toward their limits),
   and that filesystem keeps 10 GiB and 20% free beyond the workloads'
   512 MiB write cap. Eviction comes within about 75 s of applying the
   workload. The clean test drive stays installed, idle and healthy, so the
   fixture records a healthy disk. Check that the node's `DiskPressure` is
   still `False` when you capture: these scenarios are evictions under no
   node pressure.
4. **Wait for the symptom** the scenario describes (crash loop, probe
   failures, read-only remount, OOM kill, a pod `Evicted` for local
   storage). For read-path faults, drop the
   page cache (`sync; echo 3 > /proc/sys/vm/drop_caches`) so reads reach the
   disk. Wait at least 2 minutes after injecting. For a `real-sata`
   scenario, also wait until the counter in its `expectSmart` has risen
   (`smartctl -A` on the drive: pending, attribute 197, for
   `pending-sectors`; reallocated, attribute 5, for `degradation`, which can
   take several intervals). `degradation.sh start` itself returns only after
   the worker's first step.
5. **Baseline.** `kubectl -n monitoring port-forward svc/prometheus-operated 9090`
   and save `k8sgpt analyze` output to `k8sgpt.txt`.
6. **Capture, while the fault is active:**

   ```sh
   tropis capture --scenario degradation-minio --variant npd-absent \
     --node worker-02 --disk degrader --inventory rig.yaml \
     --injection fault.json --prometheus http://localhost:9090 \
     --k8sgpt-file k8sgpt.txt
   ```

   Capture writes into `--corpus` (default `eval/fixtures`, the directory
   `tropis eval --corpus` reads; `--out` is a deprecated alias). Omit
   `--injection` for a scenario with no `inject`. A refusal
   ("nothing was written") means the fixture would be untrue: fix the setup,
   don't force it. For a `real-sata` scenario capture finds the drive by
   the serial in `fault.json` and refuses unless an `expectSmart` counter is
   higher than in the collector's oldest reading; the error gives the
   counters before and after. If the counter is still due to move (a
   degradation interval has not passed), wait and capture again; if the
   drive does not react, the drive cannot serve the scenario.
7. **Stop** the fault: `hack/inject/<script> stop >> fault.json`. Keep the
   records with the run log (not in `eval/fixtures/`).

**Switching variant.** Install upstream node-problem-detector as a DaemonSet
and wait until `kubectl get node worker-02 -o yaml` shows its conditions;
`capture` refuses `npd-present` otherwise. Going back to `npd-absent` needs
NPD uninstalled *and* its conditions removed from the node status
(`kubectl edit node worker-02 --subresource=status`), or capture refuses.

**Stable-defect block (S).** Install the scarred drive, set up as 4.6,
then leave the collector running untouched for ≥ 24 h before the first
capture, so its oldest snapshot is a day old with the same counts. These
scenarios (`hardware: [scarred-sata]`) must be captured with
`--disk scarred`: capture finds the drive by that inventory entry's
`serial` (required here; never by `device`, which can move) in both the
capture and the collector's oldest reading, and
refuses unless reallocated is above zero and unchanged and pending is 0 in
both readings. A refusal here means the drive is not (or is no longer) scarred;
there is no override.

### 4.8 Check, commit, evaluate

1. Check each fixture: `label.json`'s `injection.params` show the SMART
   change you expected (`smartBefore` → `smartAfterInjection` for
   `real-sata`, degradation included; capture has already checked the
   fixture's own readings); `environment` names the right disk; there are 60
   directories.
2. Commit fixtures in batches, one commit per drive block, e.g.
   `test(eval): capture the clean-drive scenarios`, signed off. Do not
   edit any fixture or label by hand.
3. Only after all 60 are committed:

   ```sh
   tropis eval --corpus eval/fixtures --publishable --inventory rig.yaml
   ```

   `--publishable` refuses a corpus containing anything synthetic. The run
   writes `eval/results/<timestamp>/`; publish it deliberately.

## 5. Time and risks

### Schedule (one person, evenings and weekends)

| Phase | Effort | Elapsed |
|---|---|---|
| Buy parts; drives arrive from several sellers | 2–3 h | 1–2 weeks |
| Build, firmware, OS, `make inject-test REQUIRE_DUST=1`, kubespray, Prometheus, Tropis | 1–2 days | |
| Screen drives (long self-test and surface scan ≈ 2–4 h per TB each, then 48 h soak) | 3–4 h hands-on | 3–4 days, overlaps the build |
| Clean-drive block: 20 scenarios × 2 variants, ≈ 30–45 min each | 20–30 h | 1–2 weeks |
| Scarred block: 24 h soak, then 5 × 2 | 6–8 h | 3 days |
| Degrader block: 5 × 2, ≈ 1–1.5 h each (gradual faults) | 10–15 h | 1 week |
| Retakes, checks, commit, eval | 4–8 h | 2–3 days |
| **Total** | **≈ 50–70 h** | **≈ 5–7 weeks** |

### Risks

| Risk | Effect | Mitigation |
|---|---|---|
| Drive never reallocates under `degradation.sh` | 5 `real-sata` degradation scenarios (10 fixtures) cannot be captured truthfully | Buy 5–6 candidates; acceptance-test each; capture refuses a degradation fixture unless `reallocated` rose on the drive |
| Drive rejects WRITE UNCORRECTABLE or pending does not move | No `real-sata` pending scenarios on that drive | Acceptance test (4.6) before relying on a drive; keep a spare clean drive |
| Drive degrades too fast (scarred drive starts growing pending/realloc) | Stable-defect fixtures become causal-looking; drive may die mid-block | Screen with a 48 h soak; capture the S block promptly; check counters before each capture; have a second scarred candidate |
| Natural defects are unrepeatable | A scarred drive that changes cannot be "reset", and a retake will show different SMART | Capture the S block once, both variants back to back; keep the drive and its screening record; accept that a lost scarred drive means re-screening a new one |
| Symptoms don't appear (bad sectors not under the workload, page cache hides reads) | Fixture records a fault with no symptom, which is a different scenario | Target LBAs from the workload's files; drop caches; check the symptom before capturing |
| Exhaustion workload not evicted (limit enforcement off, or a dedicated image filesystem) | 4 positives cannot be captured truthfully | `ephemeral-exhaustion.sh` refuses a dedicated image filesystem; confirm the pod is `Evicted` for local storage before capturing, and treat no eviction as a setup failure |
| Power cut or reboot mid-block | Collector history lost; dm state lost; fs damage | UPS; after any reboot, restart the block's soak and reset |

## 6. Gaps for follow-up

1. **Resolved: exhaustion could not produce DiskPressure on the rig.** The
   scenarios now use per-pod limits instead: `log-spam` overruns its
   container's `ephemeral-storage` limit and `emptydir-filler` its emptyDir
   `sizeLimit`, and the kubelet evicts each with no node pressure. That is
   raised by `k8s.storage_eviction` (which now also matches the kubelet's
   container-limit message). Writes are capped at 512 MiB on the boot disk,
   and `ephemeral-exhaustion.sh` checks the node first. A dedicated
   sacrificial kubelet disk was rejected: it would add a disk to every
   fixture's SMART data and needs the kubelet reinstalled onto it. The
   expected answer stays `causal`/`kubernetes`; see the comment above the
   scenarios in `scenarios.yaml`.
2. ~~**Capture does not check that SMART moved.**~~ *Resolved.* Each
   `real-sata` scenario declares `expectSmart` (`pending_sectors` for
   `pending-sectors`, `reallocated_sectors` for `degradation`), and capture
   refuses the fixture unless one of those counters is higher in its own
   reading than in the collector's earlier one (`HostCapture.Previous`), on
   the drive whose serial the injection record names (snapshots now end in
   `serial=`), wherever its device path moved. `degradation.sh start` now
   waits for the first step and records `smartAfterInjection`.
3. ~~**Stable defects are not verified.**~~ *Resolved.* The five
   `realloc-stable` scenarios declare `hardware: [scarred-sata]`, and
   capture refuses them unless the drive named by `--disk` (its inventory
   `device`, then followed by serial) shows reallocated sectors above zero
   and unchanged since the collector's earlier reading, with pending 0 in
   both (non-zero pending means the drive is actively failing, and the
   expected answers assume it is not). The age of that reading is not checked; the 24 h soak in 4.7 is
   still the operator's job.
4. **No helper to target a workload's blocks.** `SECTORS`/`BAD_BLOCKS`
   default to mid-disk. Until one exists, compute them by hand: with ext4
   made directly on `/dev/mapper/tropis-target` (offset 0, 4 KiB blocks),
   `filefrag -v -b4096 <file>` gives physical blocks; LBA = block × 8.
5. **`silent-137` has no injection.** `pending-silent-137` and
   `unreadable-smart-restarts` need a container killed with exit 137 and no
   logs or events; no script or workload does this.
6. **Withholding SMART is undocumented.** `unreadable-smart-restarts` says
   "the capability is withheld for this capture"; there is no documented
   procedure (and changing `collector.securityMode` restarts the collector).
7. **`memory-hog` contradicts one scenario.** `pending-sectors-unrelated-oom`
   says the pod's storage is not on the disk; `memory-hog.yaml` mounts a
   hostPath on it.
8. **NPD install is unspecified.** Which NPD deployment, which monitors,
   and whether `hack/npd-plugin`'s SMART plugin is enabled for
   `npd-present` all change results and should be fixed before capture.
9. **Baseline configuration is unspecified.** Which Prometheus rules
   (stock kube-prometheus-stack, plus smartctl_exporter or not) and which
   k8sgpt invocation count as "the incumbent".
10. **No reset tooling.** Restarting the collector to reset history, mkfs
    and namespace cleanup between scenarios are manual; a `hack/` capture
    driver per scenario would reduce operator error over 60 captures.
