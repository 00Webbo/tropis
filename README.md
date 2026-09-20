<h1 align="center">Tropis</h1>

<p align="center">
  <em>A diagnostic agent for bare-metal Kubernetes that reads the host layer beneath your pods.</em>
</p>

<p align="center">
  <a href="LICENSE"><img alt="License: Apache 2.0" src="https://img.shields.io/badge/license-Apache%202.0-blue.svg"></a>
  <img alt="Status: pre-alpha" src="https://img.shields.io/badge/status-pre--alpha-orange.svg">
  <img alt="API: v1alpha1" src="https://img.shields.io/badge/API-v1alpha1-lightgrey.svg">
</p>

---

> **Pre-alpha.** Tropis is under active development and has not yet been
> evaluated against its fixture corpus. The accuracy numbers this project
> exists to publish do not exist yet. Do not deploy this to production.

*Tropis* (τρόπις, Greek for **keel**) — the structural member below the
waterline that everything else is built on. Which is precisely the host layer
beneath Kubernetes.

## The problem

A pod is in `CrashLoopBackOff`. Every AI-for-Kubernetes tool will explain it
in terms of pod specs, resource limits, and container logs. None of them will
tell you the disk underneath it has been remapping sectors for three days.

On EKS, GKE, or AKS that gap rarely matters, because a failing disk is the
cloud provider's problem — the node gets cordoned and replaced without you
noticing. On bare metal you own the whole stack, and nothing is watching the
bottom of it on your behalf.

Today's tooling splits cleanly in two, and the split is the gap:

- **AI-reasoning tools** (k8sgpt, HolmesGPT, kagent) reason about Kubernetes
  objects. They cannot see SMART attributes, kernel errors, or BMC state.
- **Host-layer tools** (node-problem-detector, node-doctor) read hardware
  signals, but only as deterministic pattern matching. They report *that* a
  threshold tripped, never *whether it is what broke your workload*.

Nothing reads a node's hardware state **and** its pods in the same reasoning
pass and asks whether one is causing the other. That is the whole of what
Tropis does.

## What it does

```console
$ tropis analyze worker-03

  Node worker-03 — CAUSAL (confidence 0.87)

  Root cause: host — Disk /dev/sda is failing. Reallocated sector count
  rose 312 → 488 over 6 hours with 24 pending sectors, indicating active
  remapping under write load.

  Evidence:
    smartctl      attribute 5 (Reallocated_Sector_Ct)     488, was 312 6h ago
    smartctl      attribute 197 (Current_Pending_Sector)  24
    kubelet       pod postgres-0 — 14 restarts in 6h
    pod-logs      postgres-0: "could not fsync file: Input/output error"

  Next step: advisory — schedule worker-03 for planned drain and disk
  replacement. Tropis has taken no action.
```

Tropis is **read-only**. It does not cordon, drain, evict, or restart
anything. There is no flag that changes this, because no remediation code
exists.

## Where Tropis sits

**Tropis is complementary to the tools you already run, not a replacement for
any of them.**

- **k8sgpt** reasons about the Kubernetes layer and does it well. Tropis sits
  *below* it and answers a different question. Run both.
- **node-problem-detector** is optional enrichment. Where NPD is installed,
  its conditions act as an extra cheap trigger telling Tropis which nodes to
  look at. Tropis reads the raw signals itself and works identically with NPD
  absent — it is never a dependency, and its collapsed boolean conditions are
  never used as evidence.

**The real incumbent is Prometheus + node_exporter + Alertmanager and an
experienced operator, and that setup works.** A well-tuned alert rule on
`Reallocated_Sector_Ct` will catch the case above, and it will do so more
cheaply than any model. Tropis earns its place only for:

- novel failure modes nobody has written a rule for,
- compound failures where two layers interact,
- ambiguous evidence needing a judgement about whether a correlation is causal.

If your failure modes are all known and ruled, you may not need this. We would
rather say so here than have you find out after installing it.

## Accuracy

Every tool in this space publishes zero diagnostic accuracy numbers. Tropis
ships a reproducible fault-injection evaluation harness and its results.

That harness is the headline artifact, not an internal gate. It replays a
corpus of real faults captured from real hardware, with ground-truth labels
withheld from the agent at analysis time, in randomised order, each scenario
run both with and without NPD present.

**Published numbers require the capture run on real hardware, which has not
happened yet.** This section will carry the results table when it does. The
bar fixed in advance — before any number was known — is ≥80% correct root
cause across ≥20 scenarios, with a false-correlation rate under 10% on
negative controls.

Negative controls are built in the same pass as positives, deliberately:
`coincidental` and `insufficient_evidence` are first-class verdicts. An agent
that never returns them is broken regardless of what it scores.

## Your data

The full statement is in [SECURITY.md](SECURITY.md). In short:

- **With a local backend (Ollama, vLLM), nothing leaves your cluster.** This
  is first-class, not a degraded mode. A large share of bare-metal operators
  run bare metal precisely because data cannot leave the network.
- With a hosted backend, only candidate nodes raised by the deterministic
  pre-filter are sent — never every node on every sweep.
- Redaction of secrets, credentials, and PII runs before any model call and
  cannot be disabled.
- The host collector asks for `SYS_RAWIO` and read-only block device access.
  It does not request `privileged`, `hostPID`, or `hostNetwork`, and holds no
  Kubernetes API permissions.

## Status

Pre-alpha, building toward a first evaluated release.

| | |
|---|---|
| Host signals | SMART only. Kernel log, NIC, ECC, BMC/Redfish are later phases |
| Output | `NodeHealthReport` CRD (`v1alpha1`) and `tropis analyze --json` |
| Backends | Anthropic (default), Ollama/vLLM-compatible local |
| Packaging | Helm — DaemonSet collector, CronJob, CRD, RBAC |

The `v1alpha1` API is not stable and will change before it is.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Commits require a
[DCO](DCO) sign-off (`git commit -s`).

## Licence

[Apache 2.0](LICENSE).
