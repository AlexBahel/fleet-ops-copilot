# Live check on the demo stack, 2026-11-08

Two `make demo` stacks on the Mac mini, one after the other. Times are UTC. The full logs are not
committed because they hold local paths. The lines below are copied from them unchanged. Neither
run measures a pass rate.

1. **21:32–21:39, filing and `make test-live`.** One guided conversation with the deployed
   fleetops-agent against Sonnet 5.5 ended in a filed ApprovalRequest. `make test-live` (phase
   `webhooks`) followed, then `make demo-down`. Sections below, up to "Teardown".
2. **23:32–23:58, end to end.** A guided conversation, dana's approval, the draft created by
   fleetops-approvals, an activation request from the same conversation, dana's approval of it,
   `fleetops activate` by dana, and the FleetTask **Succeeded**. See
   [End to end](#end-to-end) and [`end-to-end/`](end-to-end/).

| Item | Value |
|---|---|
| Copilot | `d17064d` (branch `golive`); the image `fleet-ops-copilot:dev` built from it by `make demo` |
| Swarmada | v0.4, `558da47` (tag `rfc-0001-v0.4`) through the `go.mod` pin; quickstart `healthy-fleet` from a writable copy of the pinned module; admission webhooks served (`config/controlstream-tls`); three sim adapters kept live by `hack/demo-sim-fleet.sh` (R73) |
| Cluster | kind `fleetops-demo`, kind v0.33.0, single node `kindest/node:v1.37.0` pinned by digest (`hack/demo-up.sh`) |
| Host | Mac mini (Mac18,5), macOS 27.0.1; Docker 29.9.2, VM 8 CPUs, 23066771456 bytes memory |
| Command | `DEMO_TRACING=1 DEMO_ANTHROPIC_KEY_FILE=<key file> make demo`: exit 0, "stack running on kind-fleetops-demo in 2m29s (149s)", 21:32:58–21:35:27 |
| Model | fleetops-agent `-backend=claude`; the chart's default model `claude-sonnet-5-5` (the agent's start line: `backend claude:claude-sonnet-5-5`); catalogue version 1, sha256 `43ab5816…69cc6a` |

`make demo` per stage: kind cluster 25 s, Swarmada quickstart 1m18s, Swarmada webhooks 12 s,
preflight 6 s, sim fleet 5 s, image 9 s, Helm chart 1 s, health checks 11 s; every other stage 1 s or
less. Before the conversation, Robots sim-robot-001 and sim-robot-003 were `Idle`. sim-robot-002 was
`InProgress` on the quickstart's `inspect-receiving-dock`. All four sim-fleet processes were alive.

## The conversation

[`conversation.txt`](conversation.txt) has both messages and both answers, as `fleetops chat`
printed them. sam used the token kubeconfig `make demo` wrote, so the requester was verified.

1. 21:35:59, "Send a robot to staging-shelf-a." Sonnet 5.5 made two model calls: the first called
   `list_zones` and `list_robots`, the second `propose_task` (its arguments are not in the trace; the
   preview names the catalogue kind `move`). The engine then ran `list_zones`, `list_robots`,
   `list_fleet_tasks` and `check_task` itself. It returned the preview with no question asked, the
   verdict "likely feasible", eligible now sim-robot-001 and sim-robot-003, and the `Checked:` block.
   That block gives the zone's state and, per Robot, phase, battery, zone and navigation, and
   filter 1 for sim-robot-002. Agent log: `turns=2 tools=7 latency_ms=3259`.
2. 21:36:08, "1" (Confirm). The engine filed the request with no model call:
   `tool request_draft_task REQUIRE_APPROVAL -> ApprovalRequest/areq-wn452`. Agent log:
   `turns=0 tools=1 latency_ms=28`.

The `(session …)` line differs between the two answers because it is the id of each message's
request (`internal/webui/server.go`). The conversation continues through the conversation id that
`fleetops chat` keeps per context.

## The filed request

[`approvalrequest-areq-wn452.yaml`](approvalrequest-areq-wn452.yaml) is `kubectl get -o yaml` of
the request, unchanged. It holds no token. The fields:

- `type: DraftTask`, `requestedFor: sam`, `requestedBy` the fleetops-mcp ServiceAccount.
- `state: Pending`; nobody decided it before `make demo-down`.
- `preview.kind: move`, `preview.interpretation: Known`, `preview.confirmedAt` 21:36:08.
- The catalogue's version and sha256.
- `checks.verdict: likely feasible`, with the three checks no client can make listed as `NotChecked`.

The `NotChecked` text for `validate_action` still says "Swarmada draft 5, C5". It is a string in
`internal/feasibility/feasibility.go` that is recorded as written; the documents now say "a later
Swarmada version" (BACKLOG-v4, C5). BACKLOG-v4 A5 changes the string with the next eval-surface change.

## The trace

[`jaeger-fleetops-agent.json`](jaeger-fleetops-agent.json) and
[`jaeger-fleetops-mcp.json`](jaeger-fleetops-mcp.json) are the chart's Jaeger answers to
`/api/v3/traces` for each service, 21:35–21:37, unchanged apart from indentation.

- **Message 1** is one trace, `9e51e50c…`. It has fleetops-agent's `fleetops.request`
  (`fleetops.backend` `claude:claude-sonnet-5-5`, `fleetops.turns` 2, latency 3258.8 ms), two
  `llm.chat` spans and two `tool.call` spans (`list_zones`, `list_robots`). fleetops-mcp's six
  `mcp.tool` spans in it are the reads the model and the engine made.
- **Message 2** has no fleetops-agent span. fleetops-agent starts `fleetops.request` only when it
  enters the model loop (`internal/agent/agent.go`), and the confirmation that files does not. Its
  fleetops-mcp spans are a separate trace, `e3e6b41c…`: `policy.decide` (`REQUIRE_APPROVAL`, op
  `draft_task`) and `mcp.tool` `request_draft_task`. Two further one-span traces are the `whoami`
  calls of the two messages. Listed in `docs/KNOWN-LIMITS.md`.

## Cost

Token counts from the `fleetops.request` span of message 1. They equal the sum of its two `llm.chat`
spans:

| Tokens | Count | USD per million | USD |
|---|---|---|---|
| input | 6 | 2 | 0.000012 |
| output | 173 | 10 | 0.001730 |
| cache write | 10150 | 2.5 | 0.025375 |
| cache read | 8833 | 0.1 | 0.000883 |
| **total** | | | **0.0280** |

The prices are those recorded for `claude-sonnet-5-5` in
[`evals/results/cloud-claude-sonnet-5-5-2026-11-08-v6/results.json`](../../../evals/results/cloud-claude-sonnet-5-5-2026-11-08-v6/results.json)
(`cost.pricing`: source `https://platform.claude.com/docs/en/about-claude/pricing`, read on
2026-11-08). Message 2 made no model call.

## `make test-live`

21:37:21–21:38:45, `make test-live` (`hack/live-approval-path.sh`, `PHASE=webhooks`): 1:24.05 total,
exit 0. Requests are filed with sam's token and decided by dana with hers.

```text
--- SKIP: TestLiveDraftGateAbsent (0.00s)
--- PASS: TestLiveApprovalPaths (73.42s)
    --- PASS: TestLiveApprovalPaths/swarmada-webhooks-served (0.01s)
    --- PASS: TestLiveApprovalPaths/draft-created-by-executor (11.44s)
    --- PASS: TestLiveApprovalPaths/activate-refused-to-serviceaccounts (2.06s)
    --- PASS: TestLiveApprovalPaths/activation-by-person-observed (11.24s)
    --- PASS: TestLiveApprovalPaths/requester-is-authenticated-not-asserted (10.23s)
    --- PASS: TestLiveApprovalPaths/executor-writes-only-after-named-approval (26.16s)
    --- PASS: TestLiveApprovalPaths/zone-maintenance-by-executor (6.09s)
    --- PASS: TestLiveApprovalPaths/zone-stop-by-executor (6.08s)
    --- PASS: TestLiveApprovalPaths/guardrails-api-server-refuses-serviceaccounts (0.04s)
    --- PASS: TestLiveApprovalPaths/every-write-traces-to-a-named-approval (0.01s)
    --- PASS: TestLiveApprovalPaths/audit-record (0.07s)
--- SKIP: TestLiveSwarmadaAuditActor (0.00s)
PASS
ok  	github.com/AlexBahel/fleet-ops-copilot/test/live	73.767s
--- PASS: TestLiveSwarmadaAuditActor (0.03s)
PASS
ok  	github.com/AlexBahel/fleet-ops-copilot/test/live	0.319s
```

