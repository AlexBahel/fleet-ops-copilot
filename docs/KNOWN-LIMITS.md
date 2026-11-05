# Known limits

What is not built, not measured, or not enforced. Each item stays here until code and a test
remove it.

## Not built yet

- The five agent roles (Router, Diagnose, Dispatch, Zone ops, Reporter) and their tool
  allow-lists. The tool-use loop (`internal/agent`) offers every tool.
- A login for the web UI (DESIGN D56). Without one, requests filed from the page are never
  approvable; `foc chat` is the client that files approvable requests.
- OIDC for people (ruling R43, production path). On kind the people authenticate with a static
  token file (`hack/kind-people.sh`, test only, D60); the OIDC configuration is described in
  `docs/RUNBOOK.md` and has not been set up or run anywhere. Requester authentication has not
  seen an OIDC token (see "Not measured yet").
- Conversation memory: each chat message is an independent agent run (D55). No streaming of
  answers: the page and `foc chat` wait for the whole run.
- The air-gapped profile is not templated in the Helm chart; it is installed beside it from
  `config/airgap` (`make deploy-airgap`), and neither it nor the chart runs foc-agent with
  `-backend llamacpp`. `make deploy-kind` installs the plain manifests (`config/deploy`) on a kind
  cluster; the image is built from binaries compiled on the host. `make foc-up` installs the
  chart (cloud profile); it has run on one machine (see "Not measured yet").
- Tracing: no stdout exporter and no OTLP/HTTP exporter (OTLP/gRPC only), and no spans in
  `foc-approvals` for approval and execution (D54). `foc-eval -otlp-endpoint` (D65) traces the
  agent loop and an in-process foc-mcp, not the deployed processes. The chart's optional trace backend
  (`tracing.backend.enabled`, D61) keeps traces in the Jaeger pod's memory only: a restart loses
  them, and it is for development and demonstration, not a trace store. Its images must be
  pullable by the nodes, so it is not part of the air-gapped profile. With the backend on, the
  chart renders the Collector's egress to Jaeger's OTLP port and DNS also without
  `networkPolicy.enabled` (D68); **tracing beside the air-gapped policy is verified by template
  only** (helm lint, kubeconform), not on a cluster, until the next cluster run. Beside
  `config/airgap`, foc-mcp's and foc-agent's export to the Collector's OTLP port is opened by
  `config/airgap/networkpolicy-tracing.yaml`, which `make deploy-airgap` applies only when the
  Collector is installed (D69); the chart opens the same port only with `networkPolicy.enabled`.
  Both are template-verified only, until the next cluster run. On kindnet a NetworkPolicy applies
  to new connections only (see "Egress in the air-gapped profile"): a pod started before the
  rule existed keeps its old connections, so restart foc-mcp and foc-agent after applying it.
- More than one approver per request (`minApprovers`): one accepted decision approves.
- A waypoint in a drafted task. `request_draft_task` takes an action's name, type, zone, priority,
  required capabilities, preferred robot and dependencies (`internal/tools/requests.go`), and no payload, so a
  Navigate action cannot say where to go. Scenario dispatch-04 asks for an inspection of a named
  dock and the tool cannot express it: both Claude models failed all three runs of it, and the
  failure is a product gap, not a model failure (ruling R49).

## Not measured yet

- No model evaluation of record has been run. `evals/results/fixtures/results.json` is a harness
  self-test with the scripted backend: it shows the harness, tools, policy and approval path
  behave as designed on fixture states, and says nothing about a model's pass rate.
- Model latency (p50, p95) and cost per 100 requests, for both deployments. The latency in the
  fixture results covers tool execution against a test API server only.
- The `llamacpp` backend (`internal/llm/llamacpp.go`, `foc-eval -backend llamacpp`) has not run
  an evaluation against a model; it is tested against a fake server.
- The air-gapped manifests (`config/airgap/`) and the `hack/airgap.mk` targets have run on one
  single-node kind cluster (kind v0.33.0, CNI kindnet, Docker Desktop on arm64;
  `docs/evidence/airgap-2026-11-04/`), on no other CNI or node.
- The Helm chart (`charts/fleet-ops-copilot`) with its optional tracing backend (OpenTelemetry
  Collector and Jaeger, D61) has been installed by `make foc-up` on one single-node kind cluster
  (2026-11-05, `docs/evidence/airgap-2026-11-05/`): both backend pods became ready with the pod
  security settings and probes as rendered, and the Collector received OTLP and exported it to
  Jaeger. Its cloud-profile NetworkPolicy (off by default) has never been applied to a cluster,
  and `docs/RUNBOOK.md` (draft) has not been run as a sequence beyond section 0.
- `make foc-up`, `make foc-down` and `make foc-live` (D59, D60) have run on one machine only
  (Mac mini, Docker Desktop, kind v0.33.0, 2026-11-05): `make foc-up` twice from no cluster, each
  time with Docker's image and build caches already filled, so a cold machine has not been timed;
  the kind cluster with the static token file, Swarmada's quickstart and webhook overlay from the
  writable module copy and the step order worked. `make foc-live` passed every subtest of phase
  `webhooks`, including `requester-is-authenticated-not-asserted`, after a test fix (a closed MCP
  session reused across subtests). The run logs are not committed. After the webhook overlay
  replaces the quickstart's overlay, whether the simulated adapter stays connected has not been
  observed.
