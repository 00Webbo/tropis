# Tropis — Bare-Metal Kubernetes Diagnostic Agent

**Findings & Project Plan** · rev 2, 2026-09-20 · Nathan

> *Tropis* (τρόπις, Greek for **keel**) — the structural member below the waterline that everything else is built on, which is precisely the host layer beneath Kubernetes. Follows the transliterated-Greek convention of Kyverno, Cilium and Istio. Name and domains confirmed available — see Decisions.

---

## Summary

Tropis is an open-source diagnostic agent for bare-metal Kubernetes clusters. It reads the host layer (kernel log, SMART, systemd, NIC counters, and out-of-band BMC telemetry) and the Kubernetes layer (pods, events, node conditions) for the same node in a single reasoning pass, and asks whether one is causing the other.

Nothing today does that. Every AI-for-Kubernetes tool reasons one layer above the host; every host-layer tool is deterministic pattern-matching with no reasoning. Bare metal is where that gap bites, because there is no cloud provider silently cordoning and replacing a node with a degrading disk.

**v1 is the agent and nothing else.** It installs with `helm install` onto any conformant cluster, is read-only, and ships with a fault-injection evaluation harness and published accuracy numbers — which is the thing none of the existing tools have.

**Scope decisions already made** (rationale in the sections below):

| Decision | Answer |
|---|---|
| Audience | Open source, seeking adoption — not internal tooling |
| v1 scope | Diagnostic agent only. No provisioning, no remediation, no fleet management |
| Provisioning | Out of the roadmap. Retained as Appendix A as a possible sibling project |
| Model backend | Pluggable. Claude is the default and reference implementation; local backends are first-class |
| Packaging | CLI + CronJob first. Operator only once the diagnostic core is proven |
| Output | `NodeHealthReport` CRD by default, `--json` for scripting. A CRD is not an operator |
| node-problem-detector | Optional enrichment and pre-filter trigger. Never a dependency, never an evidence source |
| First host signal | Disk SMART, alone, until the correlation loop works end to end |
| Licence | Apache 2.0 |
| Test environment | Real bare metal available — fault injection is physical, not simulated |

---

## Problem statement

Bare-metal Kubernetes clusters run without a cloud provider absorbing host-level failures. On EKS, GKE, or AKS, a failing disk, a flaky NIC, or a kernel-level memory error is largely the cloud's problem; nodes get cordoned and replaced automatically. On bare metal, the operator owns the full stack: physical hardware, host OS, systemd, container runtime, kubelet, and the Kubernetes objects on top.

Today's AI-for-Kubernetes tooling only reasons about the top of that stack. A pod in `CrashLoopBackOff` gets explained in terms of pod specs, resource limits, and container logs — never in terms of whether the node underneath it is throwing ECC memory errors, has a degrading SSD, or is dropping packets on a NIC. The diagnosis stops exactly where the interesting bare-metal-specific failures begin.

The operator has to notice the hardware symptom, then manually connect it to the failing pods by cross-referencing `kubectl describe node`, `dmesg`, and a dashboard. Tropis automates that connection with reasoning rather than static rules.

---

## Landscape findings

*Scanned September 2026 via GitHub, CNCF landscape, pkg.go.dev and vendor documentation. This is a point-in-time finding, not a permanent claim.*

**AI-powered, but Kubernetes-object-only (blind to the host):**

- **k8sgpt** — CNCF Sandbox, the reference diagnostic tool. Scans pods/services/nodes via the K8s API, sends context to a pluggable LLM backend, explains failures in plain English. Read-only. Its multi-backend design is a large part of why it got adoption, and is worth copying.
- **kagent** — LLM-agnostic agents running in-cluster via MCP and Agent2Agent. Can diagnose multi-hop issues autonomously.
- **HolmesGPT (Robusta)** — investigates alerts by correlating logs, events, and metrics.
- **Metoro** — commercial, eBPF telemetry with automatic root-cause analysis.
- **k8s4claw / claw4k8s (Prismer-AI)** — young (~9 stars), Claude-compatible operator. Deterministic rules for known failure modes, LLM escalation with Slack approval for novel ones. Closest analogue in shape, but still K8s-object-layer only. Licence not detected on pkg.go.dev; verify at the repo before referencing, and do not depend on it either way.

**Host-layer, rule-based (no reasoning):**

