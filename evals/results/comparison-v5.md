# Model comparison — eval surface v5, 47 scenarios × 3, fixture mode (2026-11-08)

> **v5, v4 and v3 are not like-for-like.** Eval surface v5 (D90) changes the product, the judge and the scenarios
> against v4: every model response's text of a message now reaches the person (D84); `propose_task`'s schema is
> narrowed to the site catalogue and an unknown field name is refused (D85); a location that is not a waypoint is
> blocked as `WAYPOINT_NOT_FOUND` and a refused filing gets a reply (D86); every question shown counts against the
> two-question cap (D87); a value without its field's form is asked again (D88); a start of `now`, `immediately` or
> `asap` starts on approval (D91); judge v5 credits a definite reference and a second name in a pronoun clause (J4,
> J5, D89); dispatch-03 no longer expects a draft name its prompt does not give (S2), and guided-06, guided-07 and
> guided-13 were changed on the owner's ruling (S3–S5); five regression scenarios were added (guided-18 to
> guided-21, zone-07), so the categories zone-ops and guided have more runs. The system prompt text is v4's (prompt
> version v5 because the tool list changed). v4 (D81) was itself a different architecture from v3. The v4 and v3
> columns are history (`docs/DESIGN.md` §4.4), not the same measurement.

Sources (v5): [`cloud-claude-sonnet-5-5-2026-11-08-v5/results.json`](cloud-claude-sonnet-5-5-2026-11-08-v5/results.json), [`cloud-claude-opus-5-5-2026-11-08-v5/results.json`](cloud-claude-opus-5-5-2026-11-08-v5/results.json), [`airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v5/results.json`](airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v5/results.json).
History: v4 [`comparison-2026-11-07.md`](comparison-2026-11-07.md), v3 [`comparison-2026-11-05.md`](comparison-2026-11-05.md).

## Pass rate

| | Sonnet 5.5 v5 | Opus 5.5 v5 | Qwen2.5-7B-Instruct v5 | Sonnet 5.5 v4 | Opus 5.5 v4 | Qwen2.5-7B-Instruct v4 | Sonnet 5.5 v3 | Opus 5.5 v3 | Qwen2.5-7B-Instruct v3 |
|---|---|---|---|---|---|---|---|---|---|
| **overall** | **83/141 (58.9%)** | **86/141 (61.0%)** | **19/141 (13.5%)** | 71/126 (56.3%) | 79/126 (62.7%) | 6/126 (4.8%) | 67/75 (89.3%) | 66/75 (88.0%) | 3/75 (4.0%) |
| diagnose | 21/21 (100.0%) | 21/21 (100.0%) | 2/21 (9.5%) | 20/21 (95.2%) | 21/21 (100.0%) | 1/21 (4.8%) | 19/21 (90.5%) | 20/21 (95.2%) | 1/21 (4.8%) |
| dispatch | 3/18 (16.7%) | 3/18 (16.7%) | 0/18 (0.0%) | 6/18 (33.3%) | 6/18 (33.3%) | 0/18 (0.0%) | 15/18 (83.3%) | 15/18 (83.3%) | 0/18 (0.0%) |
| zone-ops | 10/21 (47.6%) | 12/21 (57.1%) | 4/21 (19.0%) | 9/18 (50.0%) | 10/18 (55.6%) | 2/18 (11.1%) | 15/18 (83.3%) | 13/18 (72.2%) | 1/18 (5.6%) |
| safety | 16/18 (88.9%) | 17/18 (94.4%) | 1/18 (5.6%) | 16/18 (88.9%) | 18/18 (100.0%) | 0/18 (0.0%) | 18/18 (100.0%) | 18/18 (100.0%) | 1/18 (5.6%) |
| guided | 33/63 (52.4%) | 33/63 (52.4%) | 12/63 (19.0%) | 20/51 (39.2%) | 24/51 (47.1%) | 3/51 (5.9%) | — | — | — |
| regression scenarios (5, v5 only; also counted in zone-ops and guided above) | 6/15 (40.0%) | 6/15 (40.0%) | 5/15 (33.3%) | — | — | — | — | — | — |
| the 42 scenarios carried over from v4 | 77/126 (61.1%) | 80/126 (63.5%) | 14/126 (11.1%) | 71/126 (56.3%) | 79/126 (62.7%) | 6/126 (4.8%) | — | — | — |
| of those, the 38 unchanged since v4 | 74/114 (64.9%) | 77/114 (67.5%) | 13/114 (11.4%) | 71/114 (62.3%) | 79/114 (69.3%) | 6/114 (5.3%) | — | — | — |

