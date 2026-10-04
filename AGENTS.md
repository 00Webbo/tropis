# AGENTS.md

Guidance for coding agents working in this repository. Humans: see
[CONTRIBUTING.md](CONTRIBUTING.md), which this does not replace.

Tropis is a read-only diagnostic agent for bare-metal Kubernetes. It reads
disk SMART data (host layer) and pods, events and logs (Kubernetes layer) for
one node, and asks a model whether one is causing the other. Its
differentiator is a published, reproducible accuracy number, so anything that
could quietly corrupt the evaluation is treated as a defect.

## Commands

```sh
make check        # run before declaring any change done: fmt, vet, tests, generated files, NPD protocol, chart lint
make test         # unit tests only
make generate     # after touching pkg/schema, pkg/k8s/report.go or eval/scenarios
make eval-dev     # replay the synthetic dev corpus with the mock backend
make inject-test  # fault-injection scripts, in a privileged container (Docker)
make kind-e2e     # install on kind and require a verdict (Docker, kind, helm)
```

`make help` lists everything. No cluster, hardware or API key is needed for
`make check`.

## Rules, and what enforces them

Break one of these and the change is wrong, however good it looks. Where a
test guards a rule, do not weaken the test to make a change pass.

| Rule | Why | Enforced by |
|---|---|---|
| Tropis is read-only. The only write is its own `NodeHealthReport`, plus opt-in Node Events (create, `default` namespace only, off by default). Any new write goes in `optInGrants` and SECURITY.md together. | The project's core promise | `deploy/rbac_test.go`; `pkg/k8s` `TestCollectIsReadOnly`; `make kind-e2e` |
| Nothing synthetic in `eval/fixtures/`. | Synthetic fixtures void the published numbers | `fixture.PublishableCorpus`; `tropis eval --publishable` |
| Ground truth never reaches analysis. Only `fixture.LoadLabel*` reads `label.json`, only in the scoring phase. | Otherwise the eval measures recognition of its own test cases | `pkg/fixture` `TestLoaderCannotReachLabels`; `eval/runner` `TestLabelsAreWithheldFromAnalysis` |
| Model input is built only by `reason.BuildInput`, which redacts. | No unredacted log content may leave the process | `pkg/reason` `TestPlantedCredentialsNeverReachModelInput` |
| Provider SDKs are imported only in their backend package (`pkg/reason/anthropic`). | A local backend must be supported exactly as well | `pkg/reason` `TestProviderSDKsStayInTheirPackages` |
| NPD conditions are triggers, never evidence. | NPD collapses signal to booleans | `pkg/reason` `TestNPDNeverReachesModelInput`; `pkg/k8s` `TestNPDConditionsAreSplitFromNode` |
| Malformed model output is an error, never a verdict. `reason.Finalize` never repairs. | A guessed verdict looks exactly like a real one | `pkg/reason` `TestFinalizeRejectsMalformedOutput` |
| `coincidental` and `insufficient_evidence` are real answers. | A reasoning layer that never returns them is broken | eval warnings; the prompt's examples |
| Host signal is SMART only in v1. No kernel log, NIC, ECC, systemd or BMC collection. | Scope: one signal end to end first | review |
| No remediation, provisioning, operator/reconcile loops, dashboards or multi-cluster code. | Out of scope for v1 | review — stop and ask instead |

## Things that are easy to get wrong

- **Generated files.** `docs/schema/verdict.schema.json`, the CRD in
  `deploy/helm/tropis/crds/` and everything in `eval/testdata/` are
  generated. Change the source and run `make generate`; never hand-edit
  them. Tests fail if they drift.
- **The prompt is versioned.** Any change to `pkg/reason/prompts/v1.md` that
  could alter model output needs a new file and a new `PromptVersion`, not
  an edit in place: eval results are only comparable within a version.
- **Scenarios are the capture plan.** `eval/scenarios/scenarios.yaml` drives
  both the real capture run and the dev corpus. A scenario expecting SMART to
  change must declare `hardware: [real-sata]`.
- **`smartctl` exit status is a bitfield.** Non-zero is normal for a failing
  drive. Read the JSON envelope; see the comments in `pkg/host/smart/parse.go`.
- **Unanchored `.gitignore` patterns** once swallowed `cmd/tropis-collector`
  and the Helm chart. Anchor new patterns with `/`, and check a new directory
  with `git check-ignore -v <path>`.
- **Shell scripts run on Linux nodes** and must stay LF (`.gitattributes`
  enforces it) and executable in git (`git update-index --chmod=+x`). Under
  `set -e`, a function whose last command is `[ ... ] && die` returns failure
  when the test is false; end such functions with `return 0`.
- **Injection scripts destroy data.** Only ever run them through
  `make inject-test` or on the capture rig. Never on a development machine.

## Where things are

| Path | What |
|---|---|
| `pkg/schema` | `Verdict`, `Fixture`, `Label`: the source of truth |
| `pkg/host/smart`, `pkg/host/prefilter` | SMART parsing; deterministic rules, also the NPD plugin |
| `pkg/k8s` | Kubernetes collector, collector lookup, `NodeHealthReport` writer and CRD |
| `pkg/redact`, `pkg/reason` | Redaction; model input, prompt, output gate, backends |
| `pkg/pipeline` | Live path, shared with the eval runner |
| `eval/` | Scenarios, dev corpus, runner, report, capture (see `eval/README.md`) |
| `hack/inject`, `hack/npd-plugin` | Fault injection; NPD plugin wrapper |
| `deploy/helm/tropis` | The chart |

Background: `Tropis_Findings_and_Project_Plan.md` explains why;
`Tropis_Build_Brief.md` says what to build, and wins where they disagree.

## Commits

Releases are automated from commit messages, so the format matters:

- **Conventional Commits**: `<type>[(scope)][!]: <description>`. `feat` bumps
  the minor version and `fix` the patch (while 0.x); `docs`, `refactor`,
  `test`, `build`, `ci` and `chore` release nothing. Pick the type for what
  the change does to users, not for the files touched. CI rejects anything
  else; `hack/check-commits.sh <base> <head>` runs the same check locally.
- **Sign off** every commit (`git commit -s`); CI rejects unsigned ones.
- Keep the subject short and imperative, and use the body to explain why.
- Never edit `CHANGELOG.md` or the version in `Chart.yaml` by hand:
  release-please owns both. See CONTRIBUTING.md, Releasing.