`TestLiveSwarmadaAuditActor` passed in its own `go test` invocation, after the first one skipped it.
`TestLiveDraftGateAbsent` is skipped by design: it needs phase `no-webhooks`.

What some of the tests logged:

| Subtest | Observed |
|---|---|
| `requester-is-authenticated-not-asserted` | dana's approval of a request with `requestedFor` `unverified:dana` was refused ("the requester \"unverified:dana\" was not authenticated …; such a request can be rejected but never approved"), and the request stayed Pending |
| `executor-writes-only-after-named-approval` | dana's approval of her own request was refused ("\"dana\" asked for this change and cannot also approve it") |
| `every-write-traces-to-a-named-approval` | FleetTask `fleetops-live-draft-1794173846` and ZoneMaintenance `fleetops-areq-nllr9` each trace to an Executed request decided by dana, requested for sam |
| `audit-record` | `fleetops-approvals audit file: 14 entries, chain intact` |
| `TestLiveSwarmadaAuditActor` | Swarmada's safety audit log records the approved zone stop as `ESTOP_TRIGGERED` with actor `system:serviceaccount:fleetops-system:fleetops-approvals`, `outcome=Allowed`, `robots:3 stopped:3 unconfirmed:0`. On 2026-11-05, before the sim fleet was kept live (R73), the outcome was `Error` with `unconfirmed:3` (`docs/evidence/foc-live-2026-11-05/`) |

## Teardown

`make demo-down` at 21:39:46: exit 0, "clean ✓ — no cluster, no tracked processes, no local
pidfile/log state". No kind cluster and no `sim_adapter.py` process was left.

## End to end

23:32–23:58.

| Item | Value |
|---|---|
| Copilot | `dfba88e` (main); the image built from it by `make demo` |
| Swarmada, cluster, host | as above |
| Command | `DEMO_TRACING=1 DEMO_ANTHROPIC_KEY_FILE=<key file> make demo`: exit 0, "stack running on kind-fleetops-demo in 2m19s (139s)", 23:32:26–23:34:45 |
| People | sam files every request with his token kubeconfig; dana approves and activates with hers |

Two chains of requests ran. Chain 1 had every step up to activation, but its task never ran
(below). Chain 2 asked for the one robot that completes work on this stack, and its task Succeeded.
Each file in [`end-to-end/`](end-to-end/) is unchanged as `kubectl get -o yaml`, the API or the
pod printed it, unless noted. None holds a token.

### Chain 2: the FleetTask Succeeded

[`end-to-end/chain-2.txt`](end-to-end/chain-2.txt) has every command and its output.

