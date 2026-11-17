# Fleet Ops Copilot — Measured results

Every number links to the committed file or CI run it comes from. How the runs are defined and
judged: [`design/evaluation.md`](design/evaluation.md). Two of the four models measured (Claude
Sonnet 5.5 and Opus 5.5) are from Anthropic.

## The record: eval surface v6

The model results of record are on eval surface v6 (D97): 50 scenarios × 3 runs per model, guided
requests, judge v6. All four runs were made on 2026-11-08 (UTC) in fixture mode (Swarmada objects
loaded into an envtest API server; no Swarmada controller runs, and a scripted person answers the
questions and the preview), with Swarmada `558da47` through the `go.mod` pin and the same prompt,
tools, judge, catalogue and scenario hashes. Each `results.json` records the copilot commit
`a69f5f1` (clean tree); the record was committed at `687b4fb`, which differs from `a69f5f1` only in
results and documents. Source: [`evals/results/comparison-v6.md`](../evals/results/comparison-v6.md) and
the four `results.json` files it links.

| | Claude Sonnet 5.5 | Claude Opus 5.5 | Qwen2.5-7B-Instruct (air-gapped) | Qwen2.5-14B-Instruct (air-gapped) |
|---|---|---|---|---|
| **pass, overall** | **120/150 (80.0%)** | **127/150 (84.7%)** | **26/150 (17.3%)** | **24/150 (16.0%)** |
| diagnose | 21/21 | 21/21 | 2/21 | 4/21 |
| dispatch | 15/21 | 18/21 | 0/21 | 2/21 |
| zone-ops | 16/21 | 17/21 | 6/21 | 2/21 |
| safety | 15/18 | 17/18 | 2/18 | 2/18 |
| guided | 53/69 | 54/69 | 16/69 | 14/69 |
| regression scenarios (also in the categories above) | 14/24 | 15/24 | 8/24 | 9/24 |
| latency p50 / p95 | 4.22 s / 7.44 s | 6.61 s / 15.23 s | 10.23 s / 124.78 s | 18.06 s / 244.99 s |
| cost per 100 requests | 1.5449 USD | 3.7439 USD | not priced; hardware below | not priced; hardware below |
| results | [results.json](../evals/results/cloud-claude-sonnet-5-5-2026-11-08-v6/results.json), [failures](../evals/results/cloud-claude-sonnet-5-5-2026-11-08-v6/failures.md) | [results.json](../evals/results/cloud-claude-opus-5-5-2026-11-08-v6/results.json), [failures](../evals/results/cloud-claude-opus-5-5-2026-11-08-v6/failures.md) | [results.json](../evals/results/airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v6/results.json), [failures](../evals/results/airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v6/failures.md) | [results.json](../evals/results/airgap-llamacpp-qwen2.5-14b-instruct-2026-11-08-v6/results.json), [failures](../evals/results/airgap-llamacpp-qwen2.5-14b-instruct-2026-11-08-v6/failures.md) |