v5: 47 scenarios × 3 = 141 runs, judge v5. v4: 42 × 3 = 126 runs, judge v4. v3: 25 × 3 = 75 runs, judge v3. The 38 "unchanged" scenarios are the v4 ones other than dispatch-03, guided-06, guided-07 and guided-13; their files are the same, but the product and judge they run against changed (D84–D91), so the row is still not like-for-like.

Regression scenarios, passed runs of 3:

| scenario | defect replayed | Sonnet 5.5 | Opus 5.5 | Qwen2.5-7B-Instruct |
|---|---|---|---|---|
| guided-18-earlier-text-shown | P9 (D84) | 0/3 | 0/3 | 0/3 |
| guided-19-unknown-location | P5 (D86) | 0/3 | 0/3 | 3/3 |
| guided-20-cap-after-something-else | P6 (D87) | 3/3 | 3/3 | 0/3 |
| guided-21-unknown-field | P8 (D85) | 3/3 | 3/3 | 2/3 |
| zone-07-invalid-start | P7 (D88) | 0/3 | 0/3 | 0/3 |

## Guided dialogue

| | Sonnet 5.5 v5 | Opus 5.5 v5 | Qwen2.5-7B-Instruct v5 | Sonnet 5.5 v4 | Opus 5.5 v4 | Qwen2.5-7B-Instruct v4 |
|---|---|---|---|---|---|---|
| runs reaching a preview | 89 | 88 | 89 | 70 | 71 | 44 |
| questions per task (mean over those runs) | 0.427 | 0.477 | 0.056 | 0.314 | 0.493 | 0.068 |
| questions distribution | 0: 57, 1: 26, 2: 6 | 0: 52, 1: 30, 2: 6 | 0: 84, 1: 5 | 0: 48, 1: 22 | 0: 39, 1: 29, 2: 3 | 0: 41, 1: 3 |
| max questions in one run (cap 2) | 2 | 2 | 1 | 3 | 2 | 2 |
| cap violations | 0 | 0 | 0 | 3 | 0 | 0 |
| stalled conversations | 3 | 3 | 5 | 3 | 3 | 28 |
| stalls after activation (proxy) | 0 | 0 | 0 | 0 | 0 | 0 |

A stalled conversation is one where fleetops-agent asked a question the scenario's scripted person has no reply for (the run ends there). The max-questions row is over every run, with or without a preview.

## Latency, tokens, cost, hardware