- **node-problem-detector (NPD)** — official Kubernetes SIG project. DaemonSet watching kernel logs, systemd units, and runtime health, surfaced as Node conditions. Deterministic. **NPD is a trigger for Tropis, not a data source for it** — see Architecture. Its conditions are a useful cheap pre-filter where NPD is already installed; its output is not a substitute for reading the raw signals.
- **node-doctor** — DaemonSet checking CPU/memory/disk, network, and K8s component health, with rule-based auto-remediation and circuit breakers. Deterministic.
- **Netdata** — the closest non-LLM competitor. Per-second host and container metrics with ML-based anomaly detection spanning both. It flags *that* something is anomalous across layers; it does not reason about *why*, and it has no access to SMART narrative detail, kernel log text, or BMC state.

**eBPF observability** — Cilium Tetragon, Pixie. Rich kernel-level visibility, no diagnostic reasoning, no hardware-health signal.

**The actual incumbent** is not any of the above. It is Prometheus + node_exporter + Alertmanager plus an experienced operator. Tropis has to be better than a well-tuned alert rule, which it is only for: novel failure modes nobody has written a rule for, compound failures where two layers interact, and ambiguous evidence requiring judgement about whether a correlation is causal. Say this plainly in the README rather than pretending alert rules don't work.

**The finding:** every AI-reasoning tool operates one layer above the host; every host-layer tool is rule-based. Nothing looks at a node's SMART/kernel/BMC state *and* its pods and events in the same reasoning pass and asks whether one is causing the other. Managed cloud Kubernetes hides the host layer almost entirely, so cloud-first tooling never had structural reason to see this class of failure — which is what makes it a genuine white space rather than a k8sgpt reimplementation.

---

## Architecture

### 1. Host-layer collector

DaemonSet, one pod per node. **v1 collects SMART only** (see First Host Signal). Designed for, but not initially shipping, the rest:

- SMART data via `smartctl` — disk health, reallocated sectors, predictive failure *(v1)*
- Kernel ring buffer (`dmesg`) — hardware and driver errors
- systemd unit status for kubelet, containerd/CRI-O and host-critical services
- NIC error counters (`ethtool -S`, `/sys/class/net/*/statistics`)
- node_exporter/Prometheus metrics where already present

**Out-of-band BMC telemetry via Redfish** is a distinct and underrated input: PSU state, fan and thermal data, chassis intrusion, and predictive drive failure, read from iDRAC/iLO/Redfish rather than from the OS. It frequently surfaces faults before the OS sees anything, and remains available when the node is unresponsive. No AI-for-K8s tool touches it, and it is available *only* on bare metal — it strengthens the "cloud tooling structurally cannot see this" argument more than any in-band signal does. Schedule it immediately after SMART.

### 2. Kubernetes-layer collector

Pod status, events, logs (current and previous), resource requests/limits, node conditions, and NPD-reported conditions where present. Read-only RBAC. Worth reading k8sgpt's analyzer code directly rather than building the gathering logic from scratch.

### 2a. Relationship to node-problem-detector

**NPD is optional enrichment, never a dependency and never a source of evidence.** Tropis reads raw signals itself and works identically on a cluster with no NPD installed.

The reason is lossiness. NPD collapses rich host signal into binary Node conditions: a disk throwing reallocated sectors, remapping under load, with a rising pending-sector count becomes one boolean. That is lossy in exactly the dimension the reasoning layer needs, because the narrative detail *is* the evidence. Reasoning over NPD's conditions would mean reasoning over someone else's summary of the evidence rather than the evidence.

So where NPD exists, its conditions feed the deterministic pre-filter as an additional cheap "look at this node" signal. The reasoning pass then reads raw SMART and kernel data directly, as it would anyway.

There is no "reduced mode" that runs on NPD conditions alone. The host collector is required. A mode producing thin verdicts from collapsed booleans would undercut the accuracy claim that is the whole differentiator, and lowering the install barrier is not worth that.

**Upstream contribution: write the pre-filter as an NPD custom plugin, contribute it after Phase 4.**

NPD's CustomPluginMonitor invokes arbitrary scripts in any language, conforming to a protocol of exit codes plus stdout. It ships with essentially one built-in example (NTPProblem), and there is no SMART plugin upstream. That is a gap Tropis is well placed to fill, and filling it costs almost nothing: a shell script and a JSON config, no Go, no changes to NPD itself.