1. 23:56:35, sam: "Send sim-robot-001 to staging-shelf-a." The preview pins sim-robot-001 ("a
   preference; another eligible robot runs it if this one cannot"), says "likely feasible", and has
   the `Checked:` block. It asked no question.
2. 23:56:48, sam: "1". ApprovalRequest `areq-krhrr` was filed (DraftTask, `requestedFor: sam`).
3. 23:56:48, dana: `fleetops approve areq-krhrr`. Verdict ApprovalDecision `areq-krhrr-wnqjx`
   (`verdict: Approve`, `decidedBy: dana`). fleetops-approvals re-ran the checks and dry-ran
   `FleetTask/move-staging-shelf-a-ecad` and `FleetAction/move-staging-shelf-a-ecad-move` ("accepted
   by the API server (dry run; nothing stored)", with Swarmada's webhooks served). It then created
   the draft (`status.executionChecks`, `state: Executed`).
4. 23:56:56, sam, same conversation: "Activate the draft move-staging-shelf-a-ecad." The preview
   (kind `activate`) checks `FleetTask/move-staging-shelf-a-ecad: spec.draft=true,
   status.phase=Draft`. At 23:57:00 sam's "1" filed ActivateTask request `areq-jh82j`.
5. 23:57:00, dana: `fleetops approve areq-jh82j`. Verdict `areq-jh82j-nq6hf` (`Approve`). The
   answer gives the instruction `fleetops activate areq-jh82j -n warehouse-a`.
6. 23:57:07, dana: `fleetops activate areq-jh82j`. It re-checked ("likely feasible"), recorded
   Activation ApprovalDecision `areq-jh82j-activation-js2vx` ("type Activation, by dana"), and
   cleared `spec.draft`.
7. 23:57:16, `FleetTask/move-staging-shelf-a-ecad` is `phase: Succeeded`, `actionSummary: 1/1
   Succeeded`, member `move` on sim-robot-001, `completionTime` 23:57:16. Its condition
   `AwaitingActivation` reads "spec.draft was cleared; the task is dispatched as written".
   fleetops-approvals recorded `status.outcome.phase: Succeeded` on `areq-krhrr` and
   `activatedBy: dana`, `activationRef: areq-jh82j-activation-js2vx` on `areq-jh82j`.

Files: [`approvalrequest-areq-krhrr.yaml`](end-to-end/approvalrequest-areq-krhrr.yaml),
[`approvalrequest-areq-jh82j.yaml`](end-to-end/approvalrequest-areq-jh82j.yaml),
[`approvaldecisions.yaml`](end-to-end/approvaldecisions.yaml) (all six decisions of both chains: four
Verdicts and two Activations),
[`fleettask-move-staging-shelf-a-ecad.yaml`](end-to-end/fleettask-move-staging-shelf-a-ecad.yaml),
[`fleetaction-move-staging-shelf-a-ecad-move.yaml`](end-to-end/fleetaction-move-staging-shelf-a-ecad-move.yaml).

### Who did what: the audit actors

- **Decisions.** Every ApprovalDecision has `decidedBy: dana`, which the `fleetops-decisions`
  admission policy binds to the authenticated user (D79). The FleetTask carries the annotations `approved-by:
  dana` and `requested-for: sam`, and the label `created-by: fleetops-approvals`.
- **fleetops-approvals' audit file**
  ([`fleetops-approvals-audit.jsonl`](end-to-end/fleetops-approvals-audit.jsonl), 9 entries; `fleetops
  audit verify`: "9 entries, chain intact", [`audit-verify.txt`](end-to-end/audit-verify.txt),
  file path shortened). For each chain it has `approved` (actor dana), `executed` (actor dana, target
  the FleetTask), `approved` for the activation request, and `activation_observed` (`activatedBy:
  dana`). Chain 2 adds `task_outcome` ("FleetTask/move-staging-shelf-a-ecad ended Succeeded").
- **Events** ([`events-warehouse-a.txt`](end-to-end/events-warehouse-a.txt)): "ActivationObserved …
  left Draft after approval by dana, activated by dana; the copilot did not perform the activation";
  "TaskOutcome … ended Succeeded".
- **Swarmada's safety audit log** ([`swarmada-safety-audit.jsonl`](end-to-end/swarmada-safety-audit.jsonl),
  copied read-only as `make test-live` does). It holds no entry for the activation: Swarmada does not
  record who cleared `spec.draft` (`docs/KNOWN-LIMITS.md`). Its 39 entries are the
  `ROBOT_AUTHZ_DENIED` of chain 1's finding below.

### Chain 1: approved and activated, never ran

[`end-to-end/chain-1.txt`](end-to-end/chain-1.txt). This is the conversation of the 21:35 run
repeated. "Send a robot to staging-shelf-a." at 23:34:59 gave the same preview, and "1" filed
`areq-wkhgv`. dana approved it at 23:39:05: Verdict `areq-wkhgv-mvl4m`, draft
`move-staging-shelf-a-f548` created. "Activate the draft move-staging-shelf-a-f548." and "1" filed
`areq-675hq` at 23:39:23. dana approved it (`areq-675hq-s4tkx`) and ran `fleetops activate` at 23:39:39
(Activation `areq-675hq-activation-k8mcc`, `activatedBy: dana`). Swarmada assigned the member to
sim-robot-003, and its FleetAction stayed `Assigned`, lease renewed, until teardown 19 minutes later
([`fleettask-move-staging-shelf-a-f548.yaml`](end-to-end/fleettask-move-staging-shelf-a-f548.yaml),
[`fleetaction-move-staging-shelf-a-f548-move.yaml`](end-to-end/fleetaction-move-staging-shelf-a-f548-move.yaml)).

What was seen:

- The sim adapter of sim-robot-003 logged one line at start: `register refused robot=sim-robot-003:
  PERMISSION_DENIED: not authorized for robot sim-robot-003`. The adapter of sim-robot-002 logged the
  same for its robot.
- Swarmada's safety audit log has 39 `ROBOT_AUTHZ_DENIED` entries from `sim-fleet-adapter-2` and
  `-3`, 23:34:22–23:34:26: "robot \"sim-robot-003\" is bound to adapter \"sim-fleet-adapter\", not
  \"sim-fleet-adapter-3\"". `hack/demo-sim-fleet.sh` starts the adapters before it points the two
  Robots at them.
- sim-robot-002 stayed `InProgress` on the quickstart's `inspect-receiving-dock`, `Assigned` for the
  whole run. sim-robot-001, served by the quickstart's own adapter, completed every action given to it
  (`deliver-pallet-001`, `receiving-round-001-inspect-dock`, chain 2's move).
- At 23:51:08, sim-robot-003's adapter process was restarted by hand with the same arguments, after
  its Robot pointed at it. Nothing changed by 23:56. Its log after the restart is empty.

So on this stack all three Robots show as live (Idle or InProgress, `AdapterLive`), and on
2026-11-08 at 21:38 all three confirmed a zone stop. But only sim-robot-001 completes work. Why the
other two never act is not established beyond the refused register. Listed in `docs/KNOWN-LIMITS.md`
and BACKLOG-v4 P13.

### Trace and cost

[`end-to-end/jaeger-fleetops-agent.json`](end-to-end/jaeger-fleetops-agent.json) and
[`end-to-end/jaeger-fleetops-mcp.json`](end-to-end/jaeger-fleetops-mcp.json) are Jaeger's
`/api/v3/traces` answers for 23:34–23:59, re-indented. fleetops-approvals exports no spans.

The four messages that reached the model each have one `fleetops.request` trace
(`fleetops.backend` `claude:claude-sonnet-5-5`, two model calls each). The four "1" confirmations
made no model call and have no fleetops-agent trace (`docs/KNOWN-LIMITS.md`). Tokens per trace:

| Message | Input | Output | Cache write | Cache read | USD |
|---|---|---|---|---|---|
| chain 1, "Send a robot to staging-shelf-a." | 6 | 158 | 10135 | 8833 | 0.0278 |
| chain 1, "Activate the draft …-f548." | 6 | 242 | 2201 | 18361 | 0.0098 |
| chain 2, "Send sim-robot-001 to staging-shelf-a." | 6 | 197 | 9525 | 8838 | 0.0267 |
| chain 2, "Activate the draft …-ecad." | 6 | 345 | 2352 | 18398 | 0.0112 |
| **total** | 24 | 942 | 24213 | 54430 | **0.0754** |

Chain 2 alone, from request to Succeeded: **0.0379 USD**. Chain 1: 0.0376 USD. The prices are the
ones in "Cost" above (2, 10, 2.5 and 0.1 USD per million input, output, cache-write and cache-read
tokens). They come from `cost.pricing` in
[`evals/results/cloud-claude-sonnet-5-5-2026-11-08-v6/results.json`](../../../evals/results/cloud-claude-sonnet-5-5-2026-11-08-v6/results.json):
source `https://platform.claude.com/docs/en/about-claude/pricing`, read on 2026-11-08.

### Teardown

`make demo-down` at 23:58:37: exit 0, "clean ✓ — no cluster, no tracked processes, no local
pidfile/log state". The restarted adapter was in the pidfile and stopped with the others; no
`sim_adapter.py` process was left.
