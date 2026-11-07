# Backlog for v4

What the record runs of 2026-11-05 (Sonnet 5.5, Opus 5.5 and Qwen2.5-7B-Instruct, 25 scenarios × 3,
judge v3) showed needs a change. Nothing here is done. The scores in
`evals/results/comparison-2026-11-05.md` stand as recorded (ruling R49); its footnote lists the
affected runs, and `docs/KNOWN-LIMITS.md` states each defect.

**A v4 change re-runs all three models.** A change to the judge, the scenarios or the tools makes a
new eval surface, and results from different surfaces are not comparable (see the judge v2 and v3
note in `docs/KNOWN-LIMITS.md`). So one change here is not made alone: batch the items, then run
Sonnet 5.5, Opus 5.5 and the air-gapped Qwen2.5-7B again.

**Batched into D81 (ruling R68, 2026-11-06).** J1–J3, S1 and P1 are made together with the
guided-request change (`docs/DESIGN.md` §3.10, §4.4, D81), which changes the prompt, the tools, the
judge and the scenarios anyway. Status of each is in its row. The model runs on eval surface v4
(42 scenarios; Sonnet 5.5, Opus 5.5 and the air-gapped Qwen2.5-7B) were made on 2026-11-07. v4 is
a different architecture from v3 (R69), so their comparison is a new table,
`evals/results/comparison-2026-11-07.md`, with v3 beside it as history. What those runs found is in
"Found in the v4 record runs" below; all but S3–S5 are fixed in eval surface v5 (D90).

## Judge

| # | Defect | Seen in | Direction |
|---|---|---|---|
| J1 | A kind word before a hyphenated word is read as an object that does not exist | Sonnet 5.5 diagnose-01 run 1 ("Robot/e-stop"); Opus 5.5 diagnose-07 run 2 ("Robot/emergency-stop") | Done in judge v4 (D81): a hyphenated common word after a kind word in the spaced form is not an object; `TestJ1HyphenatedCommonWords` uses the recorded texts and still fails invented names |
| J2 | A pronoun clause with an object name as its value is not accepted | Sonnet 5.5 diagnose-02 run 3: "It now has `status.assignedRobot`=sim-robot-002" | Done in judge v4 (D81), additive only: `TestJ2PronounClause` |
| J3 | A collective statement is not attributed to each object (R47) | diagnose-07 run 3: "All three robots have their front camera active"; the run passed every other criterion | Done in judge v4 (D81) when the count matches the objects of that kind: `TestJ3CollectiveClause` |

Each fix needs a judge test that fails on the old judge, and a check that no run in the three
recorded results changes verdict in the wrong direction. D81: each test uses the recorded text
that the v3 judge failed. All three fixes only add ways to accept a citation (J1 removes a
reference, J2 and J3 add a reading), so no run v3 passed can fail on them. R71 measured it: the
225 recorded runs, replayed offline on the v3 tree (every recorded v3 verdict reproduced) and
judged by judge v4 on criteria 1–4, change in six runs, all from fail to pass (J1: Sonnet 5.5
diagnose-01 run 1, Opus 5.5 diagnose-07 run 2; J2: Sonnet 5.5 diagnose-02 run 3; J3: Qwen2.5-7B
diagnose-07 runs 1–3); none from pass to fail (`docs/DESIGN.md` §4.4).

## Scenario

| # | Defect | Seen in | Fix |
|---|---|---|---|
| S1 | zone-06 `scheduledStart` (`2026-11-03T18:00:00Z`) is before the timestamps in the `live/zone-estop` fixture | Opus 5.5 runs 1 to 3 (reported as the scenario class). Sonnet 5.5 passed it | Done (D81): `2026-11-04T18:00:00Z` in the prompt, script and expected fields; fixture runs use a fixed clock before it |

## Product

