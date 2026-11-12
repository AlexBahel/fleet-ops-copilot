# Backlog

Open items. What is not built, not measured or not enforced today is in `docs/KNOWN-LIMITS.md`;
decisions are in `docs/DESIGN.md` §7.

## Product

| # | Gap | Seen in | Fix |
|---|---|---|---|
| P2 | Conversations are kept in fleetops-agent's memory: lost on restart, one replica only | D75 | Durable, owner-bound conversation state shared by replicas (for example a copilot-owned resource holding only the dialogue state, with the same bounds and TTL), so fleetops-agent can run more than one replica |
| P3 | `fleetops activate` needs `fleetops:approver` to create its Activation record, which also grants deciding requests | D79 | A narrow `fleetops:activator` ClusterRole: `create` on `approvaldecisions` limited by admission to `type: Activation` (the `fleetops-decisions` policy checking the caller's role, or a separate `approvaldecisions/activation` subresource-style verb via `authorizer` in CEL), `get` on `approvalrequests`; no Verdict decisions |

## Proposed after the v6 record runs

Not started; none changes the eval surface until it is made.

| # | Kind | Item | Direction |
|---|---|---|---|
| A1 | product | A menu-only mode for air-gapped use: no model at all, the person picks numbered catalogue choices only | Start every request with the catalogue's asks as a numbered question and walk the same decision trees, questions, preview and confirmation; free text gets "Something else — describe it" filed for review only. Measure it with the scripted person's replies, not a model |
| A2 | evaluation | A newer tool-tuned local model, since Qwen2.5-14B did not do better than Qwen2.5-7B on v6 (24 against 26 of 150) | Pick an openly licensed model trained for tool calls that fits the 21.5 GiB Docker VM, pin its files by sha256 as for the Qwen runs, and run it on the current surface beside Qwen2.5-7B with the same server build and settings |
| A3 | product | The policy-allowed values (D93) come from fleetops-agent reading the policy file that fleetops-mcp enforces, two readers of one file | Have fleetops-mcp serve the allowed values (for example in a read tool or the `check_task` result) and fleetops-agent build `propose_task`'s enums from that answer, so the values the model sees are those the enforcing component holds |
| A4 | model | Opus 5.5 numbers its own questions to the person in 8 v6 runs (`evals/results/comparison-v6.md`, "v6 checks"), against "Do not ask your own questions" | Decide whether the prompt says it more plainly, or the engine turns a final answer that asks for field values into a `propose_task` reading; a judge check that counts such questions, measured on the next record run |

## Proposed for a later Swarmada version

Class (c) kinds of work (`docs/DESIGN.md` §3.10.1): each needs a Swarmada change. They are proposals
to Swarmada, not copilot work; the copilot lists them in its catalogue as `notYetPossible` and files
nothing that needs one.

| # | Proposal | Why the copilot needs it |
|---|---|---|
| C1 | A structured result for a robot action (`ActionStatusUpdate` field, `status.result` for non-External actions) | every inspect kind and patrol findings are class (b); the catalogue flips `resultReported` when this lands (D70) |
| C2 | Evidence artifacts (references with digest and pose) for robot actions | inspect with photos |
| C3 | A dispatch path for External actions | inspection by a vision service on a robot's stream |
| C4 | Waypoints delivered to the adapter, or a typed destination | the copilot copies a waypoint's `x`, `y` into the payload |
| C5 | **Eligibility dry run.** A read-only way to ask the control plane, for a FleetAction spec that is not created: which robots pass every candidate filter of RFC §9.4.2 now, and for each robot withheld, the filter number and reason (the same tally `noCandidateMessage` writes), plus the zone-restriction verdict (filter 11) and, optionally, the adapter's `validate_action` answer for the best candidate. For example a `dryRun` create of a FleetAction that returns this as a warning, or a subresource. Must not reserve, assign or change anything | `check_task` re-implements the filters from reads (D73) and cannot see `validate_action`; Swarmada's own answer would replace the copy and show the adapter's decision before activation |
| C6 | A Charge exemption from the battery floor, or a charging path | a robot below `minBatteryPctForAction` cannot be sent to charge (`CHARGE_BELOW_BATTERY_FLOOR`) |
| C7 | `dependsOn` validated at admission | a bad dependency in a draft is found only after activation |
