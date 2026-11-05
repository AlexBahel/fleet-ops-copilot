# Backlog for v4

What the record runs of 2026-11-05 (Sonnet 5.5, Opus 5.5 and Qwen2.5-7B-Instruct, 25 scenarios × 3,
judge v3) showed needs a change. Nothing here is done. The scores in
`evals/results/comparison-2026-11-05.md` stand as recorded (ruling R49); its footnote lists the
affected runs, and `docs/KNOWN-LIMITS.md` states each defect.

**A v4 change re-runs all three models.** A change to the judge, the scenarios or the tools makes a
new eval surface, and results from different surfaces are not comparable (see the judge v2 and v3
note in `docs/KNOWN-LIMITS.md`). So one change here is not made alone: batch the items, then run
Sonnet 5.5, Opus 5.5 and the air-gapped Qwen2.5-7B again.

## Judge

| # | Defect | Seen in | Direction |
|---|---|---|---|
| J1 | A kind word before a hyphenated word is read as an object that does not exist | Sonnet 5.5 diagnose-01 run 1 ("Robot/e-stop"); Opus 5.5 diagnose-07 run 2 ("Robot/emergency-stop") | Not count a hyphenated common word as an object name. It must still count a real invented `Kind name` |
| J2 | A pronoun clause with an object name as its value is not accepted | Sonnet 5.5 diagnose-02 run 3: "It now has `status.assignedRobot`=sim-robot-002" | Accept a fact stated in a clause whose subject is a pronoun for the object just named |
| J3 | A collective statement is not attributed to each object (R47) | diagnose-07 run 3: "All three robots have their front camera active"; the run passed every other criterion | Attribute "all three robots ..." to each of the objects it covers, if the answer names them or the cluster has exactly that set |

Each fix needs a judge test that fails on the old judge, and a check that no run in the three
recorded results changes verdict in the wrong direction.

## Scenario

| # | Defect | Seen in | Fix |
|---|---|---|---|
| S1 | zone-06 `scheduledStart` (`2026-11-03T18:00:00Z`) is before the timestamps in the `live/zone-estop` fixture | Opus 5.5 runs 1 to 3 (reported as the scenario class). Sonnet 5.5 passed it | Move the start after the fixture's timestamps, in the prompt, the script and the expected approval fields together |

## Product

| # | Gap | Seen in | Fix |
|---|---|---|---|
| P1 | `request_draft_task` has no waypoint field: an action's name, type, zone, priority, required capabilities, preferred robot and dependencies, and no payload (`internal/tools/requests.go`) | dispatch-04, Sonnet 5.5 and Opus 5.5, runs 1 to 3 | Add the field to the tool and to the approval request, then judge dispatch-04 again. A tool change also changes the tools hash in the eval surface |

## Not yet examined

- zone-03 failed in both Claude runs (Sonnet 5.5 three runs, Opus 5.5 two). It is not classified as a
  model, judge, scenario or product failure.
- The Qwen2.5-7B runs were not checked for J1 to J3 or S1.
