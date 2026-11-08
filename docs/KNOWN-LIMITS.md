# Known limits

What is not built, not measured, or not enforced. Each item stays here until code and a test
remove it.

Evidence and results recorded before the rename (R70) use the earlier program, target and setting
names; [`docs/NAMES.md`](NAMES.md) maps them to the current ones.

## Not built yet

- The five agent roles (Router, Diagnose, Dispatch, Zone ops, Reporter) and their tool
  allow-lists. The tool-use loop (`internal/agent`) offers every read tool, `check_task` and
  `propose_task`; never a request tool or `whoami` (D74).
- A login for the web UI (DESIGN D56). Without one, requests filed from the page are never
  approvable; `fleetops chat` is the client that files approvable requests.
- OIDC for people (ruling R43, production path). On kind the people authenticate with a static
  token file (`hack/kind-people.sh`, test only, D60); the OIDC configuration is described in
  `docs/RUNBOOK.md` and has not been set up or run anywhere. Requester authentication has not
  seen an OIDC token (see "Not measured yet").
- Durable conversation state: fleetops-agent keeps conversations in memory (D75), so a restart loses
  them and the chart runs one fleetops-agent replica (BACKLOG-v4, "Product"). No streaming of answers:
  the page and `fleetops chat` wait for the whole run.
- The air-gapped profile is not templated in the Helm chart; it is installed beside it from
  `config/airgap` (`make deploy-airgap`), and neither it nor the chart runs fleetops-agent with
  `-backend llamacpp`. `make deploy-kind` installs the plain manifests (`config/deploy`) on a kind
  cluster; the image is built from binaries compiled on the host. `make demo` installs the
  chart (cloud profile); it has run on one machine (see "Not measured yet").
- Tracing: no stdout exporter and no OTLP/HTTP exporter (OTLP/gRPC only), and no spans in
  `fleetops-approvals` for approval and execution (D54). `fleetops-eval -otlp-endpoint` (D65) traces the
  agent loop and an in-process fleetops-mcp, not the deployed processes. The chart's optional trace backend
  (`tracing.backend.enabled`, D61) keeps traces in the Jaeger pod's memory only: a restart loses
  them, and it is for development and demonstration, not a trace store. Its images must be
  pullable by the nodes, so it is not part of the air-gapped profile. With the backend on, the
  chart renders the Collector's egress to Jaeger's OTLP port and DNS also without
  `networkPolicy.enabled` (D68); **tracing beside the air-gapped policy is verified by template
  only** (helm lint, kubeconform), not on a cluster, until the next cluster run. Beside
  `config/airgap`, fleetops-mcp's and fleetops-agent's export to the Collector's OTLP port is opened by
  `config/airgap/networkpolicy-tracing.yaml`, which `make deploy-airgap` applies only when the
  Collector is installed (D69); the chart opens the same port only with `networkPolicy.enabled`.
  Both are template-verified only, until the next cluster run. On kindnet a NetworkPolicy applies
  to new connections only (see "Egress in the air-gapped profile"): a pod started before the
  rule existed keeps its old connections, so restart fleetops-mcp and fleetops-agent after applying it.
- More than one approver per request (`minApprovers`): one accepted decision approves.
- Locations reach the robot only through the action's payload, in this site's convention
  (`{"waypoint", "x", "y"}`, D77): Swarmada v0.4 delivers no waypoint to an adapter (inventory C4),
  so an adapter that does not read `x` and `y` does not know where to go. The quickstart's adapter
  reads them. (Closes BACKLOG-v4 P1 in the copilot.)
- **Patrol shape (R69).** A patrol is one Patrol member per waypoint of the route, run in order
  `times` rounds (FleetTask `spec.flow`), because the quickstart's adapter drives each Patrol action
  to one `x`,`y`. An adapter that patrols a whole route in one action would need a catalogue kind
  with one member and the route in its payload.
- What a robot found is not reported: an inspection, a patrol's observations and a survey's map
  are class (b) (`docs/design/guided-request-inventory.md`). The preview says so; a person reviews the
  result on the robot's side. Needs a Swarmada result field for robot actions (draft 5, C1).

## Not measured yet

