# Names before and after the rename (R70)

The project's programs, namespace, make targets, settings and scripts were renamed from `foc` to
`fleetops` (and, for the local demo, `demo`). Behaviour did not change. Records made before the
rename keep the old names as they were recorded:

- `evals/results/*-2026-11-05/` (the three model runs of record) and
  `evals/results/comparison-2026-11-05.md` (the v3 comparison);
- `docs/evidence/` (cluster logs and timings, including `docs/evidence/foc-up-2026-11-05/` and
  `docs/evidence/foc-live-2026-11-05/`, whose directory names also stay).

Use this table to read them. Unchanged: the repository name, the Helm chart name
`fleet-ops-copilot`, the API group `fleetops.alexbahel.github.io`, the role bound to people (`fleetops:approver`),
and the policy codes.

## Programs and objects

| Before | Now |
|---|---|
| `foc` (CLI, `cmd/foc`, `bin/foc`) | `fleetops` (`cmd/fleetops`, `bin/fleetops`) |
| `foc-agent`, `foc-mcp`, `foc-approvals` (`cmd/`, image binaries, Deployments, Services, ServiceAccounts, labels, Event sources) | `fleetops-agent`, `fleetops-mcp`, `fleetops-approvals` |
| `foc-eval`, `foc-capture` (`evals/cmd/`) | `fleetops-eval`, `fleetops-capture` |
| namespace `foc-system` | `fleetops-system` |
| Helm release `foc` | `fleetops` |
| Helm helpers `foc.*` (`_helpers.tpl`) | `fleetops.*` |
| ClusterRoles `fleetops:foc-mcp`, `fleetops:foc-mcp-cluster`, `fleetops:foc-agent`, `fleetops:foc-approvals`, `fleetops:foc-approvals-cluster` | `fleetops:fleetops-mcp`, `fleetops:fleetops-mcp-cluster`, `fleetops:fleetops-agent`, `fleetops:fleetops-approvals`, `fleetops:fleetops-approvals-cluster` |
| ClusterRoleBindings `fleetops-foc-mcp`, `fleetops-foc-agent`, `fleetops-foc-approvals`, ... | `fleetops-mcp`, `fleetops-agent`, `fleetops-approvals`, ... |
| admission policies `fleetops-foc-approvals-drafts-only`, `-dry-run-only`, `-zone-stop-only` | `fleetops-approvals-drafts-only`, `-dry-run-only`, `-zone-stop-only` |
| audit files `foc-mcp.jsonl`, `foc-approvals.jsonl`, `foc-agent.jsonl` | `fleetops-mcp.jsonl`, `fleetops-approvals.jsonl`, `fleetops-agent.jsonl` |
| trace services `foc-mcp`, `foc-agent`, `foc-eval-agent`, `foc-eval-mcp`; span `foc.request` and attributes `foc.*` | `fleetops-mcp`, `fleetops-agent`, `fleetops-eval-agent`, `fleetops-eval-mcp`; `fleetops.request`, `fleetops.*` |
| ZoneMaintenance created for request `<r>`: `foc-<r>` | `fleetops-<r>` |
| trace backend and air-gapped objects `foc-otel-collector`, `foc-jaeger`, `foc-egress-probe`, `foc-airgap-egress*`, `foc-llama-models`, ... | `fleetops-otel-collector`, `fleetops-jaeger`, `fleetops-egress-probe`, `fleetops-airgap-egress*`, `fleetops-llama-models`, ... |
| CLI state `$XDG_STATE_HOME/foc/conversations/` | `$XDG_STATE_HOME/fleetops/conversations/` |
| MCP server version `0.0.1-foc0` | `0.0.1` |

## Local demo and tests

| Before | Now |
|---|---|
| `make foc-up`, `make foc-down` | `make demo`, `make demo-down` |
| `make foc-live` | `make test-live` |
| `make test-live` (the Go live tests that `hack/live-approval-path.sh` runs) | `make test-live-go` |
| kind cluster `foc-up`, context `kind-foc-up`, people's contexts `<person>@foc-up` | `fleetops-demo`, `kind-fleetops-demo`, `<person>@fleetops-demo` |
| state directory `~/.local/state/fleet-ops-copilot/foc-up` | `~/.local/state/fleet-ops-copilot/demo` |
| cluster guard: `foc-*` clusters and `kind-foc-*` contexts only | `fleetops-*` clusters and `kind-fleetops-*` contexts only |
| other test clusters `foc-test`, `foc-airgap`, `foc-sw` | `fleetops-test`, `fleetops-airgap`, `fleetops-sw` |
| `hack/foc-up.sh`, `hack/foc-down.sh`, `hack/foc-sim-fleet.sh` | `hack/demo-up.sh`, `hack/demo-down.sh`, `hack/demo-sim-fleet.sh` |
| `hack/lib/foc-guard.sh` (`foc_guard`, `foc_outside_repo`) | `hack/lib/guard.sh` (`guard_cluster`, `guard_outside_repo`) |
| `scripts/foc-up-preflight.sh` | `scripts/demo-preflight.sh` |
| `hack/kind/foc-cluster.yaml.tmpl`, node path `/etc/foc-people` | `hack/kind/demo-cluster.yaml.tmpl`, `/etc/fleetops-people` |
| live-test objects `foc-live-*` | `fleetops-live-*` |

## Settings

| Before | Now |
|---|---|
| `FOC_UP_DIR`, `FOC_UP_DRY_RUN`, `FOC_UP_TRACING`, `FOC_UP_ANTHROPIC_KEY_FILE`, `FOC_UP_SCENARIO`, `FOC_UP_IMAGE`, `FOC_UP_NODE_IMAGE` | `DEMO_DIR`, `DEMO_DRY_RUN`, `DEMO_TRACING`, `DEMO_ANTHROPIC_KEY_FILE`, `DEMO_SCENARIO`, `DEMO_IMAGE`, `DEMO_NODE_IMAGE` |
| every other `FOC_*` (for example `FOC_CLAUDE_MODEL`, `FOC_LLAMACPP_URL`, `FOC_LIVE_PHASE`, `FOC_SIM_CS_PORT`, `FOC_CLUSTER`, `FOC_KUBECONFIG`) | `FLEETOPS_*` (`FLEETOPS_CLAUDE_MODEL`, ...) |
| `FOC_LIVE_FOC` (path of the CLI for the live tests) | `FLEETOPS_LIVE_FLEETOPS` |

## Session labels

Committed files used to cite internal session labels (`FOC-0` ... `FOC-8`). They now cite the
decision in `docs/DESIGN.md` §7 that the work produced, for example D1–D23 for the approved design
and D70–D81 for guided requests. `docs/design/foc8-inventory.md` is now
`docs/design/guided-request-inventory.md`.

## An install made under the old names

Nothing renames objects in a running cluster. A copilot installed as release `foc` in
`foc-system` is removed with `helm uninstall foc --namespace foc-system` and installed again as in
`README.md`; the two CRDs stay installed across an uninstall. A demo stack made with
`make foc-up` is removed with `make foc-down` from a checkout made before the rename: it also stops
the Swarmada quickstart's background processes, which deleting the kind cluster alone leaves
running.
