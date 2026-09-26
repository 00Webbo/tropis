# Security Policy

This document states plainly what Tropis reads, what leaves your cluster, and
what privileges it asks for. If any of it is unclear or appears inaccurate,
that is a bug — please open an issue.

## Reporting a vulnerability

Report suspected vulnerabilities privately via GitHub's [private security
advisory](https://github.com/nathanwebb/tropis/security/advisories/new)
feature. Please do not open a public issue for a suspected vulnerability.

We aim to acknowledge a report within 5 working days and to provide an
assessment within 15 working days. Tropis is pre-1.0 and maintained on a
best-effort basis; that timeline is a goal, not a contractual commitment.

## Tropis is read-only

Tropis performs no remediation. It does not cordon, drain, evict, delete,
restart, or modify any workload or node. The only object it writes is its own
`NodeHealthReport` custom resource.

This is a design constraint, not a default setting. There is no flag that
enables remediation, because no remediation code exists.

## What the host collector reads

v1 collects a single host signal: **disk SMART data**, via `smartctl -j`.

It does not read kernel logs, journald, NIC counters, ECC/EDAC state, or BMC
telemetry. Those signals are planned for a later phase, and each will justify
the additional access it requires here, in this file, at the time it is added.

### Privileges required

Reading SMART means opening a raw block device and issuing ATA or NVMe
passthrough commands to it. Container runtimes deny a non-privileged
container access to host block devices through the device cgroup, whatever
capabilities it holds, so the collector needs one of two things:

| Mode | What the collector gets | When to use it |
|---|---|---|
| `privileged` (default) | A privileged container | Works on any cluster with nothing else installed |
| `devicePlugin` | Only `SYS_RAWIO`, plus the specific disks a device plugin such as [smarter-device-manager](https://gitlab.com/arm-research/smarter/smarter-device-manager) grants it | Clusters that forbid privileged pods |

Set with `collector.securityMode` in the Helm chart.

**We would rather state the privileged default plainly than bury it.** An
earlier draft of this document said the collector ran unprivileged with
`SYS_RAWIO` alone; that is not possible on standard runtimes, and it was
corrected before the first release. If your policy forbids privileged pods,
use `devicePlugin` mode.

In both modes the collector:

- runs no `hostPID`, `hostNetwork` or `hostIPC`;
- mounts no host filesystem;
- has a read-only root filesystem;
- holds **no** Kubernetes API permissions and mounts no service account token;
- serves one read-only HTTP endpoint (`/v1/smart`) inside the cluster.

If a future signal needs more access than this, it will be documented here
with its justification before it ships, and it will be independently
disableable.

## What the Kubernetes collector reads

Pod status, events, container logs (current and previous), resource requests
and limits, node conditions, and node-problem-detector conditions where
present.

Its RBAC contains **read verbs only** (`get`, `list`, `watch`) — with the
single exception of create, update and patch on `nodehealthreports`, the
agent's own output resource. A test (`deploy/rbac_test.go`) fails the build
if any other write verb, any wildcard, or any access to Secrets or
ConfigMaps appears in the shipped RBAC.

To read each node's SMART data, the analyser asks that node's collector pod
through the API server's pod proxy. That needs `get` on `pods/proxy`, which
is granted by a **namespaced Role in Tropis's own namespace only** — it can
reach the collector pods and no other pod in the cluster.

The collector itself holds **no** Kubernetes API permissions: its
ServiceAccount does not mount a token.

## What leaves the cluster

This depends entirely on the configured model backend, and it is the most
important paragraph in this document.

**With a local backend (Ollama, vLLM) — nothing leaves.** This is a supported,
first-class configuration, not a degraded mode. If your environment forbids
data egress, run Tropis this way.

**With a hosted backend (Anthropic by default)** the following is sent to that
provider's API for the candidate nodes only — never for every node on every
sweep:

- Parsed SMART attributes (numeric values, device model, serial number*)
- Pod names, namespaces, phases, restart counts, and container statuses
- Kubernetes event messages
- Excerpts of container logs
- Node conditions and labels

\* Device serial numbers are redacted by default.

Nothing is sent for nodes the deterministic pre-filter does not raise. The
two-stage triage design exists partly for this reason.

### Redaction

A redaction pass runs on all text **before** it reaches any backend, hosted or
local. It strips secrets, API tokens, bearer credentials, connection strings,
private keys, email addresses, and IP addresses.

Redaction is not configurable-off. It is enforced at the boundary of the
reasoning package rather than at each call site, so a new backend cannot
accidentally bypass it, and it is covered by a non-negotiable test asserting
that planted credentials in a fixture never appear in model input.

Redaction is pattern-based and therefore imperfect. It reduces exposure; it
does not eliminate it. **If your logs must never reach a third party, use a
local backend.** We would rather state that than imply a guarantee we cannot
make.

## Supply chain

Container images are signed, and an SBOM is published with each release.
Verification instructions ship with the first tagged release.

## Supported versions

Tropis is pre-1.0. Security fixes are applied to the most recent release only.