The practical move is to **write the Phase 1 deterministic pre-filter as an NPD-protocol-conformant script from the start**. The pre-filter is needed regardless, and conforming to the protocol is free — exit code plus a short stdout message is a trivial constraint on rules that are boolean anyway. That gives one artifact serving both purposes, with no dependency created in either direction: Tropis calls the script directly, and NPD users can call the same script.

**Open the upstream PR after Phase 4, not before.** Kubernetes review cycles are slow, and the conversation goes very differently when you arrive with published accuracy numbers and a working project rather than a script and an intention. Nothing is lost by waiting, because the script exists either way.

Note that the protocol itself reinforces the argument above: `max_output_length` in NPD's own example config is 80 characters. Whatever goes upstream can only ever be a trigger.

### 3. Declared intent (optional input)

The agent reasons better when it knows what a node was *supposed* to be. It reads, in priority order: node labels and annotations, then an optional ConfigMap, then nothing. **It must produce useful output on a cluster that has told it nothing** — declared intent sharpens reasoning, it is not a prerequisite. Schema covers role, resource profile, and reserved capacity.

### 4. Triage pipeline

Two stages, by design, not as an optimisation:

1. **Deterministic pre-filter** — threshold and delta rules over collected signals, plus NPD conditions where available. Cheap, runs every sweep, decides which nodes are candidates.
2. **Reasoning pass** — only on candidates. A lightweight model for sweeps, escalating to a stronger one when a candidate anomaly is confirmed.

Sending every node's host state to a model on every sweep is both expensive and actively harmful: SMART and kernel logs are noisy, and a model handed two data streams and asked "is one causing the other" is heavily biased toward yes. The pre-filter exists to keep the reasoning pass rare and the context small.

### 5. Reasoning layer

Per candidate node, joins both collectors' output into one context and asks specifically about host↔pod causality. Requirements:

- **`unrelated` and `insufficient_evidence` are first-class verdicts**, with few-shot negative examples in the prompt. If the agent never returns them, it is not working.
- **Structured JSON output** with schema-constrained decoding.
- **Evidence-linked** — the verdict cites the specific log lines and metric samples it reasoned from, not just a conclusion. Required for human trust, and it makes the eval harness tractable.
- Calibrated confidence, reported and measured against actual accuracy in the eval.

**Backend is pluggable from day one.** Claude is the default and reference implementation; Bedrock, Vertex and local backends (Ollama, vLLM) are supported. This is not optional politeness: a large share of the bare-metal audience runs bare metal precisely because data cannot leave their network, and a single-vendor tool loses them on the first paragraph of the README.

### 6. Output

**A `NodeHealthReport` CRD is the default output**, with `--json` on the CLI for scripted use. One verdict schema, serialised to both.

Shipping a CRD does not mean building an operator: writing a custom resource from a CronJob is a few lines of client-go, and the expensive machinery is reconciliation loops, which a read-only diagnostic tool does not need. What it buys is real — `kubectl get nodehealthreports`, GitOps visibility, RBAC on findings, and other tools able to watch the output.

The cost is API commitment. Once operators build alerting on the schema, changing it hurts. Mark it `v1alpha1` loudly and do not stabilise it until after Phase 4, when the eval will have shown which fields actually carry weight.

- `NodeHealthReport` CRD, verdict in `.status` *(default)*
- `tropis analyze <node> --json` for on-demand investigation and scripting
- Slack/webhook notification for findings

### 7. Node state machine

Shared vocabulary between the agent and whatever manages the cluster:

`provisioned → healthy → suspect → draining → decommissioned`

This is what stops the agent screaming "node unreachable" during planned maintenance, upgrades, and decommissioning, and gives a later remediation layer something unambiguous to act on.

### Guardrails

**Read-only in v1.** No remediation actions of any kind. Remediation is v2 and gated on the eval numbers below.

---

## Security, privilege and data handling

This section is not a footnote. It is the main adoption blocker, and it needs to be visible in the repo, not buried in a design doc.

**Nobody installs a `hostPID`/`hostNetwork` DaemonSet from a repository with twelve stars.** Mitigations, all of which belong in a published SECURITY.md:

- Minimal capabilities rather than blanket host access; read-only mounts of `/proc`, `/sys`, and device nodes; no cluster-wide Kubernetes API permissions for the host collector
- Dedicated node-level ServiceAccount, scoped as tightly as the signal set allows
- Narrow the privilege ask by signal set, not by mode: v1 needs disk device access for SMART and nothing else. Each new signal in Phase 6 should justify the additional access it requires, in the README, at the time it is added
- Signed images, SBOM, reproducible builds
- Published security posture page explaining exactly what is read and why