| # | Gap | Seen in | Fix |
|---|---|---|---|
| P1 | `request_draft_task` has no waypoint field: an action's name, type, zone, priority, required capabilities, preferred robot and dependencies, and no payload (`internal/tools/requests.go`) | dispatch-04, Sonnet 5.5 and Opus 5.5, runs 1 to 3 | Done (D77): `location` on the request, written into the payload (D77); dispatch-04 checks the waypoint on the request |
| P2 | Conversations are kept in fleetops-agent's memory: lost on restart, one replica only | D75 | Durable, owner-bound conversation state shared by replicas (for example a copilot-owned resource holding only the dialogue state, with the same bounds and TTL), so fleetops-agent can run more than one replica (R69) |
| P4 | The demo stack left the fleet offline: the quickstart stops its sim adapter on exit, so nothing can be filed (R65 check) | Live check of D70–D81, 2026-11-07 | Done (R73): `hack/demo-sim-fleet.sh`, all three robots; make demo starts the quickstart's sim adapter for sim-robot-001 (its port-forward and `adapters/simulation/sim_adapter.py` with the quickstart's arguments) as a host process with a pidfile under `DEMO_DIR`; make demo-down stops it; make test-live checks a robot is Idle first |
| P3 | `fleetops activate` needs `fleetops:approver` to create its Activation record, which also grants deciding requests | D79 (R71, R72) | A narrow `fleetops:activator` ClusterRole: `create` on `approvaldecisions` limited by admission to `type: Activation` (the `fleetops-decisions` policy checking the caller's role, or a separate `approvaldecisions/activation` subresource-style verb via `authorizer` in CEL), `get` on `approvalrequests`; no Verdict decisions (R72) |

## Not yet examined

- zone-03 failed in both Claude runs (Sonnet 5.5 three runs, Opus 5.5 two). It is not classified as a
  model, judge, scenario or product failure.
- The Qwen2.5-7B runs were not checked for J1 to J3 or S1.

## Found in the v4 record runs (2026-11-07)

The scores in `evals/results/comparison-2026-11-07.md` stand as recorded. Each failed run's class
is in `failures.md` beside its results.json, and `docs/KNOWN-LIMITS.md` states each defect. As for
v3, a fix to the judge, a scenario, the prompt or the tools makes a new eval surface, which re-runs
all three models.

**Batched into eval surface v5 (D90, 2026-11-07).** P9 (D84), P8 (D85), P5 (D86), P6 (D87), P7
(D88), J4 and J5 (judge v5, D89) and S2 (D90) are fixed, each with a test that fails before the
fix, and P5–P9 each with a regression scenario (guided-18 to guided-21, zone-07). S3, S4 and S5
are open: the scenarios are unchanged until the owner rules on them. The three model runs are
repeated on v5; the v4 scores stand as recorded.

| # | Kind | Defect | Seen in | Direction |
|---|---|---|---|---|
| J4 | judge | A definite reference ("the action") before a value is not read as the object named in the sentence before | Sonnet 5.5 diagnose-04 run 2 | Decide whether a definite reference to the kind just named carries the object, as a pronoun does (J2); a judge test with the recorded text |
| J5 | judge | In a pronoun clause, a second object name (a zone) takes the attribution from the pronoun's object | Qwen2.5-7B diagnose-07 run 1 ("It is in the warehouse-a zone and is idle") | Attribute a value to the pronoun's object when the other name in the clause is not of the fact's kind; a judge test with the recorded text |
| S2 | scenario | dispatch-03 expects `draftTask.taskName` `charge-sim-robot-001`, which the prompt does not give | all nine runs (three per model) | Name the draft in the prompt, or check the name's prefix |
| S3 | scenario | guided-06 accepts only "composed" for a description the catalogue's `charge` kind covers | Opus 5.5 runs 1–3 (known: charge); Sonnet 5.5 read it as a charge too | Decide whether a known kind is a correct reading there, or change the description so that only basic steps fit |
| S4 | scenario | guided-07 expects "not possible" for counting damaged pallets with a vision service; the `propose_task` text calls a damage finding an inspection | Opus 5.5 runs 1, 2 | Decide which reading is wanted, and make the tool text and the scenario agree |
| S5 | scenario | guided-13 ("Send a robot to the charging bay.") expects a move with no question; all three models read it as a charge (9 of 9 runs) | all three models | Decide whether the prompt is a move; the scenario tests changing failure handling after the preview, which a prompt without the ambiguity would test as well |
| P5 | product | A location the catalogue does not know gives a preview with `location.zone: ""`; `check_task` runs no location check and says "likely feasible"; the CRD refuses the filing after the person confirmed; the person's reply is empty | Qwen2.5-7B guided-03 run 1, guided-04 run 2, guided-06 runs 2, 3 | Refuse or ask for an unknown location before the preview; a location check in `check_task`; show a filing error to the person |
| P6 | product | The engine asks its first question again when the model answers a "something else" description with a new ask, a third question over the cap of two (its own count stays at two) | Sonnet 5.5 guided-06 runs 1–3 | Count every person-facing question against the cap; decide what a new ask after "something else" means |
| P7 | product | A `start` that is not RFC 3339 (`now`, `on approval`) is refused by `check_task` and stays in the dialogue state, so later calls without a start are refused too | Opus 5.5 zone-03 runs 1, 2 (failed only on this); Sonnet 5.5 zone-03 runs 2, 3 | Map or refuse the value in the engine, before `check_task`; do not keep a refused value |
| P8 | product | Unknown `propose_task` field names are dropped without notice and the default is used | guided-12, Sonnet 5.5 and Qwen2.5-7B, runs 1–3 each (`repeat` for `times`) | Return the unknown names to the model, or map common synonyms in the catalogue |
| P9 | product | When `propose_task` ends the turn, only that model response's text reaches the person; text of earlier responses of the same message is neither shown nor recorded | Of the runs that failed on a required fact: 37 Sonnet 5.5 and 34 Opus 5.5 with no model prose at all | Record every response's text in the trace; decide whether earlier text is shown |

## Proposed for Swarmada draft 5

Gaps found by the guided-request inventory (`docs/design/guided-request-inventory.md` §4, class (c)). They are
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