Pass is judged by a deterministic harness, not a model ([§4.1](design/evaluation.md#41-definitions)). Each column is
one sample of three runs per scenario; Claude runs at the API's default sampling, which cannot be
fixed on these models (D43). The comparison classifies every failed run as model, scenario, judge
or product ([`comparison-v6.md`](../evals/results/comparison-v6.md), "Failure classes", and each
`failures.md`); defects are counted as failures (on v6: 5 scenario runs, dispatch-07 for both Claude
models; no product or judge runs).

**Fixes after the record, not record-run.** The defects the v6 runs found were fixed afterwards on
eval surface v7 (D101): a refusal of an unused field that repeated without limit (D98), a judge
gap for refusal wording (judge v7, D99) and a scenario that counted a correct reading as a failure
(D100), plus regression scenario guided-24. These postdate the record and no model has been run on v7, so the
table above does not reflect them. The v7 scripted self-test, 51 scenarios × 3, passes 153/153
([`evals/results/fixtures/results.json`](../evals/results/fixtures/results.json)) and is held there
by [`evals/baseline.json`](../evals/baseline.json).

## Air-gapped: what a small local model does, and what it cannot do

Both Qwen models ran on one Mac mini (Apple M6, 32 GiB) in the air-gapped kind profile: llama-server
with Q4_K_M weights, an 8192-token context and the egress policy in force (`make verify-egress`
passed before each run). They pass 26/150 (Qwen2.5-7B) and 24/150 (Qwen2.5-14B), at a p95 latency of
124.78 s and 244.99 s; llama-server's peak working set was 5.13 GB and 10.50 GB
([`comparison-v6.md`](../evals/results/comparison-v6.md), "Latency, tokens, cost, hardware"; the 14B
files and their source:
[`model.md`](../evals/results/airgap-llamacpp-qwen2.5-14b-instruct-2026-11-08-v6/model.md)). The larger
model did not pass more. The comparison lists their failure patterns (wrong extraction, required
reads not made, text in other languages).

A weak model still cannot change the fleet: it is offered no write tool, every request goes through
the rule engine's preview, a person's confirmation and a second person's approval, and the policy
re-runs before the one write. In the 300 v6 runs of the two Qwen models, 3 requests the scenario did
not expect were filed after the scripted person confirmed their preview (Qwen2.5-7B, zone-03), and
all 3 were left pending for a person; no Swarmada object was changed by the model.

**Swarmada objects changed by the model.** In the **600** v6 runs (four results.json files) the
model changed **0 Swarmada objects** (`unsafeWriteContainment.clusterChanges` is 0 in each). The
only Swarmada changes in those runs, 22 (`clusterChangesAllRuns`: 6, 6, 5, 5), come from the
harness's own execution step in the two scenarios that test execution (guided-01, guided-11): it
approves the filed request as a fixture person and runs the executor once
([`comparison-v6.md`](../evals/results/comparison-v6.md), "Containment").

## Other measurements

**Install time, cloud profile.** `DEMO_TRACING=1 DEMO_ANTHROPIC_KEY_FILE=<key file> make demo` (the
cloud profile then; since D103 it needs `DEMO_MODEL=cloud`) took **2m47s**
(exit 0) from no cluster to a running stack with fleetops-agent started, on one Mac mini
(2026-11-09 UTC; this run waits until every simulated robot is ready for work). Two runs on
2026-11-08 took 2m29s and 2m19s. A cold machine (no Docker images or build cache) has not been
timed. Conditions and per-stage times:
[`docs/evidence/live-2026-11-08/`](evidence/live-2026-11-08/README.md).

**Live check through the deployed fleetops-agent, end to end** (2026-11-08, Sonnet 5.5, not a pass
rate). On a `make demo` stack, sam asked in a conversation: "Send sim-robot-001 to staging-shelf-a."
The model read the fleet and proposed the task, and the engine checked it. The preview came back with
no question and its `Checked:` block. Confirmed with "1", it was filed without a model call. Then:

1. dana approved it, and fleetops-approvals re-checked and dry-ran the draft through Swarmada's
   admission, then created it.
2. sam asked in the same conversation to activate the draft, and confirmed.
3. dana approved that request and ran `fleetops activate`, which recorded dana's Activation decision.
4. The FleetTask **Succeeded** nine seconds later.

Both model messages cost 0.0379 USD at the rates the v6 Sonnet results record. Every decision names
dana, and fleetops-approvals' audit file (chain intact) and Events name who approved and who
activated. An earlier run that day filed the same kind of request and passed all 11 `make test-live`
approval-path subtests. Transcripts, requests, decisions, task status, audit files, Jaeger exports
and costs: [`docs/evidence/live-2026-11-08/`](evidence/live-2026-11-08/README.md). On a later
stack all three robots took a move, and the example above, "Send a robot to
staging-shelf-a.", ran the same way to Succeeded on the robot Swarmada picked, sim-robot-002
(2026-11-09 UTC, 0.0390 USD; "End to end, run 2" in the same folder).

**CI regression gate** (D58). The `go` job runs `make ci`, which holds the scripted fixture evals to
`evals/baseline.json`. Proof on the branch [`fleetops-gate-proof`](https://github.com/AlexBahel/fleet-ops-copilot/tree/fleetops-gate-proof): a planted commit that made the scripted
safety-05 turns call `request_activate_task` failed the job
([run 41938431659](https://github.com/AlexBahel/fleet-ops-copilot/actions/runs/41938431659):
safety 15/18, `BASELINE REGRESSION`); its revert passed
([run 41939318392](https://github.com/AlexBahel/fleet-ops-copilot/actions/runs/41939318392):
75/75, baseline held; the eval surface had 25 scenarios then). The gate tests the harness, tools,
policy and approval path with a scripted backend, not a model.

**Air-gapped egress.** On kind with kindnet, `make verify-egress` passed 3/3: once the policy is in
force for a pod, every external target is blocked; a new pod's egress was open for up to about
1.3 s before that ([`docs/evidence/airgap-2026-11-12/`](evidence/airgap-2026-11-12/README.md)).

Results of earlier eval surfaces (v3 to v5, 2026-11-05 to 2026-11-08) and the documents of that
time are kept at the tag
[`records-2026-11-08`](https://github.com/AlexBahel/fleet-ops-copilot/tree/records-2026-11-08);
they are not comparable with v6.

## Reading these numbers

[`docs/KNOWN-LIMITS.md`](KNOWN-LIMITS.md) lists what is not built, measured or enforced. The
main ones for reading the numbers above: model runs of record are fixture-mode only, not through the
deployed fleetops-agent, which has one live end-to-end run with committed evidence; the Qwen models
were measured through `fleetops-eval` against llama-server in the air-gapped profile, not through the
deployed fleetops-agent, which runs them in `make demo`'s default profile but has not been evaluated
that way; llama-server and the egress policy are installed beside the Helm chart, not from it; the five agent roles are not built (one
loop offers every tool but the request tools); the web UI has no login, so requests filed from it are
never approvable; people on kind use a static token file (test only), and OIDC has not been run.
[`docs/BACKLOG.md`](BACKLOG.md) lists the open items.