**Data egress and redaction.** journald and pod logs routinely contain secrets, tokens and PII, and the design ships them to a model. A redaction pass before the model call is mandatory, not a v2 improvement. Document precisely what leaves the cluster under each backend, and make "nothing leaves" a supported configuration via a local backend.

**Repo governance at creation time**, not retrofitted: Apache 2.0 (matches NPD and CNCF norms, keeps a Sandbox path open), DCO, SECURITY.md, CODE_OF_CONDUCT.md, CONTRIBUTING.md. Ten minutes now; genuinely painful once there are external contributors, and their absence reads as "hobby project" to exactly the cautious operators being targeted.

**Positioning: complementary, not competing.** The README should say plainly that Tropis sits *below* k8sgpt rather than replacing it, and interoperates with NPD. Asking for a slot nobody occupies is a far easier pitch than displacing an incumbent.

---

## Evaluation harness

**This is the headline artifact, not an internal gate.** k8sgpt, HolmesGPT, kagent and Netdata all publish zero diagnostic accuracy numbers. Tropis ships a reproducible harness and its results in the README, on real hardware. That is the differentiator that survives someone else adding a SMART collector to k8sgpt.

**Fault injection methods.** v1 needs only the disk rows; the rest are added alongside their signal in Phase 6.

| Fault | Method | Phase |
|---|---|---|
| Disk errors, bad sectors | `dm-dust` / `dm-flakey` overlay, or a genuinely failing spare disk | 0 |
| Disk predictive failure | SMART attribute manipulation on a test device | 0 |
| Disk exhaustion | Controlled fill | 0 |
| Runtime failure | Kill/corrupt containerd | 0 |
| Memory pressure | `stress-ng` | 0 |
| Thermal / PSU events | BMC-observable, via controlled physical conditions | 6 |
| NIC packet loss, errors | `tc netem`, cable/port manipulation | 6 |
| ECC memory errors | `mce-inject`, EINJ (real hardware only) | 6 |

Corpus construction, negative controls, blinding and baseline capture are specified under Phase 0.

**Every scenario is captured both with and without NPD installed.** Since NPD is optional enrichment, the accuracy numbers must show whether they depend on it. Cheap to build in now, expensive to retrofit once the corpus exists.

**Gate for starting v2 remediation work:** correct root cause on ≥80% of injected faults across ≥20 scenarios, with a false-correlation rate under 10% on negative controls. The numbers are arbitrary; having them fixed in advance is the point, because "diagnostic accuracy is proven" otherwise means whatever is convenient on the day. Achievable within the SMART-only scope — twenty disk-fault variations is not a stretch.

---

## Implementation plan

**Phase 0 — Fixture corpus and fault injection**

Build the harness before the agent. Without it there is no way to tell whether any later phase works. Specified in full below, because it is the phase everything else depends on and the only one with a hardware constraint.

**The output is a replayable fixture corpus, not a live test rig.** For each scenario, inject the fault once and capture the raw collector output — full SMART dumps, kernel log slices, pod and event state, timestamps — as a fixture on disk alongside its ground-truth label. Everything after injection then replays offline against fixed data with no hardware involved.

This is the single most consequential decision in the plan. Prompt iteration in Phase 3 is where most of the time goes, and against a fixture corpus it becomes a tight loop rather than something gated on physically degrading a disk each cycle. Regression testing across prompt and model changes becomes free. And every phase after injection is delegable, because none of them touch hardware.

**Scope: SMART only, matching Phase 1.** Do not build scenarios for signals that do not yet exist. Roughly twenty disk-fault scenarios is comfortably achievable within that scope — sudden versus gradual degradation, read versus write errors, reallocated and pending sector growth, I/O timeouts, controller-level failure, filesystem corruption, each in isolation and in combination with plausible pod symptoms. That satisfies the Phase 4 gate without any other signal being collected yet. Expand the corpus alongside Phase 6, one signal at a time.

**Build negative controls in the same pass as positives**, not afterwards. Scenarios with background host noise alongside pod failures that are genuinely unrelated. Building all the positives first invites unconscious tuning against them, and the false-correlation rate then surfaces far too late to be cheap to fix.

