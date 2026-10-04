# Fixture format

A fixture is one captured scenario: everything the collectors saw on one node
at one moment, stored so the entire pipeline can be replayed offline with no
hardware involved.

This matters more than it sounds. Prompt iteration is where most of the
project's time goes, and against a fixture corpus it is a tight loop rather
than something gated on physically degrading a disk each cycle. Regression
testing across prompt and model changes becomes free.

## Layout

One directory per scenario variant:

```
eval/fixtures/
  disk-realloc-growth-01-npd-absent/
    fixture.json      # the capture — no ground truth
    label.json        # ground truth — withheld during analysis
  disk-realloc-growth-01-npd-present/
    fixture.json
    label.json
```

Every scenario is captured **twice**, once with node-problem-detector
installed and once without. NPD is optional enrichment, so the accuracy
numbers have to show whether they depend on it. This is cheap to build in now
and expensive to retrofit once the corpus exists.

## Why the label is a separate file

`fixture.json` and `label.json` are separate files loaded by separate
functions in separate source files, and that is the structural basis of the
blinding claim:

| | Reads `fixture.json` | Reads `label.json` |
|---|---|---|
| `fixture.LoadFixture` | yes | **no — cannot** |
| `fixture.LoadLabel` | no | yes |

`LoadFixture` returns a `schema.Fixture`, which has no field anywhere in it
that carries ground truth. The reasoning layer is handed that value and
therefore has nothing to leak, regardless of how carelessly anything
downstream is wired.

Three tests enforce this rather than trusting the convention:

- `TestLoadFixtureWithholdsLabel` serialises a loaded fixture and asserts no
  label content appears in it, with a label file present on disk.
- `TestLoaderCannotReachLabels` parses `load.go` and fails if it so much as
  references `schema.Label` or `LoadLabel`.
- `TestFixtureTypeCarriesNoGroundTruth` fails if the `Fixture` type grows a
  field named `relationship`, `faultType`, `expected`, or similar.

Without this, the eval quietly measures the agent's ability to recognise its
own test cases.

`LoadFixture` also rejects unknown fields, so ground truth pasted into
`fixture.json` by hand is a load error rather than a contaminated run.

## `fixture.json`

| Field | Meaning |
|---|---|
| `version` | Fixture format version (`v1alpha1`) |
| `scenarioId` | Joins the fixture to its label and scenario definition |
| `variant` | `npd-present` or `npd-absent` |
| `capturedAt` | When the capture was taken |
| `node` | Node the capture came from |
| `environment` | Kernel, K8s version, disk model and transport, NPD presence |
| `host.smart` | Raw `smartctl -j` output per device path |
| `kubernetes` | Node object, pods, events, logs, NPD conditions |
| `baseline` | What Prometheus and k8sgpt concluded about the same scenario |
| `synthetic` | `true` for development fixtures — never publishable |

### Captures are stored raw

`host.smart` holds the verbatim bytes of `smartctl -j`, not a parsed struct.
Replay then exercises the real parser, so the corpus catches parser
regressions — which is half of what it is for. A corpus of pre-parsed structs
could not, and the corpus cannot be recaptured without hardware, so it has to
outlive changes to the parsing code.

The same applies to Kubernetes objects, stored as raw API JSON.

### Baseline capture

Every fixture records what a stock Prometheus/Alertmanager setup had firing
and what k8sgpt alone concluded. Uplift over the incumbent is the claim that
matters, and capturing it later would mean re-running every injection.

## `label.json`

| Field | Meaning |
|---|---|
| `scenarioId` | Must match the fixture |
| `relationship` | `causal`, `coincidental`, or `insufficient_evidence` |
| `rootCauseLayer` | `host` or `kubernetes` — set only when causal |
| `faultType` | What was injected, e.g. `disk.reallocated_growth` |
| `injection` | Machine-readable record emitted by the injection script |
| `notes` | What was done and what was expected |

Labels carry the same invariant as verdicts: `rootCauseLayer` is set exactly
when `relationship` is `causal`. Ground truth violating that would score
verdicts against an impossible target.

### Negative controls

Scenarios where a host anomaly and a workload problem are both present but
genuinely unrelated are labelled `coincidental`, and are built **in the same
pass as the positives**, not afterwards. Building all the positives first
invites unconscious tuning against them, and the false-correlation rate then
surfaces far too late to be cheap to fix.

## Synthetic fixtures

Fixtures marked `"synthetic": true` are fabricated for development. They are
loaded normally by `LoadCorpus`, and **rejected** by `PublishableCorpus`,
which report generation uses.

Fabricated SMART output and loop devices are fine for developing collectors
and rules, and are used extensively in unit tests. They must never enter
`eval/fixtures/` or reach a published number: the accuracy figures are the
project's whole differentiator, and synthetic fixtures would void them. The
development corpus lives under `eval/testdata/` and is marked non-publishable.
