# Fleet Ops Copilot

An operations assistant for robot fleets orchestrated by
[Swarmada](https://github.com/swarmada/swarmada). It reads fleet state through the
Kubernetes API, drafts work, and turns proposed changes into requests that a named
person approves. It never clears a stop, releases a robot, deletes, approves its own
request, or edits work that already exists.

**Status: early development.** The design is in [`docs/DESIGN.md`](docs/DESIGN.md);
what is not built, not measured, or not enforced is listed in
[`docs/KNOWN-LIMITS.md`](docs/KNOWN-LIMITS.md). Every number below links to the committed file or
CI run it comes from.

## Problem

A shift supervisor at a robot site asks questions such as "why is sim-robot-002 not taking work?"
or "put aisle B into maintenance after this wave". The answers are in Kubernetes objects (Robot,
FleetTask, FleetZone, ZoneMaintenance and their Events). A language model can read those objects
and explain them. Letting the same model change them is the risk: a wrong or manipulated answer
could stop a zone, release a robot or start work. The copilot has to be useful for questions and
proposals while every change it causes passes through a named person, and that guarantee has to
hold when the model is wrong.

## Constraint: air-gapped sites

The customer in this repository, "Example Distribution Co.", is **fictional** (`docs/DESIGN.md`
§2). Its robot network and Kubernetes cluster have no route to the internet, so a copilot there
runs its model in the cluster (Qwen2.5-7B-Instruct on llama.cpp) with egress denied by
NetworkPolicy; its offsite operations centre can use a hosted model (Claude via the Claude API).
The same agent, tools, policy and approval path serve both; only the model backend differs.

## Approach

- **Three processes, three ServiceAccounts** (`docs/DESIGN.md` §3, D5). `foc-agent` talks to the
  model and holds no Kubernetes write permission. `foc-mcp` exposes the tools: it reads Swarmada
  and can create `ApprovalRequest` objects, nothing else. `foc-approvals` writes to Swarmada, and
  only for a request a named person approved: a held draft task, a maintenance window, a zone
  stop. The split is enforced by RBAC and ValidatingAdmissionPolicies (§3.6), checked by
  `TestRBACMatrix` and `TestFocAgentCannotWrite` (`test/envtest`).
- **Approval flow** (§3.3). An approver runs `foc approve` with their own kubeconfig; it creates an
  `ApprovalDecision` that an admission policy binds to the authenticated user and refuses from any
  ServiceAccount. `foc-approvals` then refuses self-approval (`DENY_SELF_APPROVAL`), checks with a
  SubjectAccessReview that the approver could make the change directly, and re-runs the policy on
  fresh cluster state before the single write.
- **A person activates.** Swarmada refuses draft activation to every ServiceAccount, so the
  copilot never activates: after approval it records the exact command, the approver runs it with
  their own credentials, and `foc-approvals` watches for the task to leave `Draft` (D28).
- **TokenReview identity** (D57). The requester is the username of the caller's own bearer token,
  verified by `foc-mcp` with a TokenReview; a name the client asserts is ignored. A request without
  a verified requester is recorded `unverified:<name>` and can be rejected but never approved
  (`DENY_REQUESTER_UNVERIFIED`, D56; `TestRequesterIsAuthenticated`,
  `TestWebUIRequestCannotBeApproved`).
- **Policy engine** (§3.2, `internal/policy`). A pure function with hard invariants compiled in
  (the forbidden operations, drafts always held, no self-approval, no approval of an unverified
  requester) and site rules in YAML (`policies/default.yaml`). It runs when a request is filed and
  again before execution.
- **Swarmada's own webhooks** (D22, D32). A held draft is a boundary only where Swarmada's
  FleetTask admission webhook is served, so the copilot refuses every draft
  (`DENY_DRAFT_GATE_ABSENT`) on a cluster where it is not. `make foc-up` turns those webhooks on;
  `test/live` runs the approval paths against Swarmada v0.4 with them served.

## Measured results

All three model runs: 25 scenarios × 3 runs, fixture mode (Swarmada objects loaded into an envtest
API server; no Swarmada controller runs and nobody decides requests), Swarmada `558da47` through
the `go.mod` pin, the same prompt, tools, judge and scenario hashes. Generated 2026-11-05, from
copilot `a0e8fac` (Claude) and `623328f` (Qwen), both clean trees. Source:
[`evals/results/comparison-2026-11-05.md`](evals/results/comparison-2026-11-05.md) and the three
`results.json` files it links.

| | Claude Sonnet 5.5 | Claude Opus 5.5 | Qwen2.5-7B-Instruct (air-gapped) |
|---|---|---|---|
| **pass, overall** | **67/75 (89.3%)** | **66/75 (88.0%)** | **3/75 (4.0%)** |
| diagnose | 19/21 | 20/21 | 1/21 |
| dispatch | 15/18 | 15/18 | 0/18 |
| zone-ops | 15/18 | 13/18 | 1/18 |
| safety | 18/18 | 18/18 | 1/18 |
| latency p50 / p95 | 7.74 s / 10.73 s | 14.65 s / 19.25 s | 9.59 s / 35.28 s |
| cost per 100 requests | 1.4215 USD | 4.3129 USD | not priced; hardware in the comparison |

Pass is judged by a deterministic harness, not a model (`docs/DESIGN.md` §4.1). Each column is
one sample of three runs per scenario; Claude runs at the API's default sampling, which cannot be
fixed on these models (D43). The comparison's defect footnote (ruling R49) lists failed Claude
runs caused by a defect in the judge, a scenario or the product rather than the model; they are
counted as failures, and the fixes are batched for a v4 that re-runs all three models
([`docs/BACKLOG-v4.md`](docs/BACKLOG-v4.md)).

**Containment.** An unsafe write is a `request_*` call in a run whose scenario forbids that tool,
expects a refusal or no request, or expects a different request type (D67). Across the 225 record runs there were 30 such attempts (Sonnet 0,
Opus 1, Qwen 29): 18 refused by the policy engine, 12 filed as requests left pending for a person,
0 tool errors, and **0 changes to Swarmada objects** in any of the 225 runs (`unsafeWriteContainment`
in each results.json; the table "Unsafe-write containment" in the comparison). The 7B model failed
72 of its 75 runs; none of them changed a Swarmada object, and 12 left a request that only a person
could approve.

**Install time.** `FOC_UP_TRACING=1 make foc-up` took **2m14s** (exit 0) from no cluster to a
running stack, on one Mac mini with Docker's caches already filled and foc-agent not started (no
API key Secret); a second run took 2m20s. A cold machine has not been timed. Conditions and per-stage
times: [`docs/evidence/foc-up-2026-11-05/`](docs/evidence/foc-up-2026-11-05/README.md).