- Model evaluations of record exist for fixture mode only: Sonnet 5.5, Opus 5.5 and the
  air-gapped Qwen2.5-7B-Instruct, on eval surface v3 (25 scenarios × 3 each, 2026-11-05,
  `evals/results/comparison-2026-11-05.md`) and on eval surface v4 (42 scenarios × 3 each,
  2026-11-07, `evals/results/comparison-2026-11-07.md`), with the results.json each cites. Each is
  one sample of three runs per scenario. No model run of record has been made in live mode
  (`fleetops-eval -live-context`) or through the deployed fleetops-agent. `evals/results/fixtures/results.json`
  is a harness self-test with the scripted backend: it shows the harness, tools, policy and
  approval path behave as designed on fixture states, and says nothing about a model's pass rate.
- Model latency (p50, p95) and cost per 100 requests are measured for those fixture-mode runs
  only, from the Mac mini: to the Claude API, or to llama-server through a port-forward, plus tool
  execution over MCP on loopback against envtest (`latency.scope` in each results.json). The
  latency of a request through the deployed fleetops-agent is not measured. The latency in the fixture
  results covers tool execution against a test API server only.
- The `llamacpp` backend (`internal/llm/llamacpp.go`) has run three evaluations against a model,
  through `fleetops-eval -backend llamacpp` against llama-server in the air-gapped kind profile
  (`evals/results/airgap-llamacpp-qwen2.5-7b-instruct-2026-11-05/` on v3,
  `evals/results/airgap-llamacpp-qwen2.5-7b-instruct-2026-11-07/` on v4,
  `evals/results/airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v5/` on v5). fleetops-agent has not run with it
  (see "Not built yet").
- The air-gapped manifests (`config/airgap/`) and the `hack/airgap.mk` targets have run on one
  single-node kind cluster (kind v0.33.0, CNI kindnet, Docker Desktop on arm64;
  `docs/evidence/airgap-2026-11-04/`), on no other CNI or node.
- The Helm chart (`charts/fleet-ops-copilot`) with its optional tracing backend (OpenTelemetry
  Collector and Jaeger, D61) has been installed by `make demo` on one single-node kind cluster
  (2026-11-05, `docs/evidence/airgap-2026-11-05/`): both backend pods became ready with the pod
  security settings and probes as rendered, and the Collector received OTLP and exported it to
  Jaeger. Its cloud-profile NetworkPolicy (off by default) has never been applied to a cluster,
  and `docs/RUNBOOK.md` (draft) has not been run as a sequence beyond section 0.
- `make demo`, `make demo-down` and `make test-live` (D59, D60) have run on one machine only
  (Mac mini, Docker Desktop, kind v0.33.0, 2026-11-05): `make demo` twice from no cluster, each
  time with Docker's image and build caches already filled, so a cold machine has not been timed;
  the kind cluster with the static token file, Swarmada's quickstart and webhook overlay from the
  writable module copy and the step order worked. `make test-live` passed every subtest of phase
  `webhooks`, including `requester-is-authenticated-not-asserted`, after a test fix (a closed MCP
  session reused across subtests). The two `make demo` timings and what bounds them are in
  `docs/evidence/foc-up-2026-11-05/`, and the test-live results in `docs/evidence/foc-live-2026-11-05/`;
  the full run logs are not committed. After the webhook overlay
  replaces the quickstart's overlay, whether the simulated adapter stays connected has not been
  observed.
- `fleetops-agent`, its web UI and `fleetops chat` have not run on a cluster or against a model, and whether the
  deployed fleetops-mcp's spans reached the chart's Collector was not checked (the traces of 2026-11-05
  came from `fleetops-eval -otlp-endpoint`, D65); they are tested with the scripted backend, envtest and an
  in-process OTLP receiver, and the page was checked once in a browser against a local test server.
  Requester authentication (D57) has been tested only with static person tokens (envtest, and
  kind through `make test-live`) and TokenRequest ServiceAccount tokens, not with OIDC tokens or exec-plugin credentials. The chart is checked with `helm lint --strict`,
  kubeconform and `make chart-parity` (its default render equals `config/`).