**Blind the analysis.** Same runner, same fixture format, randomised order, ground-truth label not visible to the reasoning layer at analysis time. Otherwise the eval quietly measures the agent's ability to recognise its own test cases.

**Baseline capture**: for every scenario, record what a stock Prometheus/Alertmanager setup surfaces and what k8sgpt alone concludes, stored in the same fixture. Uplift over the incumbent is the claim that matters, and capturing it later means re-running every injection.

**Minimum rig**: three nodes (one control plane, two workers) on real hardware, stock kubespray or Talos, one worker with a sacrificial disk, BMC reachable even though Redfish is not used until Phase 6. Most disk injection is software — `dm-dust` and `dm-flakey` overlays, controlled fills, killing containerd — so this does not need to be a lab.

**Write the environment down as an inventory the harness targets**: node names, device paths, BMC endpoints, and what is safe to destroy. That file is the interface between the hardware-bound work and everything delegated.

**Build split**: injection runs require physical access and are not delegable. Scenario schema, injection scripts, runner, fixture store, label handling, baseline capture and reporting are all hardware-free and go with the plan.

**Phase 1 — Host collector, SMART only**

DaemonSet with minimal RBAC, reading SMART per node, exposed over HTTP or written per-node. No AI. Validated against the Phase 0 fixture corpus.

The deterministic SMART rules are written as an NPD-protocol-conformant script (exit code plus short stdout message) so the same artifact serves as both Tropis's pre-filter and a future upstream NPD plugin. Costs nothing now, saves rewriting it later.

**Phase 2 — Kubernetes-layer collector**

Pod/event/log gathering, read-only RBAC, NPD condition ingestion. Reuse k8sgpt analyzer patterns.

**Phase 3 — Triage and reasoning**

Deterministic pre-filter, then the correlation pass. Prompt design is core IP: few-shot examples of real host→pod causality *and* of coincidental co-occurrence. Schema-constrained JSON with evidence citations. Pluggable backend from the first commit.

**Phase 4 — Evaluation and publication**

Run the full scenario set, publish accuracy, false-correlation rate, and baseline comparison. Record the flagship demo: end-to-end diagnosis of a real injected hardware fault correlating to real pod failures. For this category of project, that demo does more for adoption than any amount of architecture prose.

**Phase 5 — Packaging**

Helm chart, CronJob + CLI, `NodeHealthReport` CRD written from the CronJob. Five-minute quickstart from `helm install` to first verdict is an explicit design constraint, and the CRD manifest is the only install surface it adds. Operator (kubebuilder) only if and when the CronJob model demonstrably limits something.

**Phase 6 — Signal expansion**

BMC/Redfish first, then NIC counters, then ECC, then systemd. Each new signal re-runs the full eval; accuracy must not regress.

**Phase 7 — (v2) Guarded remediation**

Only after the Phase 4 gate is met. Deterministic rules for known-safe fixes, model reasoning plus human approval for everything else.

---

## Cost model

Needs a napkin figure in the README before anyone will trust running this at scale. Per-node-per-sweep token cost at each escalation tier, delta-only context so unchanged nodes cost near-nothing, and a worked monthly figure for a 100-node cluster on the default backend. The two-stage triage design exists largely to make this number small.

---

## Decisions

**Nothing is open. Everything below is decided and recorded.**

**Name: Tropis, settled.** Checked 2026-09-20: no project, package or organisation named `tropis` on GitHub, in the Go module namespace, on PyPI or on npm. Nearest neighbours are all distinguishable — *Tropy* (digital-humanities photo software), `trops` (PyPI ops-logging tool), `troposphere`, `electron/trop`. Trademark searches return only *tropic*/*tropik*/*tropico* variants in food, drink and clothing classes, nothing in software. `tropis.io` and `tropis.dev` are both available (confirmed at registrar). Register both, use `.io` as canonical and redirect `.dev` — `.io` is the convention here (cilium.io, kyverno.io), and holding the pair cheaply forecloses a confusable squat later.

`tropis.com` is taken by an unrelated consumer-services company and there is an Indonesian clothing brand at `tropis-studio.com`; neither is a software collision. Avoid anything that shortens to "Tropic" in docs or logo work, since `tropicapp.io` is an established procurement SaaS.

Ruled out along the way: *Carina* (660-star K8s CSI storage plugin), *Keelson* (taken twice, including an AI security scanner), *Plimsoll* (metaphor is about overloading rather than depth of visibility, and it means a gym shoe in British English).

