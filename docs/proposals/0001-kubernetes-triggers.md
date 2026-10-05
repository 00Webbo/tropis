# Proposal 0001: Kubernetes-side pre-filter triggers

Status: **accepted** (2026-10-05). D1: probe timeouts are not triggers, and
the gap is recorded. D2: gate on end-to-end detection. D3: `PromptVersion`
v2.

## Problem

The pre-filter decides which nodes are analysed at all. Today it raises a
node only on SMART rules or node-problem-detector conditions, so a storage
fault that SMART cannot see never reaches the model.

On the dev corpus, the pre-filter misses 22 of the 40 causal cases (55%):

| Missed scenario | What the Kubernetes layer shows |
|---|---|
| `read-errors-batch`, `read-errors-postgres` | crash loop; `Input/output error` in logs |
| `write-errors-postgres`, `write-errors-intermittent-uploads` | crash loop; `Input/output error` in logs |
| `write-errors-readonly-remount`, `fs-corruption-readonly` | crash loop; `Read-only file system` in logs (raised only with NPD) |
| `fs-corruption-app-errors` | crash loop; `Structure needs cleaning` in logs |
| `exhaustion-emptydir`, `exhaustion-log-spam` | `DiskPressure`, evictions, `EvictionThresholdMet` |
| `io-timeout-log-writer`, `io-timeout-postgres-probes` | probe timeouts, crash loop; nothing storage-specific |

The dev corpus is synthetic, so these counts show the shape of the gap, not
its size. The faults are real, though. Failing cables and controllers,
filesystem corruption, device-mapper layers and disk exhaustion all break
workloads without changing SMART.

The eval hides the gap. It analyses every fixture whether or not the
pre-filter raised it, and the gate applies only to reasoning accuracy. In a
live cluster, the share of faults Tropis reports correctly is
**pre-filter recall × reasoning accuracy**. With today's recall, that tops
out at 45%, however good the model is.

## Found while investigating: NPD reaches the model

`prefilter.Result.TriggeredBy` includes NPD triggers (`npd.<type>`), and
`reason.BuildInput` copies `TriggeredBy` into the document the model reads.
For `fs-corruption-readonly-npd-present`, the model sees
`"triggeredBy": ["npd.ReadonlyFilesystem"]`: NPD's conclusion, as evidence.
This breaks a hard rule. `TestNPDNeverReachesModelInput` misses it because
it passes no triggers.

It also undermines the NPD-present vs NPD-absent comparison, which exists to
show that accuracy does not depend on NPD.

The same channel would carry every new trigger. A node raised by a rule
called `k8s.storage_error` arrives already labelled, and the model is
already biased toward "yes, causal". **Trigger names should not reach the
model at all.** The model should reach its conclusion from the evidence,
which already contains whatever fired the trigger. `TriggeredBy` stays in the
verdict, the report and notifications, where operators use it.

This is a bug fix independent of the rest of the proposal, and should land
first (see D3 for versioning).

## Proposal

### New rules

Add `pkg/k8s/triggers`: pure functions over a `schema.K8sCapture`, mirroring
`pkg/host/prefilter`. Because the inputs are the same as a fixture's, the
eval replays them exactly. `pipeline.Prefilter` combines them with the SMART
and NPD results. Rule IDs are part of the output contract:

| Rule | Fires when |
|---|---|
| `k8s.disk_pressure` | the kubelet's own `DiskPressure` condition is `True` (a kubelet condition, not NPD) |
| `k8s.storage_eviction` | a pod on the node was evicted for ephemeral storage or nodefs, or a node event is `EvictionThresholdMet`, `FreeDiskSpaceFailed` or `ImageGCFailed` |
| `k8s.storage_error` | a **failing** container (the collector's `Unhealthy`: restarting, or terminated non-zero) has a storage error signature in its captured log tail or termination message; or a Warning event for the node or its pods carries one |

Storage error signatures are the C library's `strerror` texts for the errnos
a storage fault produces, matched case-insensitively. These are stable across
languages, because Go, Java, Python, PostgreSQL and the shell all print the
libc text:

| errno | Text |
|---|---|
| `EIO` | `input/output error` |
| `EROFS` | `read-only file system` |
| `ENOSPC` | `no space left on device` |
| `EUCLEAN` | `structure needs cleaning` |
| `EDQUOT` | `disk quota exceeded` |

The signatures come from errno semantics, **not** from the dev corpus text.
Fitting rules to fixtures we generated ourselves would be circular.

### Deliberately not triggers

Crash loops, OOM kills, exit 137, image pull failures and probe failures on
their own. They are common, mostly not storage, and raising on them would
send a large share of all nodes to a model that leans toward "causal". This
means the `io-timeout-*` scenarios stay unraised unless SMART moves: slow
storage shows up only as probe timeouts, which look the same as CPU or
network trouble. That gap is recorded rather than papered over (see D1).

Matching runs in-process on the raw capture, before redaction. Nothing
leaves the cluster, and the model still receives only `reason.BuildInput`'s
redacted input.

### Cost

The sweep currently fetches only SMART and the Node object for each node. To
evaluate the new rules, it would run the existing `Collect` for every node on
every sweep, then reuse that capture if the node is analysed, so there is no
second fetch. `Collect` is already bounded:

- one field-selected pod list
- event lists
- log reads only for failing containers, capped at 200 lines or 64 KB each

At the default 15-minute schedule this is modest for the small clusters v1
targets. A later optimisation is one cluster-wide pod and event list per
sweep instead of one per node. No new RBAC is needed: pods, events and logs
are already read.

### Scope check

- **v1 SMART-only host signal:** unchanged. These are Kubernetes-layer
  signals, which Tropis already collects. No kernel log, NIC, ECC, systemd
  or BMC collection.
- **Read-only:** unchanged. No new verbs.
- **NPD optional:** unchanged, and strengthened by the fix above.
- **NPD plugin artifact:** unaffected. The new rules are not host rules and
  are not part of the NPD plugin.

### Eval changes

- **New headline metric, end-to-end detection:** positives that were
  raised *and* scored correct, divided by all positives. Report it alongside
  reasoning accuracy and pre-filter recall.
- The report's negative-control section already counts raised non-causal
  scenarios. That is the cost side of the new rules, and it needs watching.
- Threshold and signature changes are result-affecting, like prompt changes,
  and are recorded in the results.

## Decisions needed

**D1. Probe timeouts.** Should repeated liveness or readiness probe
timeouts on a node raise it?
*Recommendation: no, for v1.* They are a weak, non-specific signal, and
raising on them puts a large population of healthy-disk nodes in front of the
model. Record `io-timeout-*` as a known pre-filter gap. Revisit with real
negative-control data from the capture rig.

**D2. The gate.** The gate (≥80% root-cause accuracy on ≥20 injected faults,
<10% false correlation) was "fixed before any result". No real result exists
yet, so it can still be changed honestly, but only now, before capture, and
the change must be recorded.
*Recommendation: gate on end-to-end detection ≥80%*, keeping the
false-correlation bar. That is the number an operator experiences. Gating on
reasoning alone would let a pre-filter that never fires pass.

**D3. Versioning the model-input change.** Removing `triggeredBy` from
model input changes what the model sees, so results before and after are not
comparable.
*Recommendation: bump `PromptVersion` to v2* (the prompt text is unchanged),
and treat any change to the model-input document as a version bump from now
on. Add that to AGENTS.md.

## Plan

1. `fix(reason)!`: keep trigger names, including NPD's, out of model input.
   Extend `TestNPDNeverReachesModelInput` to pass NPD triggers through the
   pre-filter. Bump `PromptVersion` (D3).
2. `feat(prefilter)`: the three Kubernetes rules, with table-driven tests per
   signature and per non-trigger (a crash loop without a storage error must
   not raise). The sweep collects before pre-filtering.
3. `feat(eval)`: end-to-end detection metric, and the gate change if D2 is
   accepted.
4. Re-run the dev eval and the local smoke test, and update AGENTS.md and
   `eval/README.md`.
