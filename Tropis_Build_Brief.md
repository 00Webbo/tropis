# Tropis — Build Brief

**For the implementing agent.** Read this with `Tropis_Findings_and_Project_Plan.md` alongside it: that document is *why*, this one is *what to build next*. Where they disagree, this document wins.

---

## What you are building

An open-source diagnostic agent for bare-metal Kubernetes clusters. It reads host-layer signals (starting with disk SMART) and Kubernetes-layer state for the same node, and uses an LLM to decide whether one is causing the other.

Language: **Go**. Module path: `github.com/<owner>/tropis`.

---

## Hard constraints

Violating any of these is a defect, not a design choice.

1. **Read-only.** No remediation, no cordoning, no draining, no writes to cluster state other than the agent's own `NodeHealthReport` resources.
2. **No synthetic data in the eval corpus.** Loop devices and fabricated SMART output are fine for developing and unit-testing collectors and rules. They must never enter `eval/fixtures/`. The published accuracy numbers are the project's whole differentiator and synthetic fixtures would void them.
3. **Model backend is an interface from the first commit.** Claude is the default implementation; a local backend (Ollama or vLLM) must be equally supported. No Anthropic-specific types outside `pkg/reason/anthropic`.
4. **node-problem-detector is optional.** Everything works identically with NPD absent. NPD conditions may feed the pre-filter as an extra trigger. NPD output is never evidence for the reasoning layer.
5. **`insufficient_evidence` and `coincidental` are real outputs.** If a scenario set never produces them, the reasoning layer is broken regardless of its accuracy score.
6. **Redaction runs before any model call.** No unredacted log content leaves the process.
7. **SMART only.** Do not add kernel log, NIC, ECC or systemd collection. Those are Phase 6 and out of scope here.

## Explicitly not in scope

Do not build: Ansible provisioning, node lifecycle operations, multi-cluster or hub-cluster anything, a dashboard, a kubebuilder operator with reconciliation loops, or any remediation path. If a task seems to need one of these, stop and flag it.

---

## Repository layout

```
tropis/
  cmd/
    tropis/                CLI entrypoint
    tropis-collector/      DaemonSet binary
  pkg/
    schema/                verdict + fixture types — SOURCE OF TRUTH
    host/smart/            smartctl invocation and parsing
    host/prefilter/        deterministic rules, NPD-protocol conformant
    k8s/                   Kubernetes-layer collector
    reason/                backend interface
    reason/anthropic/      default backend
    reason/local/          Ollama/vLLM backend
    redact/                secret and PII stripping
  eval/
    runner/                fixture replay, blinding, scoring
    scenarios/             scenario definitions
    fixtures/              captured corpus (real hardware only)
    report/                accuracy report generation
  hack/
    inject/                fault injection scripts
    npd-plugin/            NPD custom plugin wrapper + JSON config
  deploy/helm/
  docs/
```

---

## Task order

Each task is independently testable **without bare-metal hardware**. Do them in order; T1 and T2 gate everything.

### T1 — Verdict schema (`pkg/schema`)

The single most load-bearing artifact. It is simultaneously the CRD `.status`, the `--json` output, and the eval's scoring input. Define once, generate the rest.

```go
type Verdict struct {
    Node          string
    ObservedAt    time.Time
    AgentVersion  string
    Backend       BackendInfo    // provider, model id, prompt version
    Relationship  Relationship   // causal | coincidental | insufficient_evidence
    RootCause     *RootCause     // nil when relationship != causal
    Confidence    float64        // 0..1
    Evidence      []Evidence     // REQUIRED, non-empty
    NextStep      string         // advisory text only, never an instruction to act
    TriggeredBy   []string       // pre-filter rule IDs that raised this node
    InputDigest   string         // hash of the exact input, for reproducibility
}

type RootCause struct {
    Layer       Layer  // host | kubernetes
    Description string
}

type Evidence struct {
    Source      string    // "smartctl", "kubelet-events", ...
    CollectedAt time.Time
    Ref         string    // attribute name, event UID, log offset
    Excerpt     string    // the specific line or value relied on
}
```

**Acceptance:** JSON schema generated from the Go types; round-trip test; a verdict with empty `Evidence` fails validation; `RootCause` non-nil is rejected unless `Relationship == causal`.

### T2 — Fixture schema (`pkg/schema`)

Decide this before capturing anything.

- `fixture.json` — scenario id, environment metadata (kernel, K8s version, disk model, NPD present), raw captures (full `smartctl -j` output, pod state, events, node conditions, NPD conditions if any), baseline captures (Prometheus alerts fired, k8sgpt output)
- `label.json` — **stored as a separate file** so the runner can withhold it. Ground truth: expected relationship, expected root-cause layer, expected fault type, free-text notes

**Acceptance:** a fixture loads with its label withheld; loader has no code path that can read `label.json` during analysis.

### T3 — SMART collection and parsing (`pkg/host/smart`)

`smartctl -j` emits stable JSON across vendors. Invoke, parse, normalise into a typed struct. Handle: SATA and NVMe attribute differences, missing/unsupported devices, `smartctl` absent, permission failures.