- `foc-agent`, its web UI and `foc chat` have not run on a cluster or against a model, and whether the
  deployed foc-mcp's spans reached the chart's Collector was not checked (the traces of 2026-11-05
  came from `foc-eval -otlp-endpoint`, D65); they are tested with the scripted backend, envtest and an
  in-process OTLP receiver, and the page was checked once in a browser against a local test server.
  Requester authentication (D57) has been tested only with static person tokens (envtest, and
  kind through `make foc-live`) and TokenRequest ServiceAccount tokens, not with OIDC tokens or exec-plugin credentials. The chart is checked with `helm lint --strict`,
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

## Not enforced, or enforced only partly

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
  reach the foc-agent Service can still chat (spending model tokens under the site's API key),
  file such unapprovable requests (counted against the per-session draft limit of the session id
  they send), and read every ApprovalRequest in the bound namespaces (operator names, reasons).
  The Service is ClusterIP only, the chart renders no Ingress (`make chart-parity` refuses one),
  and the RUNBOOK uses `kubectl port-forward`; whether NetworkPolicy applies to port-forwarded
  traffic depends on the CNI. The only rate limit is the per-pod cap on chats in flight.
- **Client-certificate users are unverified requesters (D57, R43).** On kind, kind's admin and the
  people `hack/kind-person.sh` creates authenticate with client certificates and have no bearer
  token, so a request they file (through `foc chat` or any other client) records
  `unverified:<name>` and can only be rejected. A verified requester needs a token-backed person
  identity: on kind, the static token file for the fictional people (`hack/kind-people.sh`,
  `make foc-up`, D60); in production, OIDC.
- **The kind person tokens are not an identity system (D60).** A static token file has no expiry
  and no rotation, and a change needs an API server restart. Anyone who can read
  `$FOC_UP_DIR/people/tokens.csv` (mode 600) or the node's `/etc/foc-people` can act as sam, dana
  or eve on that cluster. It exists for fictional people on a local cluster, and no other.
- **Requester tokens (D57).** The token reaches `foc-mcp` through `foc-agent` over plain HTTP
  inside the cluster, and both processes briefly hold a credential that is valid against the API
  server; `foc-mcp` uses it only for a `TokenReview` (default audiences) and keeps a SHA-256 of it
  for 30 seconds. Session ids are still asserted by the client.
- **Impersonation.** A person allowed to impersonate users can create a decision as another
  person; the admission policy sees the impersonated identity. This is how Kubernetes
  authentication works, not something the copilot can detect.
- **Draft gate detection.** `foc-mcp` and `foc-approvals` treat any validating webhook with
  `failurePolicy: Fail` covering `UPDATE` of `fleettasks.swarmada.io` as the gate. They do not
  verify that it is Swarmada's webhook or that it is serving.
- **ServiceAccount names** in the admission policies are fixed to namespace `foc-system`.
- **Per-session draft limit** is applied when a request is made, not when it is approved.
- **Estop attribution.** When `foc-approvals` triggers an approved zone stop, Swarmada attributes
  it to the executor's ServiceAccount: its mutating webhook stamps `swarmada.io/estop-actor`,
  its controller seals that actor into an `ESTOP_TRIGGERED` entry of Swarmada's safety audit log
  and then removes the annotation (D33). The approving person is recorded only in the copilot's
  `ApprovalRequest`, Events and audit file.
- **Activation is done by the approver, outside the copilot.** After approval the copilot gives
  the exact command (`status.instruction`) and watches for the task to leave `Draft`; it cannot
  tell who ran the command, and it waits without a deadline. swarmctl v0.4 has no activate
  subcommand, so the command is a `kubectl patch`. The web UI does not perform it (D52).
- **The write shown in the web UI** is the body `foc-approvals` sends (D53); fields the API server
  defaults and anything Swarmada's mutating webhooks add are not shown.
- **foc-agent readiness** depends on foc-mcp and on the API server; either being down removes the
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
  Jaeger, opened before `foc-airgap-egress` was created, carried spans for at least 68.6 minutes
  under a policy that allows the Collector only DNS; after a restart its new connection, and a new
  connection from its network namespace, timed out, while a pod without a policy still reached
  Jaeger (`docs/evidence/airgap-2026-11-05/collector-restart-check.log`). So a connection opened in
  a new pod's start-up window, or by any pod before the policy is applied, can outlive the policy:
  apply the policy before the workloads start, or restart every pod in `foc-system` after applying
  it (`make deploy-airgap` restarts foc-mcp and foc-approvals only). Other CNIs were not tested. Which CNIs enforce a policy from pod
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
  served (`config/controlstream-tls` applied over the quickstart). It has been run against a source
  tree identical to Swarmada `558da47` before the tag (labelled `558da47`, D48); no run has
  been made with the module resolved through the `go.mod` pin. To read Swarmada's
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
