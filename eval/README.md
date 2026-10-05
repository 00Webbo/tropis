# Evaluation

Tropis ships its accuracy numbers, and this directory is how they are made.

| Path | What |
|---|---|
| `scenarios/scenarios.yaml` | The capture plan: 30 scenarios — 20 injected faults, 6 negative controls, 4 genuinely undecidable — each with its injection, workload and ground truth |
| `scenarios/workloads/` | The workloads run on the target node during a capture |
| `fixtures/` | **The real corpus**, captured from hardware. Empty until the capture run |
| `testdata/` | The synthetic development corpus. **Never publishable** |
| `runner/` | Replays a corpus, withholding labels, and scores it |
| `report/` | Renders results for people |
| `devcorpus/` | Generates `testdata/` from the scenario definitions |
| `capture/` | `tropis capture`: turns a live node, mid-fault, into a fixture and label, refusing any that would misdescribe itself |

## Running

```sh
# Against the development corpus, with the mock backend: exercises the
# harness, measures nothing.
tropis eval --corpus eval/testdata --backend mock

# Against the real corpus, with the default backend. --publishable refuses
# to run if any fixture is synthetic.
tropis eval --corpus eval/fixtures --publishable --inventory rig.yaml
```

Each run writes `results.json` (the record) and `report.md` (a view of it)
under `eval/results/<timestamp>/`, which is not committed by default:
results are published deliberately, not by accident.

## How a run works

1. **Analysis.** Every fixture is loaded without its label — the loader has
   no code path that can read one — and put through the pre-filter and the
   reasoning backend, in a randomised order. The seed is recorded, so any run
   can be reproduced.
2. **Scoring.** Only after the last verdict exists are labels loaded.

A test verifies both halves: no label is read before analysis finishes, and a
marker planted in every label never appears in any model input. Without
this, the eval would quietly measure the agent's ability to recognise its own
test cases.

Every fixture is analysed whether or not the pre-filter would have raised it,
so reasoning accuracy and pre-filter recall are reported separately, and
combined into end-to-end detection.

## What is measured

- **End-to-end detection** on positives: the pre-filter raised the fault
  *and* the verdict names the right relationship and layer. A live sweep
  analyses only what is raised, so this is the share of faults an operator
  is told about correctly, and the number the gate applies to.
- **Root-cause accuracy** on positives: the verdict names the right
  relationship *and* the right layer, whether or not the fault was raised.
  Errors count as wrong.
- **False-correlation rate** on negative controls: how often a coincidental
  scenario is called causal.
- **Confusion matrix** across the three answers, plus errors.
- **Calibration**: reliability bins, Brier score and expected calibration
  error. Confidence is reported, not trusted.
- **Pre-filter recall**: how many positives the deterministic rules would
  have raised at all, and how many non-causal scenarios they raised anyway
  (the cost side: every one is a model call on a node with nothing to
  find).
- All of the above **per NPD variant**. Every scenario is captured with and
  without node-problem-detector; if the columns differ materially, accuracy
  depends on NPD.

The run also warns loudly if the backend never answers `coincidental` or
`insufficient_evidence`. A reasoning layer that never returns them is
broken, whatever it scores on positives.

## The gate

Fixed before any result was known: **end-to-end detection — raised by the
pre-filter and given the correct root cause — on ≥80% of at least 20
injected faults, with a false-correlation rate under 10% on negative
controls.** The gate never passes on a corpus containing synthetic fixtures.

The gate originally applied to root-cause accuracy alone. It was moved to
end-to-end detection on 2026-10-05, before any capture from real hardware
existed, by decision D2 of
[proposal 0001](../docs/proposals/0001-kubernetes-triggers.md). Root-cause
accuracy counts every fixture, raised or not, so a pre-filter that never
fired could still pass; an operator only ever hears about faults that were
raised. The false-correlation bar is unchanged.

## SMART-visible and SMART-clean faults

v1's host signal is SMART alone. Device-mapper injection produces real I/O
errors, hangs and corruption but leaves SMART untouched — as a failing cable,
controller or backplane does. The scenario set keeps both kinds, labelled
truthfully, so the numbers show how Tropis does when its host signal has
nothing to say, rather than hiding it. SMART-visible scenarios require real
SATA hardware (`hardware: [real-sata]`); see `hack/inject/README.md`.
