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
make vet lint test test-envtest eval-fixtures
```

All five must pass. `make help` lists every target.

`make eval-fixtures` also holds the run to `evals/baseline.json` (per category: scenarios,
runs, passing runs). It fails when a category passes fewer runs than recorded or when the
scenario count or a category's counts change. A change that adds, removes or moves a scenario
runs `make eval-baseline` and commits the rewritten `evals/baseline.json` in the same commit;
the scenario count is 25 (`docs/DESIGN.md` §4.2, D58).

## Rules for changes

- Read `docs/DESIGN.md` first. A change of course is recorded as a new row in its
  decision log, with the reason.
- The copilot is a client of Swarmada's public API. Do not copy, vendor, or modify
  Swarmada source; import `github.com/swarmada/swarmada/api/v1` as a module.
- The forbidden operations in `docs/DESIGN.md` §3.1 (clear a stop, release a robot,
  delete, approve, edit existing work) must stay refused in code, in RBAC, and in the
  admission policies. A change that weakens any of the three needs a test showing the
  other two still refuse.
- Secrets come only from environment variables backed by Kubernetes Secrets and must
  never appear in logs, traces, audit lines, or Events.
- Every number in a report cites the file that holds its evidence. Numbers are
  measured, not estimated.
- Wording in public files is neutral and claims nothing the code does not do.
