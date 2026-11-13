# Known limits

What is not built, not measured, or not enforced. Each item stays here until code and a test
remove it.

## Not built yet

- The five agent roles (Router, Diagnose, Dispatch, Zone ops, Reporter) and their tool
  allow-lists. The tool-use loop (`internal/agent`) offers every read tool, `check_task` and
  `propose_task`; never a request tool or `whoami` (D74).
- A login for the web UI (DESIGN D56). Without one, requests filed from the page are never
  approvable; `fleetops chat` is the client that files approvable requests.
- OIDC for people (production path). On kind the people authenticate with a static
  token file (`hack/kind-people.sh`, test only, D60); the OIDC configuration is described in
  `docs/RUNBOOK.md` and has not been set up or run anywhere. Requester authentication has not
  seen an OIDC token (see "Not measured yet").
- Durable conversation state: fleetops-agent keeps conversations in memory (D75), so a restart loses
  them and the chart runs one fleetops-agent replica (P2 in `docs/BACKLOG.md`). No streaming of answers:
  the page and `fleetops chat` wait for the whole run.
- The air-gapped profile is only partly templated in the Helm chart: `llm.profile: airgap` runs
  fleetops-agent with `-backend llamacpp` (D103), but llama-server, its model storage and the egress
  policies are installed beside the chart from `config/airgap` (`make deploy-airgap`). The image is
  built from binaries compiled on the host. `make demo` installs the air-gapped profile by default
  and the cloud profile with `DEMO_MODEL=cloud` (D103); it has run on one machine (see "Not measured
  yet"; the air-gapped profile: `docs/evidence/demo-airgap-2026-11-13/`). Its Docker memory requirement, 12 GiB, is a threshold set from llama-server's memory limit
  and the rest of the stack on the v6 record's node (D103), not a measured minimum.
- Tracing: no stdout exporter and no OTLP/HTTP exporter (OTLP/gRPC only), and no spans in
  `fleetops-approvals` for approval and execution (D54). `fleetops-eval -otlp-endpoint` (D65) traces the
  agent loop and an in-process fleetops-mcp, not the deployed processes. The chart's optional trace backend
  (`tracing.backend.enabled`, D61) keeps traces in the Jaeger pod's memory only: a restart loses
  them, and it is for development and demonstration, not a trace store. Its images must be
  pullable by the nodes, so it is not part of the air-gapped profile. With the backend on, the
  chart renders the Collector's egress to Jaeger's OTLP port and DNS also without
  `networkPolicy.enabled` (D68). Beside `config/airgap`, fleetops-mcp's and fleetops-agent's export
  to the Collector's OTLP port is opened by `config/airgap/networkpolicy-tracing.yaml`, which
  `make deploy-airgap` applies only when the Collector is installed (D69); the chart opens the
  same port only with `networkPolicy.enabled`. Beside the air-gapped policy both ran on one kind
  cluster with kindnet (`docs/evidence/airgap-2026-11-12/`): a restarted Collector delivered spans
  to Jaeger, and fleetops-mcp reached the Collector's OTLP port. fleetops-agent did not run there,
  so its export beside `config/airgap` is verified by template only. On kindnet a NetworkPolicy applies
  to new connections only (see "Egress in the air-gapped profile"): a pod started before the
  rule existed keeps its old connections, so restart fleetops-mcp and fleetops-agent after applying it.
- More than one approver per request (`minApprovers`): one accepted decision approves.
- Locations reach the robot only through the action's payload, in this site's convention
  (`{"waypoint", "x", "y"}`, D77): Swarmada v0.4 delivers no waypoint to an adapter (C4 in `docs/BACKLOG.md`),
  so an adapter that does not read `x` and `y` does not know where to go. The quickstart's adapter
  reads them.
- **Patrol shape (D77).** A patrol is one Patrol member per waypoint of the route, run in order
  `times` rounds (FleetTask `spec.flow`), because the quickstart's adapter drives each Patrol action
  to one `x`,`y`. An adapter that patrols a whole route in one action would need a catalogue kind
  with one member and the route in its payload.
- What a robot found is not reported: an inspection, a patrol's observations and a survey's map
  are class (b) (`docs/DESIGN.md` §3.10.1). The preview says so; a person reviews the
  result on the robot's side. Needs a Swarmada result field for robot actions, proposed for a later Swarmada version (C1 in `docs/BACKLOG.md`).

## Not measured yet

- Model evaluations of record exist for fixture mode only, on eval surface v6 (50 scenarios × 3
  each, 2026-11-08): Sonnet 5.5, Opus 5.5 and the air-gapped Qwen2.5-7B-Instruct and
  Qwen2.5-14B-Instruct (`evals/results/comparison-v6.md`, with the results.json each cites). Each is
  one sample of three runs per scenario. The current eval surface, v7 (D101), has no model run.
  No model run of record has been made in live mode
  (`fleetops-eval -live-context`) or through the deployed fleetops-agent. `evals/results/fixtures/results.json`
  is a harness self-test with the scripted backend: it shows the harness, tools, policy and
  approval path behave as designed on fixture states, and says nothing about a model's pass rate.
- Model latency (p50, p95) and cost per 100 requests are measured for those fixture-mode runs
  only, from the Mac mini: to the Claude API, or to llama-server through a port-forward, plus tool
  execution over MCP on loopback against envtest (`latency.scope` in each results.json). The
  latency of a request through the deployed fleetops-agent is not measured. The latency in the fixture
  results covers tool execution against a test API server only.
- The `llamacpp` backend (`internal/llm/llamacpp.go`) has run against a model only through
  `fleetops-eval -backend llamacpp` against llama-server in the air-gapped kind profile (the v6
  records `evals/results/airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v6/` and
  `evals/results/airgap-llamacpp-qwen2.5-14b-instruct-2026-11-08-v6/`). `config/airgap/llama-server.yaml` names
  the Qwen2.5-7B-Instruct files; the 14B run used it with the model files, alias and memory
  request and limit changed (that directory's `model.md`). fleetops-agent runs it in `make demo`'s
  default profile (D103) and answered two single questions there (`docs/evidence/demo-airgap-2026-11-13/`:
  134 s through `fleetops chat`, 33 s in the web chat, one condition misread in the latter); no eval has
  been run through the deployed fleetops-agent with it, so its pass rate and latency there are not
  measured.
- The air-gapped manifests (`config/airgap/`) and the `hack/airgap.mk` targets have run on
  single-node kind clusters (kind v0.33.0, CNI kindnet, Docker Desktop on arm64;
  `docs/evidence/airgap-2026-11-12/`), on no other CNI or node.
- The Helm chart (`charts/fleet-ops-copilot`) with its optional tracing backend (OpenTelemetry
  Collector and Jaeger, D61) has been installed by `make demo` on single-node kind clusters
  (2026-11-08 and 2026-11-09, `docs/evidence/live-2026-11-08/`; 2026-11-12,
  `docs/evidence/airgap-2026-11-12/`): both backend pods became ready with the pod security
  settings and probes as rendered, and the Collector received OTLP and exported it to Jaeger. Its
  cloud-profile NetworkPolicy (off by default) has never been applied to a cluster, and
  `docs/RUNBOOK.md` has not been run as a sequence beyond section 0.
- `make demo`, `make demo-down` and `make test-live` (D59, D60) have run on one machine only
  (Mac mini, Docker Desktop, kind v0.33.0; 2026-11-08 and 2026-11-09 UTC,
  `docs/evidence/live-2026-11-08/`). `make demo` took 2m29s, 2m19s and 2m47s, each creating its kind
  cluster; a cold machine (no Docker images or build cache) has not been timed.
  `make test-live` passed every subtest of phase `webhooks`, and Swarmada's audit of the approved
  zone stop showed all three simulated robots stopped (`stopped:3 unconfirmed:0`). The full run
  logs are not committed.
- Swarmada's sim adapter (`adapters/simulation/sim_adapter.py`) does not retry a refused register:
  a robot whose register is refused keeps streaming telemetry, shows as live and confirms a stop, but
  never acts on work. `hack/demo-sim-fleet.sh` therefore points each Robot at its adapter before the
  adapter that stays starts, and `make demo` fails, naming the robot, if one is not registered and
  ready for work within the timeout. Restarting a sim adapter by hand while it holds
  work did not bring that work back on 2026-11-08 (`docs/evidence/live-2026-11-08/`, "Chain 1").
- The deployed `fleetops-agent` has run against a model on a cluster on two days (UTC), each time on the
  demo stack with Sonnet 5.5 through `fleetops chat`. The 2026-11-08 runs are in `docs/evidence/live-2026-11-08/`
  with transcripts, requests, decisions, Jaeger exports and costs. At 21:35 one move request was filed
  and stayed Pending (0.0280 USD). At 23:32–23:58 it ran end to end: a move to staging-shelf-a for
  sim-robot-001, approved by dana, an activation request from the same conversation approved by
  dana, `fleetops activate` by dana, and the FleetTask Succeeded (0.0379 USD for the two model
  messages). On 2026-11-09 (01:22–01:28 UTC, same folder, "End to end, run 2") the README's own
  example, "Send a robot to staging-shelf-a.", ran the same way and Succeeded on the robot Swarmada
  picked, sim-robot-002 (0.0390 USD). There, the spans of
  fleetops-agent and fleetops-mcp reached the chart's Collector and Jaeger. One conversation is not a
  pass rate: the measured results are the fixture-mode runs above. The web UI has not been used on a
  cluster: the page was checked once in a browser against a local test server. Otherwise the agent
  is tested with the scripted backend, envtest and an in-process OTLP receiver.
- **A message the engine answers without the model has no fleetops-agent trace.** fleetops-agent
  starts its `fleetops.request` span only in the model loop (`internal/agent/agent.go`). The
  confirmation that files a request does not enter it, so its tool calls appear only as fleetops-mcp
  spans in a trace of their own, not under the conversation's request (seen on 2026-11-08,
  `docs/evidence/live-2026-11-08/`). The ApprovalRequest still names the message's session id.
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
- The judge (D66) accepts a fact in any clear form. It does not detect an invented object named
  without its kind word (only `Kind/name`, `Kind name` and `name (Kind)` are checked against the
  cluster), and it accepts the value of another field of the same object stated in the same
  clause (a field name is not required). `results.json` records the judge version
  (`systemPrompt.judge`).
- No eval scenario reaches `DENY_DRAFT_GATE_ABSENT`: fixture evals always run with the draft
  gate served. The refusal is covered by the policy unit tests (`internal/policy/policy_test.go`),
  an envtest test (`test/envtest/approval_test.go`) and the live test (`test/live`).
- **Eval surface v7 has no record runs.** Its fixes (D98–D101) come from the v6 record runs of
  2026-11-08, which stay the record; each failed run's class is in the `failures.md` beside its
  results.json, and `evals/results/comparison-v6.md` compares them.
- **The air-gapped profile's 8192-token context was reached** in 4 of the v6 runs (Qwen2.5-7B
  guided-08 run 2; Qwen2.5-14B guided-08 run 3, guided-18 runs 2 and 3), after refusal loops or
  repeated reads; the run ends with llama-server's HTTP 400 (`evals/results/comparison-v6.md`).
