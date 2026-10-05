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

Commits follow [Conventional Commits](https://www.conventionalcommits.org/),
because releases are automated from them: release-please reads the messages
to choose the next version and write the changelog. A CI check enforces the
format on every commit in a pull request, and on the PR title. Pull requests
are rebase-merged, so each commit lands on `main` as written: make every
commit a good changelog entry.

```
<type>[(scope)][!]: <short imperative description>
```

| Type | Use for | Version (while 0.x) | Changelog |
|---|---|---|---|
| `feat` | new behaviour | minor | Features |
| `fix` | a bug fix | patch | Bug fixes |
| `perf` | a performance improvement | patch | Performance |
| `security` | a security fix | patch | Security |
| `deps` | dependency updates | patch | Dependencies |
| `revert` | reverting a commit | patch | Reverts |
| `docs`, `refactor`, `test`, `build`, `ci`, `chore` | everything else | no release | hidden |

A `!` after the type, or a `BREAKING CHANGE:` footer, marks a breaking
change, which bumps the minor version while Tropis is 0.x and the major
version after 1.0. Use the scope for the area touched:

```
fix(smart): treat exit status bit 2 as a partial read, not a failure
feat(notify)!: send the full verdict in webhook payloads
```

Use the body to explain why. Sign off every commit (`git commit -s`).

`hack/check-commits.sh <base> <head> [title]` runs the same check locally.

## Licence

Contributions are accepted under Apache 2.0. By submitting a contribution you
confirm you have the right to do so, per the DCO.

## Releasing

Releases are automated with [release-please](https://github.com/googleapis/release-please).

1. Merge pull requests to `main` as usual.
2. When CI passes on `main`, release-please opens — or updates — a release
   pull request titled `chore(main): release X.Y.Z`. It bumps `version` and
   `appVersion` in `deploy/helm/tropis/Chart.yaml` and adds the changelog
   section, both worked out from the commit messages since the last release.
3. **Merging the release pull request publishes the release.** CI runs again,
   release-please creates the tag and GitHub Release, and the publish job
   attaches the artifacts: CLI archives with cosign-signed checksums, the
   multi-arch image at `ghcr.io/00webbo/tropis:X.Y.Z` with SBOM and
   provenance, and the chart at `oci://ghcr.io/00webbo/charts/tropis`, all
   signed. Versions `0.x` are marked pre-release.

Merges containing only `docs`, `refactor`, `test`, `build`, `ci` or `chore` commits
do not open a release pull request. To force a specific version, add a
`Release-As: X.Y.Z` footer to a commit.

The release pull request is opened by GitHub Actions, so no workflows run on
it; that is expected. Its commits are signed off by the Actions bot.

`make dist` and `make chart-package` build the same archives and chart
locally.
