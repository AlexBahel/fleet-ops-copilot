# Contributing

## Developer Certificate of Origin

Every commit must be signed off under the
[Developer Certificate of Origin 1.1](https://developercertificate.org/):

```bash
git commit -s
```

The `Signed-off-by:` line states that the contributor has the right to submit the
change under the project's license (Apache-2.0). Commits without it are not merged.

## Before opening a pull request

```bash
make ci
```

It must pass. It builds, vets, lints, runs the pinned ShellCheck, the unit tests, the envtest tests
and the fixture evals; the CI `go` job runs it, and the `hygiene` job also checks the Apache-2.0
header of every Go file and the `Signed-off-by:` line of every commit. Changes to the chart also need
`make chart-check`. `make help` lists every target.

`make ci` changes no tracked file: the fixture evals write their results to
`evals/results/tmp/selftest/`, which is ignored, and CI fails when `git status` is not clean after
`make ci`. The committed harness self-test, `evals/results/fixtures/results.json` and its
`traces.jsonl`, changes only through `make record-selftest`, committed on its own. `fleetops-eval`
run by hand without `-out` also writes to `evals/results/tmp/selftest/`.

The fixture evals (`make eval-fixtures`) hold the run to `evals/baseline.json` (per category:
scenarios, runs, passing runs). They fail when a category passes fewer runs than recorded or when
the scenario count or a category's counts change (D58). A change that adds, removes or moves a
scenario runs `make eval-baseline` and commits the rewritten `evals/baseline.json` in the same
commit; the scenario count is 51 on eval surface v7 ([`docs/design/evaluation.md`](docs/design/evaluation.md#42-scenarios) §4.2, D101). CI also prints a
warning annotation for every category a change lowers in that file against its base commit (D62); `make baseline-lowered
BASE=<revision>` shows the same locally. Lowering a category is allowed, and the warning asks the
reviewer to look at it.

## Rules for changes

- Read `docs/DESIGN.md` first. A change of course is recorded as a new row in its
  decision log, with the reason.
- The copilot is a client of Swarmada's public API. Do not copy, vendor, or modify
  Swarmada source; import `github.com/swarmada/swarmada/api/v1` as a module.
- The forbidden operations in [`docs/design/tools-and-policy.md`](docs/design/tools-and-policy.md#31-mcp-server-fleetops-mcp-go) §3.1 (clear a stop, release a robot,
  delete, approve, edit existing work) must stay refused in code, in RBAC, and in the
  admission policies. A change that weakens any of the three needs a test showing the
  other two still refuse.
- Secrets come only from environment variables backed by Kubernetes Secrets and must
  never appear in logs, traces, audit lines, or Events.
- Every number in a report cites the file that holds its evidence. Numbers are
  measured, not estimated.
- Wording in public files is neutral and claims nothing the code does not do.

## Security

Report a vulnerability privately, as [`SECURITY.md`](SECURITY.md) describes, not in a public issue.
