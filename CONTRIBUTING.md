# Contributing to Tropis

Thanks for considering a contribution.

## Developer Certificate of Origin

All commits must be signed off under the [Developer Certificate of
Origin](DCO) (DCO). Add the sign-off with `git commit -s`, which appends:

```
Signed-off-by: Your Name <your.email@example.com>
```

Use your real name and an email you can be reached at. CI rejects unsigned
commits.

## Development setup

Requires Go 1.27 or later and GNU make. No cluster or bare-metal hardware is
needed to build, test, or run the evaluation harness.

```sh
make help     # every task, grouped
make check    # what to run before pushing: format, vet, tests, generated files, NPD protocol, chart lint
```

The integration targets need Docker: `make inject-test` runs the fault
injection scripts in a privileged container, and `make kind-e2e` installs the
chart on a fresh kind cluster and requires a verdict (also kind, kubectl and
helm).

`smartctl` (from `smartmontools`) is needed only to run the collector against
a real disk. The unit tests run against committed sample output and do not
require it.

## Project constraints

These are not style preferences. A change violating one of them will be
rejected regardless of its quality.

1. **Tropis is read-only.** No remediation, no cordoning, no draining, no
   writes to cluster state other than its own `NodeHealthReport` resources.
2. **No synthetic data in `eval/fixtures/`.** Loop devices and fabricated
   SMART output are fine for unit tests and are used extensively. They must
   never enter the evaluation corpus. The published accuracy numbers are the
   project's differentiator, and synthetic fixtures would void them. The
   development corpus under `eval/testdata/` is synthetic and is marked
   non-publishable.
3. **The model backend is an interface.** No provider-specific types outside
   that provider's package. A local backend is supported exactly as well as a
   hosted one.
4. **node-problem-detector is optional.** Everything works identically with
   NPD absent. NPD conditions may trigger the pre-filter; NPD output is never
   evidence for the reasoning layer.
5. **`insufficient_evidence` and `coincidental` are real outputs**, not
   failure modes.
6. **Redaction runs before any model call.**

## Out of scope

Tropis is a diagnostic agent. It is not a provisioning tool, a node lifecycle
manager, a multi-cluster control plane, a dashboard, or an operator with
reconciliation loops. If a change seems to need one of those, please open an
issue to discuss it before writing code.

## Tests

- Rules and parsers are table-driven tests against committed samples.
- Anything touching redaction needs a test proving the secret does not survive.
- Changes to `pkg/schema` need a round-trip test.

## Commit messages

Short imperative subject line, prefixed with the area touched:

```
schema: reject non-causal verdicts carrying a root cause
```

## Licence

Contributions are accepted under Apache 2.0. By submitting a contribution you
confirm you have the right to do so, per the DCO.