### Recorded so they stop getting relitigated

- **Standalone, not a k8sgpt analyzer plugin.** The host layer is the point, and a plugin architecture would constrain it. Ship optional k8sgpt and NPD interoperability instead, so the project is additive rather than competing for the same slot.
- **CronJob + CLI before an operator.** Much less to build and secure. A CRD ships from v1 regardless — a custom resource is not an operator.
- **`NodeHealthReport` CRD as default output**, `--json` for scripting, `v1alpha1` until after Phase 4.
- **NPD is optional enrichment, not a dependency or an evidence source.** Host collector required; no reduced mode.
- **Contribute a SMART custom plugin upstream to NPD, after Phase 4.** Write the Phase 1 pre-filter to NPD's plugin protocol now so the artifact is reusable; open the PR once there are accuracy numbers behind it.
- **Phase 0 produces a replayable fixture corpus, not a live test rig.** Inject once, capture raw collector output, replay offline thereafter.
- **Build split: injection runs stay with whoever has the hardware; everything else is delegable.** The environment inventory is the interface between the two.
- **Cheap sweep escalating to a stronger model.** Now architecture, not an open question — see Triage pipeline.
- **Apache 2.0.**
- **SMART as the sole first host signal.** Broad-but-shallow first passes produce noisy, low-confidence output. Prove one signal end to end.

**Reference implementations to read before writing code:** k8sgpt (analyzer pattern and multi-backend design, MIT), node-problem-detector (host-signal collection, Apache 2.0), node-doctor (DaemonSet structure — check its licence). Avoid depending on or copying from k8s4claw given unclear licensing.

---

## Appendix A — Provisioning and lifecycle (deferred, likely a sibling project)

Earlier revisions of this plan scoped Ansible-based bare-metal provisioning as Phase 0, with the agent layered on top. **The open-source decision removes it from the roadmap**: nobody changes their cluster bootstrap path in order to try a diagnostic tool, and the install has to be `helm install` onto a cluster that already exists. The "shared inventory as reasoning baseline" argument is also weakest under an adoption goal, because adopters will not have that inventory. What survives of it is the declared-intent input described in the Architecture section, read from labels and annotations that users already have.

The thinking is retained here because it is sound and may become a separate project:

Ansible fits bare-metal bootstrap because it is agentless (SSH-only, nothing pre-installed on a fresh OS image), idempotent, and the de facto standard — kubespray is itself Ansible. A single inventory file is source of truth for which servers exist, their roles, and their intended resource allocation, making allocation deliberate rather than incidental. Playbook stages follow kubespray's shape: host prep (swap, kernel modules, sysctls, DNS), container runtime, pinned kubeadm/kubelet/kubectl with reserved capacity encoded via `KubeletConfiguration` (note: the equivalent kubelet command-line flags are deprecated), control-plane init with CIDRs templated from inventory, node join, CNI and core add-ons, then labels and taints per declared role. Re-running the same playbooks periodically corrects configuration drift.

Node lifecycle — add, remove, replace — uses the same inventory-driven approach: targeted playbook runs, `kubectl drain` as a playbook step rather than a manual command so it is consistent and auditable, and inventory as the record of the cluster's intended shape across hardware churn. A hardware fault confirmed by the agent is a natural trigger to *recommend* planned removal, escalated to a human. The node state machine in the Architecture section is the interface between the two halves.

**CNI note if this is ever built:** Calico was the historical bare-metal default; Cilium has largely taken that position. Pick one with a stated reason rather than inheriting the default.

## Appendix B — Multi-cluster governance (deferred)

If this ever becomes relevant: a single Git repository holds inventory for every managed cluster, CI/CD triggers Ansible on merge, and PR review is the change-control process. This avoids the chicken-and-egg problem by construction — it never needs a Kubernetes cluster to exist in order to bootstrap one — and avoids running a second piece of infrastructure to manage the first. Each cluster's agent pushes findings to a simple central aggregator for cross-cluster visibility.

A hub cluster with CRDs (the Cluster API + Metal3 pattern) is the natural path if fleet size ever justifies live, queryable cluster state, bootstrapped via Cluster API's own bootstrap-and-pivot approach. A dashboard, if wanted, comes last and layers on existing state rather than owning its own store.

Neither is a v1 concern, and neither should influence v1 design.