- **Fields of composed steps are not checked for their form.** A "something else" request composed
  from basic steps (D72) carries each step's fields as the model gave them; D88's form check
  applies to the request's own fields only, so an invalid step value reaches `check_task`.
- **Guided requests have run end to end on a cluster for one kind only.** On the demo stack a guided
  `move` (2026-11-08 and 2026-11-09, `docs/evidence/live-2026-11-08/`, "End to end" and "End to end,
  run 2") went through the dialogue,
  `check_task`, the preview and confirmation, the approval, the execution re-check, and the executor's
  dry runs, which Swarmada's admission accepted with its webhooks served. It then went through the
  draft, an activation request, its approval and `fleetops activate`, and the FleetTask Succeeded.
  Nothing else has run on a cluster: no
  other kind, no "something else" request, no blocked preview, no execution re-check that failed, and
  no follow-up (choice 2). Those are tested with the scripted backend, unit tests, envtest and the
  fixture evaluations. In envtest the executor's dry runs reach the CRD schema and the copilot's
  admission policies only.
- **The activation record names who ran `fleetops activate`, not who sent the patch.** The record is
  bound to the authenticated person and is made just before the patch; Swarmada does not record
  who cleared `spec.draft`. fleetops-approvals links the most recent record made after the approval and
  lists earlier ones (for example one whose patch Swarmada refused), so with two people activating
  at once the link can name the one whose patch lost. An activation made with `kubectl patch` and
  no record is recorded as "activated outside fleetops activate; activator not recorded".
- **`fleetops activate` needs the `fleetops:approver` role** (`create approvaldecisions`), which also
  lets the holder approve or reject requests within their Swarmada permissions. A person who may
  only activate uses `kubectl patch`, and the activator is then not recorded (P3 in `docs/BACKLOG.md`).
- **The demo's simulated fleet is three host processes.** The Swarmada quickstart stops its sim
  adapter when it exits, so make demo runs `hack/demo-sim-fleet.sh start`: one `sim_adapter.py` per
  robot and one port-forward, in the background on the host (PIDs in `$DEMO_DIR/sim-fleet/pids`,
  logs in `$DEMO_DIR/logs`), with sim-robot-002/003 moved to their own FleetAdapters
  (`sim-fleet-adapter-2`/`-3`, client certificates from Swarmada's `demo-ab-live` overlay), as the
  quickstart's all-live path does. If a process dies, the Robots go `Offline` and filing is refused
  (`check_task`, D73); `hack/demo-sim-fleet.sh start` again restarts them. `make demo-down` stops them.
- **A finding's inspect kind may come from a word list.** When a model reads a finding as "not
  possible" and gives no `lookFor`, the engine picks inspect-damage if the person's words contain a
  damage word (damage, dent, crack, broken, leak, scratch, torn, bent) and inspect-location
  otherwise (D72). The preview shows the kind, so the person can change it with choice 3.
- **RobotClass `simbot-v1` is a demo workaround.** Swarmada's warehouse quickstart names it on its
  Robots but does not ship it, and Swarmada's Robot webhook refuses every Robot spec update while it
  is missing. make demo creates a minimal one (manufacturer, model, base adapter, the front camera),
  labelled `fleetops.alexbahel.github.io/demo-workaround: "true"`. It is not a model of real hardware.
- **`stallsAfterActivationProxy`** in results.json is a proxy: `check_task` re-run on the fixture
  at the end of a run, not an observation of work left Pending by Swarmada.
- **Fixture setup.** Scenarios that file work set the live captures' FleetAdapter
  `Connected` in a declared `setup` step (the captures show `Disconnected` only because the
  quickstart stops its adapter on exit, D31); the capture files are unchanged. The hand-written
  `healthy-fleet-draft` carries the robots' class and current zone. Fixture runs use a fixed clock
  (2026-11-04T06:00:00Z), recorded in `environment.clock`.

## Not enforced, or enforced only partly

- **`check_task` is a copy of Swarmada's rules, from reads.** It evaluates the candidate filters of
  RFC-0001 §9.4.2 that a read can see and reports the rest as not checked: the adapter's
  `validate_action` for the concrete action, filter 9's contract range and SafetyStream, filter 10's
  reported stops and duplicate robot ids. A capability denied on a descendant zone is not narrowed
  (Swarmada does not either, at v0.4). "Likely feasible" can turn out wrong: the fleet changes
  between the check, the approval, the activation and the assignment. `TestFilterListPinned` fails
  when Swarmada's filter list changes at a re-pin; a Swarmada eligibility dry run is proposed for
  a later Swarmada version (C5 in `docs/BACKLOG.md`).
- **The confirmation is asserted by the client.** fleetops-agent sets the confirmed preview id only
  after the person's own reply, and fleetops-mcp refuses a request without it (D74). Another MCP client
  (the evaluation harness, a local stdio client, `test/live`) sets it itself, as it sets the session
  id.
- **SwarmadaConfig read (D80).** fleetops-mcp's and fleetops-approvals' ServiceAccounts can `get` and
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
- **Client-certificate users are unverified requesters (D57).** On kind, kind's admin authenticates
  with a client certificate and has no bearer token, so a request it files (through `fleetops chat` or any other client) records
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
  records the activator from the Activation `ApprovalDecision` that `fleetops activate` creates (D79); an activation made another way
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
  (`docs/evidence/airgap-2026-11-12/egress-startup-window.log`): a new pod's egress was open for
  up to about 1.3 s after its container started (open at 714 ms and 571 ms, blocked from 1302 ms
  and 1214 ms in two runs), then denied. The egress probe therefore records egress at pod start (not counted), waits until
  a connection its policy denies inside the cluster fails, and decides on the attempt after that
  (D63); the at-start line shows the window where it exists. Nothing in the copilot waits for the
  policy before opening connections, so a process could reach the internet in that window.
  **On kindnet the policy applies to new connections only:** a connection opened before the policy
  existed kept working after it. Measured 2026-11-12: a pod in `fleetops-system` opened one
  connection to the API server before the policies were created; under a policy that allows that
  pod DNS only, the connection answered 299 reused requests over 14 min 38 s, while every new
  connection from the same pod timed out (`docs/evidence/airgap-2026-11-12/conn-check.log`). So a connection opened in
  a new pod's start-up window, or by any pod before the policy is applied, can outlive the policy:
  apply the policy before the workloads start, or restart every pod in `fleetops-system` after applying
  it (`make deploy-airgap` restarts fleetops-mcp and fleetops-approvals only). Other CNIs were not tested. Which CNIs enforce a policy from pod
  start has not been checked here; for a site that needs it, choose a CNI whose own documentation
  says so and run `make verify-egress` there. Cluster DNS answered
  queries for external names in kind (CoreDNS forwards to the host; `example.com` resolved and
  connected from a namespace without the policy, `docs/evidence/airgap-2026-11-12/verify-egress-baseline.log`). `kubectl port-forward`
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
  served (`config/controlstream-tls` applied over the quickstart). Through `make test-live` on the
  `make demo` stack, against `558da47` resolved through the `go.mod` pin, every subtest of phase
  `webhooks` and `TestLiveSwarmadaAuditActor` passed on 2026-11-08 at copilot `d17064d`
  (`docs/evidence/live-2026-11-08/`). To read Swarmada's
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