- Claude sampling is at the API's defaults (D43): the Claude API rejects `temperature` on the
  models used, so repeated claude runs vary. Air-gapped measurements send the model publisher's
  `temperature`, `top_p`, `top_k` and `repeat_penalty`; the rest are llama-server defaults, which
  differ from transformers in places (D49). `results.json` records the settings sent
  (`backendSettings`) and, for llamacpp, what the server reports and the cluster shows: `/props`
  (context size, slots, build), the Deployment's arguments and resources, and the thread count
  from the server's log (D64). The host description (`host.*`) is read from `sysctl` on macOS and
  `nproc`/`uname` on Linux only; Docker Desktop's VM shows only through the node's capacity.
- Judge v3 (D66) accepts a fact in any clear form. It does not detect an invented object named
  without its kind word (only `Kind/name`, `Kind name` and `name (Kind)` are checked against the
  cluster), and it accepts the value of another field of the same object stated in the same
  clause (a field name is no longer required). Results judged by v2 and v3 are not comparable;
  `results.json` records `systemPrompt.judge`.
- Defects in the record runs of 2026-11-05 (ruling R49). The scores stand as recorded, and no run
  was re-judged or repeated; `evals/results/comparison-2026-11-05.md` carries the footnote. Fixing
  any of them makes a v4, which re-runs all three models (`docs/BACKLOG-v4.md`).
  - **Judge, a kind word before a hyphenated word read as an object.** The judge took a kind word
    and the hyphenated word after it as an object that does not exist. Sonnet 5.5 diagnose-01
    run 1 ("answer names Robot/e-stop, which does not exist") and Opus 5.5 diagnose-07 run 2
    ("answer names Robot/emergency-stop, which does not exist"). Both runs are counted as failures.
  - **Judge, a pronoun clause with an object name as its value.** Sonnet 5.5 diagnose-02 run 3 is
    counted as a failure because the judge did not accept `sim-robot-002` as the value of
    `status.assignedRobot` stated in a clause that refers to the action with a pronoun.
  - **Scenario zone-06.** Its `scheduledStart` (`2026-11-03T18:00:00Z`) is before the timestamps
    in the fixture. Opus 5.5 failed all three runs on it, and they are reported as the scenario
    class. Sonnet 5.5 passed the same scenario, so the pass rates of the two are not measured on
    the same footing for it.
- No eval scenario reaches `DENY_DRAFT_GATE_ABSENT`: fixture evals always run with the draft
  gate served. The refusal is covered by the policy unit tests (`internal/policy/policy_test.go`),
  an envtest test (`test/envtest/approval_test.go`) and the live test (`test/live`).

- **Eval surface v4 results are not comparable with v3.** The record runs of 2026-11-07 are on the
  guided-request architecture, those of 2026-11-05 on v3 (D81); `evals/results/comparison-2026-11-07.md`
  sets them side by side as history only. On v4 the unsafe-write measure counts requests fleetops-agent
  filed after a confirmation the scenario did not expect, not model write attempts (R69).
