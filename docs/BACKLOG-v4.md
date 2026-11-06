# Backlog for v4

What the record runs of 2026-11-05 (Sonnet 5.5, Opus 5.5 and Qwen2.5-7B-Instruct, 25 scenarios × 3,
judge v3) showed needs a change. Nothing here is done. The scores in
`evals/results/comparison-2026-11-05.md` stand as recorded (ruling R49); its footnote lists the
affected runs, and `docs/KNOWN-LIMITS.md` states each defect.

**A v4 change re-runs all three models.** A change to the judge, the scenarios or the tools makes a
new eval surface, and results from different surfaces are not comparable (see the judge v2 and v3
note in `docs/KNOWN-LIMITS.md`). So one change here is not made alone: batch the items, then run
Sonnet 5.5, Opus 5.5 and the air-gapped Qwen2.5-7B again.

**Batched into FOC-8 (ruling R68, 2026-11-06).** J1–J3, S1 and P1 are made together with the
guided-request change (`docs/DESIGN.md` §3.10, §4.4, D81), which changes the prompt, the tools, the
judge and the scenarios anyway. Status of each is in its row. What remains of v4 is the model runs:
Sonnet 5.5, Opus 5.5 and the air-gapped Qwen2.5-7B on eval surface v4 (39 scenarios), which is a
different architecture from v3 (R69), so the comparison is a new table, not a column added to the
old one.

## Judge

| # | Defect | Seen in | Direction |
|---|---|---|---|
| J1 | A kind word before a hyphenated word is read as an object that does not exist | Sonnet 5.5 diagnose-01 run 1 ("Robot/e-stop"); Opus 5.5 diagnose-07 run 2 ("Robot/emergency-stop") | Done in judge v4 (FOC-8): a hyphenated common word after a kind word in the spaced form is not an object; `TestJ1HyphenatedCommonWords` uses the recorded texts and still fails invented names |
| J2 | A pronoun clause with an object name as its value is not accepted | Sonnet 5.5 diagnose-02 run 3: "It now has `status.assignedRobot`=sim-robot-002" | Done in judge v4 (FOC-8), additive only: `TestJ2PronounClause` |
| J3 | A collective statement is not attributed to each object (R47) | diagnose-07 run 3: "All three robots have their front camera active"; the run passed every other criterion | Done in judge v4 (FOC-8) when the count matches the objects of that kind: `TestJ3CollectiveClause` |

Each fix needs a judge test that fails on the old judge, and a check that no run in the three
recorded results changes verdict in the wrong direction. FOC-8: each test uses the recorded text
that the v3 judge failed. All three fixes only add ways to accept a citation (J1 removes a
reference, J2 and J3 add a reading), so no run v3 passed can fail on them. R71 measured it: the
225 recorded runs, replayed offline on the v3 tree (every recorded v3 verdict reproduced) and
judged by judge v4 on criteria 1–4, change in six runs, all from fail to pass (J1: Sonnet 5.5
diagnose-01 run 1, Opus 5.5 diagnose-07 run 2; J2: Sonnet 5.5 diagnose-02 run 3; J3: Qwen2.5-7B
diagnose-07 runs 1–3); none from pass to fail (`docs/DESIGN.md` §4.4).

## Scenario

| # | Defect | Seen in | Fix |
|---|---|---|---|
| S1 | zone-06 `scheduledStart` (`2026-11-03T18:00:00Z`) is before the timestamps in the `live/zone-estop` fixture | Opus 5.5 runs 1 to 3 (reported as the scenario class). Sonnet 5.5 passed it | Done (FOC-8): `2026-11-04T18:00:00Z` in the prompt, script and expected fields; fixture runs use a fixed clock before it |

## Product

| # | Gap | Seen in | Fix |
|---|---|---|---|
| P1 | `request_draft_task` has no waypoint field: an action's name, type, zone, priority, required capabilities, preferred robot and dependencies, and no payload (`internal/tools/requests.go`) | dispatch-04, Sonnet 5.5 and Opus 5.5, runs 1 to 3 | Done (FOC-8): `location` on the request, written into the payload (D77); dispatch-04 checks the waypoint on the request |
| P2 | Conversations are kept in foc-agent's memory: lost on restart, one replica only | FOC-8 (D75) | Durable, owner-bound conversation state shared by replicas (for example a copilot-owned resource holding only the dialogue state, with the same bounds and TTL), so foc-agent can run more than one replica (R69) |
| P3 | `foc activate` needs `fleetops:approver` to create its Activation record, which also grants deciding requests | FOC-8 (R71, R72) | A narrow `fleetops:activator` ClusterRole: `create` on `approvaldecisions` limited by admission to `type: Activation` (the `fleetops-decisions` policy checking the caller's role, or a separate `approvaldecisions/activation` subresource-style verb via `authorizer` in CEL), `get` on `approvalrequests`; no Verdict decisions (R72) |

## Not yet examined

- zone-03 failed in both Claude runs (Sonnet 5.5 three runs, Opus 5.5 two). It is not classified as a
  model, judge, scenario or product failure.
- The Qwen2.5-7B runs were not checked for J1 to J3 or S1.

## Proposed for Swarmada draft 5

Gaps found by the FOC-8 inventory (`docs/design/foc8-inventory.md` §4, class (c)). They are
proposals to Swarmada, not copilot work; the copilot lists them in its catalogue as
`notYetPossible` and files nothing that needs one.

| # | Proposal | Why the copilot needs it |
|---|---|---|
| C5 | **Eligibility dry run (ruling R62 b).** A read-only way to ask the control plane, for a FleetAction spec that is not created: which robots pass every candidate filter of RFC §9.4.2 now, and for each robot withheld, the filter number and reason (the same tally `noCandidateMessage` writes), plus the zone-restriction verdict (filter 11) and, optionally, the adapter's `validate_action` answer for the best candidate. For example a `dryRun` create of a FleetAction that returns this as a warning, or a subresource. Must not reserve, assign or change anything | `check_task` re-implements the filters from reads (D73) and cannot see `validate_action`; Swarmada's own answer would replace the copy and show the adapter's decision before activation |
| C1 | A structured result for a robot action (`ActionStatusUpdate` field, `status.result` for non-External actions) | every inspect kind and patrol findings are class (b); the catalogue flips `resultReported` when this lands (R67) |
| C2 | Evidence artifacts (references with digest and pose) for robot actions | inspect with photos |
| C3 | A dispatch path for External actions | inspection by a vision service on a robot's stream |
| C4 | Waypoints delivered to the adapter, or a typed destination | the copilot copies a waypoint's `x`, `y` into the payload |
| C6 | A Charge exemption from the battery floor, or a charging path | a robot below `minBatteryPctForAction` cannot be sent to charge (`CHARGE_BELOW_BATTERY_FLOOR`) |
| C7 | `dependsOn` validated at admission | a bad dependency in a draft is found only after activation |
