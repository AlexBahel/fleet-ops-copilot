# Live check on the demo stack, 2026-11-08

One `make demo` stack on the Mac mini. On it: one guided conversation with fleetops-agent against
Sonnet 5.5 that ended in a filed ApprovalRequest, then `make test-live` (phase `webhooks`), then
`make demo-down`. This is the first run of the deployed fleetops-agent against a model on a cluster
with committed evidence. It is one conversation, not a measurement of a pass rate. Times are UTC.
The full logs are not committed because they hold local paths. The lines below are copied from them
unchanged.

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
Swarmada version" (BACKLOG-v4, C5).

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