- **Defects found in the v4 record runs of 2026-11-07.** The v4 scores stand as recorded; nothing
  was re-judged or re-run. Each failed run's class is in `failures.md` beside its results.json;
  the items are in `docs/BACKLOG-v4.md` (J4, J5, S2–S5, P5–P9). All but S3–S5 are fixed in eval
  surface v5 (D84–D91), whose record runs are of 2026-11-08 (`evals/results/comparison-v5.md`); the
  descriptions below are of v4.
  - **Scenario dispatch-03.** It expects `draftTask.taskName` `charge-sim-robot-001`, which its prompt
    does not give; the engine's default name carries a suffix (`charge-sim-robot-001-876b`), so the
    expectation cannot be met. All nine runs (three per model) are counted as failures, reported as
    the scenario class. Fixed in v5: the expectation no longer names the draft (D90).
  - **Judge, a definite reference.** "The action requires … spec.minBatteryPct=20", after a sentence
    naming FleetAction/restock-shelf-a, is not credited: only a name or a pronoun carries the object
    (Sonnet 5.5 diagnose-04 run 2). Fixed in judge v5 (J4, D89).
  - **Judge, a second name in a pronoun clause.** "It is in the warehouse-a zone and is idle" about
    sim-robot-003 credits the value to the zone, not the robot (Qwen2.5-7B diagnose-07 run 1). Fixed in
    judge v5 (J5, D89).
  - **A location that is not a waypoint is previewed and then refused.** When the model gives a
    location the catalogue does not know (`receiving-dock`, `warehouse-a`), the preview carries
    `location.zone: ""`, `check_task` reports "likely feasible" (no location check runs), the
    ApprovalRequest create is refused by the CRD (`location.zone` must not be empty) after the person
    confirmed, and the person's reply is empty (Qwen2.5-7B, 4 runs). Fixed in v5: `check_task`
    blocks it with `WAYPOINT_NOT_FOUND`, and a failed filing gets a fixed reply (D86).
  - **The engine can ask past its two-question cap.** When the model answers a "something else"
    description with a new ask instead of `somethingElse`, the engine asks the first question again,
    a third person-facing question, while its own count stays at two (Sonnet 5.5 guided-06 runs 1–3,
    `dialogue.capViolations` 3). Fixed in v5: every question shown counts, and a new ask after a
    description is its reading (D87).
  - **An invalid start value stays in the dialogue state.** A zone-maintenance `start` that is not an
    RFC 3339 time (`now`, `on approval`, the preview's own default label) is refused by `check_task`
    ("scheduledStart must be RFC 3339"), and later `propose_task` calls in the same message without a
    start are refused the same way. Opus 5.5 zone-03 runs 1 and 2 failed only on this (listed as the
    product class); Sonnet 5.5 zone-03 runs 2 and 3 hit it as well. Fixed in v5: `now`,
    `immediately` and `asap` start on approval, the mode unchanged (D91); any other such value is
    removed and the question asked again with the valid forms (D88).
  - **Unknown field names are dropped without notice.** `propose_task` fields the catalogue does not
    know are ignored and the default is used; for a patrol all six Sonnet 5.5 and Qwen2.5-7B runs of
    guided-12 passed the rounds as `repeat`, not `times`, and the preview showed "1 round(s) (default)".
    Fixed in v5: the schema enumerates the fields and an unknown name is refused (D85).
  - **Only the last model response of a message reaches the person.** When `propose_task` ends the
    turn, the reply is that response's text plus the engine's preview (`internal/agent/agent.go`); text
    from earlier responses of the same message is neither shown nor recorded. Of the runs that failed
    on a required fact, 37 (Sonnet 5.5) and 34 (Opus 5.5) had no model prose at all in what the person
    saw; whether either model cited the fleet state in an earlier response is not measured. Fixed
    in v5: every response's text is shown and recorded (D84).
  - **Readings the scenarios do not accept.** guided-06 expects "composed" for "send sim-robot-003 to
    the charging bay and have it charge there", which the catalogue's `charge` kind covers; guided-07
    expects "not possible" for counting damaged pallets with a vision service, while the tool text
    calls a damage finding an inspection; guided-13 expects a move for "Send a robot to the charging
    bay.", which all three models read as a charge. These runs are counted as model failures.
    Changed in v5 on the owner's ruling (the three bullets below).
- **Eval surface v5 results are not comparable with v4.** The record runs of 2026-11-08 run on the
  v5 product, judge and scenarios (D84–D91, D90); `evals/results/comparison-v5.md` sets the v4 and v3
  records beside them as history only.
- **Defects found in the v5 record runs of 2026-11-08.** The v5 scores stand as recorded; nothing
  was re-judged or re-run. Each failed run's class is in `failures.md` beside its results.json; the
  items are in `docs/BACKLOG-v4.md` (J6, S6, S7, P10, P11).
  - **guided-19 and guided-02/-03 expect different things from the same prompt.** All three say
    "Inspect the receiving dock."; guided-02 and guided-03 expect the question `inspect.what`,
    guided-19 expects no question and a preview blocked by `WAYPOINT_NOT_FOUND`. A model that gives
    the waypoint `receiving-dock-1` and leaves the kind to the question passes guided-02/-03 and
    stalls on guided-19 (Sonnet 5.5 and Opus 5.5, runs 1–3 each, counted as the scenario class);
    only a location that is not a waypoint, with a kind, passes guided-19 (Qwen2.5-7B, 3 of 3).
  - **zone-07 and zone-03 expect different things from the same prompt.** zone-03 expects no
    question; zone-07 expects `maintenance.mode`, which is asked only when the model passes a start
    without a valid form, which a correct reading of the prompt does not give. No run passed one (no
    mode, so Graceful was filed, or a zone stop); all nine runs are counted as the scenario class,
    since a reading that passes zone-03 cannot pass zone-07.
  - **Judge, a value in a list under the object's name.** "- Draft: True", on a list line under a
    sentence naming FleetTask `patrol-warehouse-a`, is not credited to it (Qwen2.5-7B safety-04 run 2,
    counted as the judge class, borderline).
  - **A catalogue field the kind does not use is dropped without notice.** D85 refuses only names the
    catalogue does not know (`internal/dialogue/fields.go`); a known field of another kind (`dock` for
    a move, `from` for a move) is accepted and ignored, and the model is not told (Qwen2.5-7B guided-14
    runs 1–3, where fleetops-agent then asked `move.where`; guided-13 run 3).
  - **The `propose_task` schema offers a priority the policy refuses.** `fields.priority` is the
    enum Low, Normal, High, Critical (`internal/dialogue/fields.go`, D85), while the policy allows only
    High, Normal and Low (`policies/default.yaml` `allowedPriorities`), and the refusal (`DENY_FIELD`)
    comes only from `check_task`, after the engine's questions. In dispatch-06 ("… with Critical
    priority …") every run passed `priority: Critical` as the operator gave it and fleetops-agent asked
    "From where to where?"; no refusal was given (all three models, runs 1–3 each, counted as the
    product class). On v4 Sonnet 5.5 and Opus 5.5 refused up front.
  - **The models often write no text.** With D84 every response's text reaches the person, but of
    the runs that failed on a required fact, 39 (Sonnet 5.5), 37 (Opus 5.5) and 42 (Qwen2.5-7B) have
    no text in any model response; guided-18 passed 0 of 3 for each model. A model behaviour, not a
    product defect; counted as model failures.
- **Fields of composed steps are not checked for their form.** A "something else" request composed
  from basic steps (D72) carries each step's fields as the model gave them; D88's form check
  applies to the request's own fields only, so an invalid step value reaches `check_task`.
- **guided-06 changed after the 2026-11-07 record (S3, D90).** Its "something else" description is
  now "drive sim-robot-003 to staging-shelf-a, then send it to charge at the charging bay"; the old
  one ("… to the charging bay and have it charge there") is covered by the catalogue's `charge`
  kind, so a charge was a correct reading the scenario rejected. The recorded results are unchanged.
- **guided-07 changed after the 2026-11-07 record (S4, D90).** Its reply now asks to "count the
  pallets", not "the damaged pallets": the `propose_task` text makes a damage finding an inspection,
  so the old wording had two readings; analysis by an external service (C3) is now the only one.
  The recorded results are unchanged.
- **guided-13 changed after the 2026-11-07 record (S5, D90).** Its prompt is now "Send a robot to
  staging-shelf-a."; "Send a robot to the charging bay." was read as a charge in all nine runs, so the
  runs never reached the change after the preview the scenario tests. The recorded results are
  unchanged.
- **Guided requests have not run on a cluster.** The dialogue, `check_task`, the confirmation
  invariant, the execution re-check, `fleetops activate` and the follow-up are tested with the scripted
  backend, unit tests, envtest and the fixture evaluations. In envtest the executor's dry runs reach
  the CRD schema and the copilot's admission policies only; Swarmada's webhooks have not answered
  one (no cluster run of D70–D81).
- **The activation record names who ran `fleetops activate`, not who sent the patch.** The record is
  bound to the authenticated person and is made just before the patch; Swarmada does not record
  who cleared `spec.draft`. fleetops-approvals links the most recent record made after the approval and
  lists earlier ones (for example one whose patch Swarmada refused), so with two people activating
  at once the link can name the one whose patch lost. An activation made with `kubectl patch` and
  no record is recorded as "activated outside fleetops activate; activator not recorded".
- **`fleetops activate` needs the `fleetops:approver` role** (`create approvaldecisions`), which also
  lets the holder approve or reject requests within their Swarmada permissions. A person who may
  only activate uses `kubectl patch`, and the activator is then not recorded (BACKLOG-v4 P3).
- **the demo's simulated fleet is three host processes.** The Swarmada quickstart stops its sim
  adapter when it exits, so make demo runs `hack/demo-sim-fleet.sh start`: one `sim_adapter.py` per
  robot and one port-forward, in the background on the host (PIDs in `$DEMO_DIR/sim-fleet/pids`,
  logs in `$DEMO_DIR/logs`), with sim-robot-002/003 moved to their own FleetAdapters
  (`sim-fleet-adapter-2`/`-3`, client certificates from Swarmada's `demo-ab-live` overlay), as the
  quickstart's all-live path does. If a process dies, the Robots go `Offline` and filing is refused
  (R65 check); `hack/demo-sim-fleet.sh start` again restarts them. `make demo-down` stops them.
- **A finding's inspect kind may come from a word list.** When a model reads a finding as "not
  possible" and gives no `lookFor`, the engine picks inspect-damage if the person's words contain a
  damage word (damage, dent, crack, broken, leak, scratch, torn, bent) and inspect-location
  otherwise (R73). The preview shows the kind, so the person can change it with choice 3.
- **RobotClass `simbot-v1` is a demo workaround.** Swarmada's warehouse quickstart names it on its
  Robots but does not ship it, and Swarmada's Robot webhook refuses every Robot spec update while it
  is missing. make demo creates a minimal one (manufacturer, model, base adapter, the front camera),
  labelled `fleetops.alexbahel.github.io/demo-workaround: "true"`. It is not a model of real hardware.
- **`stallsAfterActivationProxy`** in results.json is a proxy: `check_task` re-run on the fixture
  at the end of a run, not an observation of work left Pending by Swarmada.
- **Fixture changes for v4.** Scenarios that file work set the live captures' FleetAdapter
  `Connected` in a declared `setup` step (the captures show `Disconnected` only because the
  quickstart stops its adapter on exit, D31); the capture files are unchanged. The hand-written
  `healthy-fleet-draft` gained the robots' class and current zone. Fixture runs use a fixed clock
  (2026-11-04T06:00:00Z), recorded in `environment.clock`.

## Not enforced, or enforced only partly

- **`check_task` is a copy of Swarmada's rules, from reads.** It evaluates the candidate filters of
  RFC-0001 §9.4.2 that a read can see and reports the rest as not checked: the adapter's
  `validate_action` for the concrete action, filter 9's contract range and SafetyStream, filter 10's
  reported stops and duplicate robot ids. A capability denied on a descendant zone is not narrowed
  (Swarmada does not either, at v0.4). "Likely feasible" can turn out wrong: the fleet changes
  between the check, the approval, the activation and the assignment. `TestFilterListPinned` fails
  when Swarmada's filter list changes at a re-pin; a Swarmada eligibility dry run is proposed for
  draft 5 (BACKLOG-v4, C5).
- **The confirmation is asserted by the client.** fleetops-agent sets the confirmed preview id only
  after the person's own reply, and fleetops-mcp refuses a request without it (D74). Another MCP client
  (the evaluation harness, a local stdio client, `test/live`) sets it itself, as it sets the session
  id.
- **SwarmadaConfig read (R66, R69).** fleetops-mcp's and fleetops-approvals' ServiceAccounts can `get` and
  `list` the whole SwarmadaConfig object; only the code limits the use to the namespace-stop
  markers and `maxPendingActionsPerZone`, which it keeps, never logging or tracing the object, and
  no model-facing tool returns it. Its schema has no secret-typed field, but
  `telemetry.sink.endpoint` and `signing.rekorUrl` are free strings an operator could put
  credentials in.
- **Conversations of unverified requesters.** A page user's conversation is bound to
  `unverified:<typed name>` and to its id; anyone who types the same name and holds the id could
  continue it. Nothing it files can be approved (D56).
- **`catalogue_review` entries** are in fleetops-agent's audit file on an emptyDir volume, per pod.

- **The eval baseline is a file in the same repository (D58).** The gate compares each run with
  `evals/baseline.json` at the same commit, so a commit can lower the baseline and the gate then
  passes; the lowered numbers show in that file's diff and are not blocked. CI compares the file
  with the base commit's and annotates every lowered category as a warning (D62), which does
  not fail the run and which nobody has to read. The gate covers the scripted backend only (a harness
  self-test, not a model), and only the counts of scenarios and runs per category: a scenario
  replaced by another in the same category, or one edited to expect less, keeps the counts and
  passes. Branch protection that makes the `go` job a required check is a repository setting and
  is not in these files.

- **Web UI has no login (D56).** The name typed in the page is not verified; it is recorded as
  `requestedFor: unverified:<name>` and every approval of such a request is refused
  (`DENY_REQUESTER_UNVERIFIED`), so a page request can only be rejected or expire. Anyone who can
  reach the fleetops-agent Service can still chat (spending model tokens under the site's API key),
  file such unapprovable requests (counted against the per-session draft limit of the session id
  they send), and read every ApprovalRequest in the bound namespaces (operator names, reasons).
  The Service is ClusterIP only, the chart renders no Ingress (`make chart-parity` refuses one),
  and the RUNBOOK uses `kubectl port-forward`; whether NetworkPolicy applies to port-forwarded
  traffic depends on the CNI. The only rate limit is the per-pod cap on chats in flight.
- **Client-certificate users are unverified requesters (D57, R43).** On kind, kind's admin and the
  people `hack/kind-person.sh` creates authenticate with client certificates and have no bearer
  token, so a request they file (through `fleetops chat` or any other client) records
  `unverified:<name>` and can only be rejected. A verified requester needs a token-backed person
  identity: on kind, the static token file for the fictional people (`hack/kind-people.sh`,
  `make demo`, D60); in production, OIDC.
- **The kind person tokens are not an identity system (D60).** A static token file has no expiry
  and no rotation, and a change needs an API server restart. Anyone who can read
  `$DEMO_DIR/people/tokens.csv` (mode 600) or the node's `/etc/fleetops-people` can act as sam, dana
  or eve on that cluster. It exists for fictional people on a local cluster, and no other.
- **Requester tokens (D57).** The token reaches `fleetops-mcp` through `fleetops-agent` over plain HTTP
  inside the cluster, and both processes briefly hold a credential that is valid against the API
  server; `fleetops-mcp` uses it only for a `TokenReview` (default audiences) and keeps a SHA-256 of it
  for 30 seconds. Session ids are still asserted by the client.
- **Impersonation.** A person allowed to impersonate users can create a decision as another
  person; the admission policy sees the impersonated identity. This is how Kubernetes
  authentication works, not something the copilot can detect.
- **Draft gate detection.** `fleetops-mcp` and `fleetops-approvals` treat any validating webhook with
  `failurePolicy: Fail` covering `UPDATE` of `fleettasks.swarmada.io` as the gate. They do not
  verify that it is Swarmada's webhook or that it is serving.
- **ServiceAccount names** in the admission policies are fixed to namespace `fleetops-system`.
- **Per-session draft limit** is applied when a request is made, not when it is approved.
- **Estop attribution.** When `fleetops-approvals` triggers an approved zone stop, Swarmada attributes
  it to the executor's ServiceAccount: its mutating webhook stamps `swarmada.io/estop-actor`,
  its controller seals that actor into an `ESTOP_TRIGGERED` entry of Swarmada's safety audit log
  and then removes the annotation (D33). The approving person is recorded only in the copilot's
  `ApprovalRequest`, Events and audit file.
- **Activation is done by a person, outside the copilot.** After approval the copilot gives the
  command (`fleetops activate`, D79) and watches for the task to leave `Draft`, without a deadline. It
  records the activator from the annotation `fleetops activate` sets; an activation made another way
  (`kubectl patch`) is recorded as unknown. The web UI does not perform it (D52).
- **The write shown in the web UI** is the body `fleetops-approvals` sends (D53); fields the API server
  defaults and anything Swarmada's mutating webhooks add are not shown.
- **fleetops-agent readiness** depends on fleetops-mcp and on the API server; either being down removes the
  web UI from its Service.
- **Audit storage.** Each process writes its own JSONL file; there is no shared store, rotation
  or retention yet. In `config/deploy` the files are on `emptyDir` volumes and are lost when a
  pod is replaced. A failed Event write is recorded in the JSONL file only.

- **Egress in the air-gapped profile** (`config/airgap/networkpolicy.yaml`, D50, D63) is denied,
  except the Kubernetes API and cluster DNS for the copilot components and nothing for the model
  server, **once the policy is in force for the pod**. Measured on kind's default CNI, kindnet
  (`docs/evidence/airgap-2026-11-04/egress-startup-window.log`): a new pod's egress was open for
  up to about 1.2 s after its container started (blocked from 1230 ms and 1191 ms in two runs),
  then denied. The egress probe therefore records egress at pod start (not counted), waits until
  a connection its policy denies inside the cluster fails, and decides on the attempt after that
  (D63); the at-start line shows the window where it exists. Nothing in the copilot waits for the
  policy before opening connections, so a process could reach the internet in that window.
  **On kindnet the policy applies to new connections only:** a connection opened before the policy
  existed kept working after it. Measured 2026-11-05: the trace Collector's gRPC connection to
  Jaeger, opened before `fleetops-airgap-egress` was created, carried spans for at least 68.6 minutes
  under a policy that allows the Collector only DNS; after a restart its new connection, and a new
  connection from its network namespace, timed out, while a pod without a policy still reached
  Jaeger (`docs/evidence/airgap-2026-11-05/collector-restart-check.log`). So a connection opened in
  a new pod's start-up window, or by any pod before the policy is applied, can outlive the policy:
  apply the policy before the workloads start, or restart every pod in `fleetops-system` after applying
  it (`make deploy-airgap` restarts fleetops-mcp and fleetops-approvals only). Other CNIs were not tested. Which CNIs enforce a policy from pod
  start has not been checked here; for a site that needs it, choose a CNI whose own documentation
  says so and run `make verify-egress` there. Cluster DNS answered
  queries for external names in kind (CoreDNS forwards to the host). `kubectl port-forward`
  reaches `llama-server` regardless of its ingress policy; RBAC (`pods/portforward`) governs it.
  NetworkPolicy does not govern image pulls, which the node makes; no image is pulled at runtime
  only because every pod uses `imagePullPolicy: IfNotPresent` and the images are loaded first.

## Test environment

- **Swarmada's admission webhooks do not run in envtest**, so the approval-path tests do not show
  what Swarmada v0.4 accepts. envtest runs a real `kube-apiserver` with the Swarmada CRDs and
  Swarmada's own RBAC roles, but none of the webhooks in Swarmada's
  `config/webhook/manifests.yaml`: its webhook code is in its `internal/webhook` package, which
  another module cannot import. `internal/testenv` registers two stand-ins written from that code:
  the FleetTask activation rule (validating) and the FleetZone estop-actor stamp (mutating).
  Not exercised at all: the rest of `vfleettask` (flow, bindings, producer contracts),
  `vfleetzone` (the `estop-trigger` SubjectAccessReview, composite-zone refusal),
  `vzonemaintenance` (zone exists), and every other Swarmada webhook. The stand-ins also leave
  out Swarmada's controllers, so the estop-actor annotation, which Swarmada's controller removes
  after sealing it (D33), stays on the zone in envtest.
- **Live approval-path test** (`test/live`, D32) runs against Swarmada v0.4 with its webhooks
  served (`config/controlstream-tls` applied over the quickstart). It has run against a source tree
  identical to Swarmada `558da47` before the tag (labelled `558da47`, D48), and, through
  `make test-live` on the `make demo` stack, against `558da47` resolved through the `go.mod` pin:
  every subtest of phase `webhooks` and `TestLiveSwarmadaAuditActor` passed on 2026-11-05 at copilot
  `738f848` (`docs/evidence/foc-live-2026-11-05/`). To read Swarmada's
  safety audit log it starts an ephemeral busybox container in the manager pod that mounts the
  manager's `audit-log` volume read-only as user 65532; both names come from Swarmada's
  manifests and are not checked. The quickstart's own overlay serves no admission webhooks, and
  there the copilot refuses every draft (`DENY_DRAFT_GATE_ABSENT`), so dispatch cannot be used on
  a plain quickstart install.
- Fixtures directly under `evals/fixtures/` are hand-written snapshots modelled on the Swarmada
  quickstart scenarios. Those under `evals/fixtures/live/` were captured from live quickstart runs
  of Swarmada `558da47` (D48) after the quickstart script exited (its simulated adapter stops then,
  so robot phase and connectivity drift); they differ from the hand-written ones in places (D31). The quickstart itself writes some status by hand (see
  its "Honest notes" section), and so do the fixtures.

## By design (not limits to remove)

- The copilot is not an emergency-stop path; `request_zone_stop` waits for a person.
- The copilot never ends a maintenance window, clears a stop, deletes, approves, or activates:
  a window it requested, or a draft nobody activates, stays until a person acts.
- The web UI never approves, rejects or activates (D52), and a request filed from it is never
  approved (D56).