**Acceptance:** unit tests against committed `smartctl -j` samples for at least three device types. Runs against the development machine's own disk.

### T4 — Pre-filter rules (`pkg/host/prefilter`)

Deterministic rules over parsed SMART: reallocated sector count and growth rate, pending sectors, uncorrectable errors, SMART overall-health failure, temperature excursion, power-on-hours-relative wear.

Written to **NPD's custom plugin protocol**: exit code plus a short stdout message (NPD's example config caps output at 80 chars). Same artifact serves as Tropis's pre-filter and a future upstream NPD plugin.

**Acceptance:** each rule unit-tested against synthetic attribute sets; `hack/npd-plugin/` contains a working wrapper script and NPD JSON config; script exits 0/1/2 per NPD protocol.

### T5 — Kubernetes collector (`pkg/k8s`)

Pod status, events, current and previous logs, resource requests/limits, node conditions, NPD conditions where present. Read-only RBAC, scoped as tightly as possible.

**Acceptance:** runs against a `kind` cluster; RBAC manifest contains no write verbs; works with NPD absent.

### T6 — Redaction (`pkg/redact`)

Strip secrets, tokens, bearer credentials, connection strings, email addresses and IP addresses from any text before it reaches a backend.

**Acceptance:** table-driven tests; a fixture containing planted credentials produces model input with none of them. This test is non-negotiable.

### T7 — Reasoning backend interface (`pkg/reason`)

```go
type Backend interface {
    Analyze(ctx context.Context, input AnalysisInput) (schema.Verdict, error)
    Describe() schema.BackendInfo
}
```

Implement `anthropic` (default) and `local` (Ollama-compatible). Schema-constrained JSON output. Prompt lives in a versioned file, and its version is recorded in every verdict.

The prompt must include few-shot examples of **both** genuine host→pod causality and coincidental co-occurrence, and must make `insufficient_evidence` an expected answer rather than a failure.

**Acceptance:** a mock backend satisfies the interface; switching backends requires no change outside `pkg/reason`; malformed model output produces an error, never a fabricated verdict.

### T8 — Fault injection scripts (`hack/inject`)

Disk faults only. **Develop against loop devices backed by files** — `dm-dust` and `dm-flakey` work fine over a loop device, so this whole path is buildable on a VM.

Cover: read errors, write errors, growing reallocated sectors, pending sectors, I/O timeouts, filesystem corruption, disk exhaustion, containerd kill.

Each script takes a target device from the environment inventory (below) and declares what it injects in machine-readable form.

**Acceptance:** every script runs end-to-end against a loop device in CI; each emits a structured description of the fault it injected, suitable for `label.json`.

### T9 — Environment inventory

A YAML file describing the capture rig: node names, disk device paths, which devices are safe to destroy, BMC endpoints (unused until Phase 6), K8s version. This file is the interface between hardware-bound work and everything else.

**Acceptance:** schema documented in `docs/`; injection scripts and the eval runner both read it; a committed example file with placeholder values.

### T10 — Eval runner (`eval/runner`)

Replays fixtures through the full pipeline offline. Must:

- Withhold `label.json` from everything downstream of loading
- Randomise scenario order
- Score: root-cause accuracy on positives, false-correlation rate on negatives, confidence calibration
- Run each fixture in both its NPD-present and NPD-absent variants
- Emit a machine-readable results file plus a human-readable report

**Acceptance:** runs against a small synthetic corpus committed to the repo for development purposes (clearly marked as non-publishable); label withholding verified by test.

### T11 — CLI and CRD output

`tropis analyze <node>` with `--json`. `NodeHealthReport` custom resource, `v1alpha1`, written from a CronJob. **This is not an operator** — no controller-runtime, no reconciliation loops. A few lines of client-go writing a resource.

**Acceptance:** CRD manifest applies cleanly; `kubectl get nodehealthreports` shows verdicts; `--json` and `.status` serialise from the same schema type.

### T12 — Packaging

Helm chart: DaemonSet collector, CronJob, CRD, RBAC. Target is five minutes from `helm install` to first verdict.

**Acceptance:** installs on `kind` with a mock backend and produces a verdict.

### T0 — Do this first, it takes ten minutes

Apache 2.0 LICENSE, DCO, `SECURITY.md` (state plainly what the collector reads and what leaves the cluster), `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `README.md` stub positioning Tropis as complementary to k8sgpt and NPD rather than competing.

---

## What is blocked on hardware

Only one thing: **capturing the real eval fixtures**. Everything above is developable on a laptop or VM.

Once the rig exists (three nodes, one sacrificial disk, stock kubespray or Talos), the capture run produces ~20 disk-fault scenarios plus negative controls into `eval/fixtures/`. After that the corpus replays offline forever, and prompt iteration never touches hardware again.

## Definition of done for this handover

T0–T12 complete, all acceptance criteria met, CI green, and the eval runner producing a report against the development corpus. At that point the project is waiting only on the real capture run.