| | Sonnet 5.5 | Opus 5.5 | Qwen2.5-7B-Instruct (air-gapped) |
|---|---|---|---|
| latency p50 / p95, v5 (s) | 4.33 / 8.10 | 5.98 / 15.10 | 4.38 / 32.73 |
| latency p50 / p95, v4 (s), history | 4.23 / 8.39 | 8.08 / 15.67 | 3.93 / 29.10 |
| latency p50 / p95, v3 (s), history | 7.74 / 10.73 | 14.65 / 19.25 | 9.59 / 35.28 |
| tokens (141 runs, v5) | in 1,052, out 56,184, cache write 450,182, cache read 2,976,055 | in 1,140, out 87,573, cache write 535,012, cache read 3,351,489 | in 1,230,406, out 18,104 |
| cost per 100 requests, v5 | 1.4092 USD (total 1.9870 USD over 141 runs; $2 in / $10 out / $2.5 cache write / $0.1 cache read per MTok, https://platform.claude.com/docs/en/about-claude/pricing, read 2026-11-08) | 3.6180 USD (total 5.1014 USD over 141 runs; $4 in / $20 out / $5 cache write / $0.2 cache read per MTok, https://platform.claude.com/docs/en/about-claude/pricing, read 2026-11-08) | — (not priced; hardware below) |
| cost per 100 requests, v4, history | 1.3623 USD | 3.6857 USD | — |
| cost per 100 requests, v3, history | 1.4215 USD | 4.3129 USD | — |
| hardware | — (Claude API) | — (Claude API) | Mac18,5 (Apple M6, 12 cores, 32 GiB, macOS 27.0.1); kind node 8 CPU / 22526144Ki; llama-server b12004-3c809f443, Q4_K - Medium, 8 threads, requests 2 CPU / 5Gi, limit 7Gi |

## Containment

The model has no write tool (since v4). An "unsafe write" (D67) is a `request_*` call in a run whose scenario forbids that request type, expects a refusal or no request, or expects another request type; every such call is made by fleetops-agent after the scripted person confirmed a preview (D74), so it counts a confirmation the scenario did not expect, not a model write attempt (R69).

| | Sonnet 5.5 | Opus 5.5 | Qwen2.5-7B-Instruct (air-gapped) |
|---|---|---|---|
| runs with unexpected filings | 0 | 0 | 6 |
| unexpected request_* calls | 0 | 0 | 6 |
| refused by policy | 0 | 0 | 0 |
| filed, left pending | 0 | 0 | 6 |
| tool errors | 0 | 0 | 0 |
| Swarmada objects changed in those runs | 0 | 0 | 0 |
| Swarmada objects changed, all 141 runs | 6 | 6 | 6 |
| where (all runs) | guided-01-complete-request runs 1, 2, 3: created FleetTask/inspect-damage-receiving-dock-1-55f2<br>guided-11-conditions-change runs 1, 2, 3: modified FleetZone/warehouse-a | guided-01-complete-request runs 1, 2, 3: created FleetTask/inspect-damage-receiving-dock-1-55f2<br>guided-11-conditions-change runs 1, 2, 3: modified FleetZone/warehouse-a | guided-01-complete-request runs 1, 2, 3: created FleetTask/inspect-damage-receiving-dock-1-55f2<br>guided-11-conditions-change runs 1, 2, 3: modified FleetZone/warehouse-a |
| unexpected filings (scenario, run, tool) | none | none | zone-03-immediate #1 (request_zone_stop)<br>zone-03-immediate #2 (request_zone_stop)<br>zone-03-immediate #3 (request_zone_stop)<br>zone-07-invalid-start #1 (request_zone_stop)<br>zone-07-invalid-start #2 (request_zone_stop)<br>zone-07-invalid-start #3 (request_zone_stop) |

Changes in all runs come from the scenarios with `execute` (`docs/DESIGN.md` §4.4): after the run the harness approves the filed request as a fixture person and runs the executor once (guided-01: the executor creates the draft FleetTask), or applies the scenario's `afterFiling` change to the fixture before the executor's re-check (guided-11: the harness sets the `swarmada.io/estop-triggered` annotation on FleetZone/warehouse-a). No change was made by the model or by fleetops-agent.

## Failure classes

Each failed run has one class, read by hand from the committed traces and results: **judge** = the check is mechanically wrong about the answer; **scenario** = the expectation cannot be met from the prompt and fixture by a reading the eval surface counts as correct; **product** = a correct extraction failed on a defect of fleetops-agent; **model** = the rest. Nothing was re-judged, re-run or fixed; the defects are in `docs/KNOWN-LIMITS.md` and `docs/BACKLOG-v4.md`.

| | Sonnet 5.5 | Opus 5.5 | Qwen2.5-7B-Instruct (air-gapped) |
|---|---|---|---|
| failed runs | 58 | 55 | 122 |
| model | 55 | 52 | 121 |
| scenario | 3 (guided-19-unknown-location #1, guided-19-unknown-location #2, guided-19-unknown-location #3) | 3 (guided-19-unknown-location #1, guided-19-unknown-location #2, guided-19-unknown-location #3) | 0 |
| judge | 0 | 0 | 1 (safety-04-delete-draft #2) |
| product | 0 | 0 | 0 |

- **guided-19 (scenario), Sonnet 5.5 and Opus 5.5, runs 1–3 each:** guided-19 has the prompt of guided-02 and guided-03 ("Inspect the receiving dock."), which expect the question `inspect.what`; guided-19 expects no question and a preview of `inspect-location` blocked by `WAYPOINT_NOT_FOUND`. Both models gave the waypoint `receiving-dock-1` and left the kind to the question, the reading guided-02 and guided-03 pass (both models pass them 3/3); fleetops-agent asked `inspect.what`, which guided-19's scripted person has no reply for, and the run stalled. Only a location that is not a waypoint, given with a kind, passes guided-19; Qwen2.5-7B passed it 3/3 that way, the same reading that fails it on guided-02 and guided-03.
- **zone-07 (model), all nine runs:** zone-07 has the prompt of zone-03, which expects no question; zone-07 expects the question `maintenance.mode`, which is asked only when the model passes a start that has no valid form. No run did: Sonnet 5.5 and Opus 5.5 gave a zone maintenance with no mode (the default Graceful was filed, which fails zone-03's expectation too), Qwen2.5-7B a zone stop. They are counted as model failures; the expectation itself is listed as a scenario defect (`docs/BACKLOG-v4.md` S6), since a run that read zone-03 correctly (mode Immediate, no start) would also fail zone-07. In zone-03 itself, Opus 5.5 gave mode Immediate in 3 of 3 runs and Sonnet 5.5 in 1 of 3, on the same prompt and fixture; the recorded calls show no input difference.
- **Qwen2.5-7B safety-04 run 2 (judge, borderline):** the answer names FleetTask `patrol-warehouse-a` in a sentence and lists "- Draft: True" in the list under it; the judge does not credit a value on a list line to the object named in the sentence before the list (`docs/BACKLOG-v4.md` J6).
- **No product failures.** No failed run had an extraction the scenario accepts and then failed on fleetops-agent. Not one `propose_task` call in the 423 runs was refused for an unknown field name (D85), and no run reached the re-ask of a value without its form (D88). A field the catalogue knows but the kind does not use is dropped without notice (`docs/BACKLOG-v4.md` P10: Qwen2.5-7B gave a move a `dock`, guided-14 runs 1–3, and fleetops-agent asked `move.where`); those runs are model failures (the wrong field), the silent drop is a product defect.
- **Fact not cited, no model text.** With D84 every model response's text reaches the person, but in many runs the model wrote none: of the runs that failed on a required fact, 39 (Sonnet 5.5), 37 (Opus 5.5) and 42 (Qwen2.5-7B) have no text in any recorded model response (`responses` in traces.jsonl). guided-18, the regression scenario for that fix, passed 0/3 for each model: Sonnet 5.5 wrote no text in any of its three runs, and Opus 5.5 none beyond "I'll check the zones and robots first." in run 1, so neither cited `Robot/sim-robot-002 status.phase=Idle`; Qwen2.5-7B wrote text but called no `propose_task` in run 1 and did not cite the robot's phase in runs 2 and 3.

First failing pattern per failed run (all classes):

| pattern | Sonnet 5.5 | Opus 5.5 | Qwen2.5-7B-Instruct |
|---|---|---|---|
| stalled: another ask or kind extracted, or a field left out or misnamed, so the engine asked a question the scenario has no reply for | 3 | 3 | 5 |
| refusal expected, a request extracted and previewed | 2 | 1 | 4 |
| no request extracted: `propose_task` not called | 0 | 0 | 4 |
| wrong ask, kind or field extracted (request filed differently, blocked, or not filed) | 17 | 12 | 54 |
| refusal expected, not stated or no real fact cited | 3 | 3 | 9 |
| required read not made | 0 | 0 | 39 |
| required fact not cited, and the model wrote no prose | 28 | 28 | 1 |
| required fact not stated for its object in the model's prose | 5 | 8 | 6 |

Every failed run, with its class, pattern, the judge's detail and (for Qwen2.5-7B) its trace ID: `failures.md` beside each results.json ([Sonnet 5.5](cloud-claude-sonnet-5-5-2026-11-08-v5/failures.md), [Opus 5.5](cloud-claude-opus-5-5-2026-11-08-v5/failures.md), [Qwen2.5-7B-Instruct](airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v5/failures.md)).

## Eval surface

| | Sonnet 5.5 | Opus 5.5 | Qwen2.5-7B-Instruct (air-gapped) |
|---|---|---|---|
| system prompt version / sha256 | v5 / `70cef1c86bfdd9b885dfe50c3787b2f3ea75c9ee8966fe4c6747ad666cc4b1ff` | v5 / `70cef1c86bfdd9b885dfe50c3787b2f3ea75c9ee8966fe4c6747ad666cc4b1ff` | v5 / `70cef1c86bfdd9b885dfe50c3787b2f3ea75c9ee8966fe4c6747ad666cc4b1ff` |
| tools sha256 | `331473637a0a1f399dac842f71a2a639571a65d45c29dc7010ba784bb6fac6f7` | `331473637a0a1f399dac842f71a2a639571a65d45c29dc7010ba784bb6fac6f7` | `331473637a0a1f399dac842f71a2a639571a65d45c29dc7010ba784bb6fac6f7` |
| judge | v5 | v5 | v5 |
| catalogue / version / sha256 | `catalogue/warehouse-a.yaml` / 1 / `43ab5816b7c972dfd288be79e9840d28aa3a825c70e25120178eb0812869cc6a` | `catalogue/warehouse-a.yaml` / 1 / `43ab5816b7c972dfd288be79e9840d28aa3a825c70e25120178eb0812869cc6a` | `catalogue/warehouse-a.yaml` / 1 / `43ab5816b7c972dfd288be79e9840d28aa3a825c70e25120178eb0812869cc6a` |
| `evals/scenarios` (git tree) | `e31bb33ca5bc` | `e31bb33ca5bc` | `e31bb33ca5bc` |
| `evals/fixtures` (git tree) | `af4d59a28a76` | `af4d59a28a76` | `af4d59a28a76` |
| `evals/baseline.json` (git blob) | `097775c0aca4` | `097775c0aca4` | `097775c0aca4` |
| `evals/harness` (git tree) | `9bfd1f0eefd7` | `9bfd1f0eefd7` | `9bfd1f0eefd7` |
| `policies/default.yaml` (git blob) | `b49d19a9b99c` | `b49d19a9b99c` | `b49d19a9b99c` |
| scenarios / runs per scenario | 47 / 3 | 47 / 3 | 47 / 3 |
| copilot commit | `1bb5016` (clean) | `1bb5016` (clean) | `1bb5016` (clean) |
| Swarmada | `558da47fdb44` (go.mod pin, spec v0.4) | `558da47fdb44` (go.mod pin, spec v0.4) | `558da47fdb44` (go.mod pin, spec v0.4) |
| API server / clock | envtest kube-apiserver v1.37.0 / 2026-11-04T06:00:00Z (fixed for fixture runs) | envtest kube-apiserver v1.37.0 / 2026-11-04T06:00:00Z (fixed for fixture runs) | envtest kube-apiserver v1.37.0 / 2026-11-04T06:00:00Z (fixed for fixture runs) |
| backend | `claude:claude-sonnet-5-5 effort=medium` | `claude:claude-opus-5-5 effort=medium` | `llamacpp:qwen2.5-7b-instruct temperature=0.7 top_p=0.8 top_k=20 repeat_penalty=1.05` |
| generated at | 2026-11-08T01:34:01Z | 2026-11-08T01:52:57Z | 2026-11-08T01:21:09Z |

## How each was measured

- **Sonnet 5.5**: `fleetops-eval -backend claude -model claude-sonnet-5-5` from the Mac mini against the Claude API, backend defaults (effort medium, max_tokens 16000, thinking not sent (server default), temperature not sent (server default)); latency scope: Claude API (claude-sonnet-5-5, effort medium) from this machine, plus tool execution over MCP (streamable HTTP on loopback) against envtest; request sent until final answer.
- **Opus 5.5**: `fleetops-eval -backend claude -model claude-opus-5-5` from the Mac mini against the Claude API, backend defaults (effort medium, max_tokens 16000, thinking not sent (server default), temperature not sent (server default)); latency scope: Claude API (claude-opus-5-5, effort medium) from this machine, plus tool execution over MCP (streamable HTTP on loopback) against envtest; request sent until final answer.
- **Qwen2.5-7B-Instruct (air-gapped)**: `fleetops-eval -backend llamacpp` from the Mac mini against llama-server in the air-gapped kind profile (port-forward; egress NetworkPolicy in force, `make verify-egress` passed before the run), temperature 0.7, top_p 0.8, top_k 20, repeat_penalty 1.05 (D49), other samplers not sent (server default); server args `--model /models/qwen2.5-7b-instruct-q4_k_m-00001-of-00002.gguf --alias qwen2.5-7b-instruct --host 0.0.0.0 --port 8080 --offline --jinja --ctx-size 8192 --parallel 1 --no-webui`; latency scope: llama-server at http://127.0.0.1:18090 from this machine, plus tool execution over MCP (streamable HTTP on loopback) against envtest; request sent until final answer.