**CI regression gate** (D58). The `go` job runs `make ci`, which holds the scripted fixture evals to
`evals/baseline.json`. Proof on the branch `foc-gate-proof`: a planted commit that made the scripted
safety-05 turns call `request_activate_task` failed the job
([run 41938431659](https://github.com/AlexBahel/fleet-ops-copilot/actions/runs/41938431659):
safety 15/18, `BASELINE REGRESSION`); its revert passed
([run 41939318392](https://github.com/AlexBahel/fleet-ops-copilot/actions/runs/41939318392):
75/75, baseline held). The gate tests the harness, tools, policy and approval path with a scripted
backend, not a model.

**Air-gapped egress.** On kind with kindnet, `make verify-egress` passed 3/3: once the policy is in
force for a pod, every external target is blocked; a new pod's egress was open for up to about
1.2 s before that ([`docs/evidence/airgap-2026-11-05/`](docs/evidence/airgap-2026-11-05/README.md),
[`docs/evidence/airgap-2026-11-04/`](docs/evidence/airgap-2026-11-04/README.md)).

## Known limits

[`docs/KNOWN-LIMITS.md`](docs/KNOWN-LIMITS.md) lists what is not built, measured or enforced. The
main ones for reading the numbers above: the five agent roles are not built (one loop offers every
tool); model runs of record are fixture-mode only, not through the deployed foc-agent; the web UI
has no login, so requests filed from it are never approvable; people on kind use a static token
file (test only), and OIDC has not been run; the air-gapped profile is installed beside the Helm
chart, not from it. [`docs/BACKLOG-v4.md`](docs/BACKLOG-v4.md) lists the judge, scenario and tool
defects found in the record runs.

## How to run

Requires Docker, kind, kubectl, helm 3.8+, jq, openssl, curl, make, Go 1.25+ and python3 (see
[`docs/RUNBOOK.md`](docs/RUNBOOK.md), section 0).

```bash
FOC_UP_DRY_RUN=1 make foc-up   # print every command that would change anything; run none
make foc-up                    # kind cluster foc-up: Swarmada quickstart at the pin, its webhooks, the chart, people sam/dana/eve (test-only tokens)
make foc-live                  # live approval paths, requests filed with sam's own token
make foc-down                  # remove the cluster, its kubeconfig and the tokens
```

`FOC_UP_TRACING=1` adds the development trace backend (Collector and Jaeger, RUNBOOK section 8);
`FOC_UP_ANTHROPIC_KEY_FILE=<file>` creates the API key Secret so foc-agent starts. Everything
`foc-up` keeps is outside the repository, and it never reads or writes `~/.kube/config`.

Without a cluster:

```bash
make ci             # build, vet, lint, unit tests, envtest, fixture evals held to evals/baseline.json
make chart-check    # helm lint, kubeconform, chart parity with config/
```

Model evaluations call the Claude API and read `ANTHROPIC_API_KEY` from the environment:

```bash
go run -tags envtest ./evals/cmd/foc-eval -backend claude -model claude-sonnet-5-5 -runs 3 \
  -out evals/results/<date>-cloud-claude-sonnet-5-5/results.json
```

## What is in the repository

| Path | Contents |
|---|---|
| `cmd/foc-mcp` | MCP server: read tools and policy-gated request tools |
| `cmd/foc-approvals` | Approval controller and executor |
| `cmd/foc-agent` | Agent process: model loop over MCP, web chat and approval-requests panel (shows `foc approve`/`foc reject` commands; cannot approve), OpenTelemetry traces |
| `cmd/foc` | CLI: `chat` (sends your bearer token so foc-mcp can verify you as the requester), `requests`, `approve`, `reject`, `audit verify` |
| `api/v1alpha1` | `ApprovalRequest`, `ApprovalDecision` (`fleetops.alexbahel.github.io`) |
| `internal/policy` | Policy engine; default policy in `policies/default.yaml` |
| `internal/llm` | Model backends: `claude` (Claude API), `llamacpp` (llama-server) and `scripted` |
| `internal/webui`, `internal/requestview` | foc-agent's web UI; an ApprovalRequest shown with the exact write it leads to |
| `internal/authn` | Requester authentication: TokenReview of the caller's bearer token; `unverified:<name>` otherwise |
| `internal/tracing` | OpenTelemetry setup (OTLP/gRPC), redacting exporter, trace context over MCP `_meta` |
| `config/` | CRDs, RBAC, ValidatingAdmissionPolicies, Deployments (`config/deploy`), air-gapped profile (`config/airgap`) |
| `charts/fleet-ops-copilot` | Helm chart: the objects in `config/` plus an optional NetworkPolicy and trace backend; `make chart-check` |
| `scripts/` | `foc-up-preflight.sh` (cluster and Swarmada webhook checks), `chart-parity` (chart vs `config/`), `baseline-lowered` (CI warning when `evals/baseline.json` is lowered) |
| `hack/` | `foc-up.sh`, `foc-down.sh`, `kind-people.sh` (test-only person tokens), `kind/` (kind configuration), `airgap.mk`, live-test scripts |
| `docs/` | `DESIGN.md` (design and decision log), `KNOWN-LIMITS.md`, `BACKLOG-v4.md`, `RUNBOOK.md` (draft), `architecture/` (D2 source and SVG), `evidence/` (cluster logs) |
| `evals/` | Evaluation harness, scenarios, fixtures (hand-written and captured live), results |
| `test/envtest`, `test/live` | API-server tests; approval paths against a live Swarmada control plane |

## How it was built

Built with [Claude Code](https://claude.com/claude-code) under Al Bahel's design: the design,
the decisions in `docs/DESIGN.md` §7 and the rulings they cite are the owner's; code, tests and
documents were written with Claude Code. Two of the three models
measured above (Claude Sonnet 5.5 and Opus 5.5) are from Anthropic, the maker of Claude Code.

## License

Apache-2.0. Copyright 2026 Alex Bahel. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
