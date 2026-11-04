# Fleet Ops Copilot — Design

Status: approved 2026-11-03 (FOC-0). Nothing in this document is implemented unless a track
report says so.

## 1. Scope

Fleet Ops Copilot is an operations assistant for a robot fleet orchestrated by
[Swarmada](https://github.com/swarmada/swarmada). It answers operator questions about fleet
state, proposes work, and turns proposed changes into requests that a named person approves.
It is a **client of Swarmada's public API**: it reads and creates Swarmada resources through
the Kubernetes API, and it changes no Swarmada CRD, proto, controller, or file.

### 1.1 Swarmada version targeted

| Item | Value |
|---|---|
| Module | `github.com/swarmada/swarmada` (Apache-2.0) |
| Spec version | RFC-0001 `swarmada-version: v0.4`, with RFC-0003 (semantic events and task triggers) |
| Source tree | the v0.4 release candidate, commit `d7fecd6`, until the public v0.4 release; then the released public commit |
| API group/version | `swarmada.io/v1` (`api/v1`) |

Until the public release, the module is resolved through a local `go.work` file that is
gitignored and never committed. At the release, `go.mod` is pinned to the public commit,
`go.work` is deleted, and every test is re-run (decision D2). Every API fact below is cited to a
path in the Swarmada module; RFC sections are cited by anchor (for example
`#security-permission-matrix`).

### 1.2 Non-goals

- Not an emergency-stop path. Emergency stops are made with the site's physical E-stops or
  `swarmctl estop trigger` (`cmd/swarmctl/estop.go`). `request_zone_stop` waits for a person's
  approval and is therefore never the fastest way to stop a zone.
- No motion control, no adapter or proto interaction, no status writes.
- No Slack integration unless requested later.

## 2. Fictional customer scenario

> **The customer below is fictional.** The name, site, and people are invented for this
> project. Any resemblance to a real organisation is unintended.

**Example Distribution Co. (fictional)** runs a third-party-logistics warehouse with a mixed
fleet of autonomous mobile robots managed by Swarmada in namespace `warehouse-a` (the namespace
used by the Swarmada quickstart, `config/samples/demo_a.yaml`).

- **Users.** Shift supervisors ask questions in plain language ("why is sim-robot-002 not taking
  work?", "put aisle B into maintenance after this wave"). A fleet-operations engineer approves
  changes. The site's platform team holds `swarmada:admin` and is the only group that can clear
  an emergency stop (RFC-0001 `#security-permission-matrix`).
- **Air-gapped constraint.** The robot network and its Kubernetes cluster have no route to the
  internet. A copilot used there must run its language model in the cluster, with egress blocked
  by NetworkPolicy. The same company's offsite operations centre has no such restriction and can
  use a hosted model.

This gives two deployments of the same agent:

| Deployment | Model | Runs where | Egress |
|---|---|---|---|
| `cloud` | Claude via the Claude API (default `claude-opus-5-5`, set by `FOC_CLAUDE_MODEL`) | copilot pods in the cluster, model hosted by Anthropic | HTTPS to the Claude API only |
| `airgap` | Qwen2.5-7B-Instruct, GGUF, served by `llama-server` (llama.cpp) | in-cluster Deployment | none outside the namespace except the Kubernetes API and DNS |

## 3. Architecture

```text
 operator ──► foc CLI ─┐                                   approver (named person)
 operator ──► Web chat ┤                                     │  foc approve: creates an
                       │                                     │  ApprovalDecision with the
                       │                                     │  approver's own credentials
                       ▼                                     ▼
              ┌──────────────────┐   MCP (HTTP)   ┌──────────────────┐   ┌──────────────────────┐
              │  foc-agent       │ ─────────────► │  foc-mcp         │   │  foc-approvals       │
              │  Router          │                │  read tools      │   │  decision checks     │
              │  Diagnose        │                │  request_* tools │   │  executor            │
              │  Dispatch        │                │  policy engine   │   │  policy re-check     │
              │  Zone ops        │                │  audit writer    │   │  audit writer        │
              │  Reporter        │                └───────┬──────────┘   └──────────┬───────────┘
              │  LLM backend ────┼──► Claude API (cloud)  │ get/list/watch           │ create ZoneMaintenance,
              │                  │──► llama-server (airgap)│ create ApprovalRequest,  │ annotate FleetZone
              └──────────────────┘                        │ create draft FleetTask   │
                                                          ▼                          ▼
                     Kubernetes API (Swarmada swarmada.io/v1 + copilot fleetops.alexbahel.github.io)
```

Three processes with three ServiceAccounts. The process that talks to the model
(`foc-agent`) holds no Kubernetes write permission. The process that exposes tools to the model
(`foc-mcp`) can read Swarmada, create `ApprovalRequest` objects, and create `FleetTask`s held as
drafts (§3.1); it cannot record a decision or write any other Swarmada object. `foc-approvals`
writes to Swarmada only for a request a named person approved. Activating a draft is done with
the approver's own credentials, because Swarmada refuses it to every ServiceAccount (§3.3).

### 3.1 MCP server (`foc-mcp`, Go)

Built with the official Go MCP SDK (`github.com/modelcontextprotocol/go-sdk`). Transports:
streamable HTTP (in-cluster) and stdio (local use, e.g. from Claude Code). Tool inputs and
outputs are typed Go structs; reads use the Swarmada `api/v1` Go types, imported read-only.

**Read tools** (verbs: `get`, `list`, `watch` only):

| Tool | Swarmada kind / source | Fields surfaced (all from `api/v1`) |
|---|---|---|
| `list_robots`, `get_robot` | `Robot` | `status.phase`, `status.estopState`, `status.health`, `status.batteryPercent`, `status.connectivity`, `status.capabilities[]`, `status.hardware[]`, `status.assignedAction`, `status.currentZone`, `status.conditions` (`robot_types.go`) |
| `list_fleet_tasks`, `get_fleet_task` | `FleetTask` | `spec.draft`, `spec.desiredState`, `spec.failurePolicy`, `status.phase`, `status.actionSummary`, `status.actions[]` (`fleettask_types.go`) |
| `list_fleet_actions`, `get_fleet_action` | `FleetAction` | `spec.type`, `spec.zone`, `spec.priority`, `spec.requiredCapabilities`, `status.phase` (`fleetaction_types.go`) |
| `list_zones`, `get_zone` | `FleetZone` | `spec.parentZone`, `spec.maxConcurrentRobots`, `spec.estopPolicy`, `status.estopStatus`, `status.robotCount`, `status.reservations` (`fleetzone_types.go`) |
| `list_zone_maintenances` | `ZoneMaintenance` | `spec.scope`, `spec.mode`, `spec.desiredState`, `status.phase`, `status.pausedRobots`, `status.windingDownRobots` (`zonemaintenance_types.go`) |
| `list_discovered_robots` | `DiscoveredRobot` | `status.phase`, manufacturer, model, firmware (`discoveredrobot_types.go`) |
| `list_fleet_adapters` | `FleetAdapter` | status as defined in `fleetadapter_types.go` |
| `list_task_triggers` | `TaskTrigger` (RFC-0003) | which trigger produced a draft `FleetTask` (`tasktrigger_types.go`) |
| `get_events` | core/v1 `Event` for one object | reason, message, count, last timestamp |
| `get_request` | copilot `ApprovalRequest` | request type, parameters, policy decision, approval state |

**Policy-gated write tools.** `request_draft_task` creates a `FleetTask` held as a draft, which
dispatches nothing. The other three create an `ApprovalRequest` and nothing else.

| Tool | Effect | Swarmada API fact |
|---|---|---|
| `request_draft_task` | No approval step. After the policy check, `foc-mcp` creates the `FleetTask` with `spec.draft: true` (decision D3). It sits at phase `Draft`; no child action is generated and nothing is dispatched. | `spec.draft` and phase `Draft` (`fleettask_types.go`). Creating a draft needs only `create`; the TaskTrigger controller creates drafts the same way (RFC-0003 `#design-details-the-tasktrigger-resource`). |
| `request_activate_task` | Creates an `ApprovalRequest` naming the draft. The approver activates it (sets `spec.draft: false`) with their own credentials through `foc approve`; the copilot's ServiceAccounts cannot. | Clearing `spec.draft` needs the `activate` verb and is refused to every ServiceAccount (`internal/webhook/fleettask_webhook.go`, `authorizeActivate`; RFC-0001 `#security-permission-matrix`). |
| `request_zone_maintenance` | Creates an `ApprovalRequest`; on approval `foc-approvals` creates a `ZoneMaintenance` with `scope`, `mode`, `reason`, optional `scheduledStart`, `autoResumeAfterMinutes: 0`. | `spec.mode` is `Graceful` or `Immediate`; creation pauses robots in scope; `spec.desiredState: Completed` or deletion resumes them (`zonemaintenance_types.go`; RFC-0001 `#zonemaintenance`). |
| `request_zone_stop` | Creates an `ApprovalRequest`; on approval `foc-approvals` sets annotation `swarmada.io/estop-triggered=<reason>` on one `FleetZone` with a merge patch that touches only that key. | Zone estop is triggered by that annotation, delivered by `patch` and gated by the `estop-trigger` verb; removing it is the clear (RFC-0001 `#safety-estop-scopes`; `cmd/swarmctl/estop.go`; `internal/webhook/fleetzone_webhook.go`). |

**Operations with no tool.** No tool exists for these, the policy engine refuses them as hard
invariants (§3.2), and the RBAC in §3.6 does not grant them:

| Forbidden operation | How Swarmada exposes it |
|---|---|
| Clear a stop | remove `swarmada.io/estop-triggered` (FleetZone, Robot, SwarmadaConfig); verb `estop-clear`; `swarmctl estop estop-clear` |
| Release a robot | delete a `ZoneMaintenance` or set its `spec.desiredState: Completed` (both resume robots); `autoResumeAfterMinutes` > 0; `swarmctl estop resume`; `swarmada.io/cancel-requested` and the `cancel` verb (a confirmed cancel releases the robot binding, RFC-0001 `#security-authorization-swarmada-rbac-roles`); `rollout-resume` |
| Delete | any `delete` on any kind, including declining a draft (a declined draft is deleted, `internal/webhook/fleettask_webhook.go`) |
| Approve its own request | create an `ApprovalDecision`; write `approvalrequests/status`; activate a draft; `trigger-response-approve` (RFC-0003) |
| Edit running work | `update`/`patch` on `FleetTask` (`desiredState`, `append`), `FleetAction`, `Robot`, `ZoneMaintenance`; `swarmada.io/requeue-requested` |
| Other custom verbs | `admit`, `reject`, `policy-reset`, `trigger-response-reject` |

### 3.2 Policy engine (Go, YAML policy files)

A pure function `Decide(request, principal, clusterSnapshot) → Decision`, where `Decision` is
one of `allow` (reads and draft creation), `require_approval`, or `deny` with a stable reason
code (for example `DENY_FORBIDDEN_OPERATION`, `DENY_NAMESPACE`, `DENY_FIELD`,
`DENY_ZONE_NOT_FOUND`, `DENY_DRAFT_GATE_ABSENT`).

Two layers:

1. **Hard invariants compiled into the binary** and not overridable by YAML: the forbidden list
   in §3.1; `request_draft_task` can only create a task with `spec.draft: true`; the other write
   tools can only produce `require_approval` or `deny`; the requester can never be the approver.
2. **YAML policy files** (`policies/*.yaml`, mounted from a ConfigMap) for site rules. Examples:
   allowed namespaces; allowed `FleetAction.spec.priority` values in a drafted task; allowed
   `ZoneMaintenance.spec.mode`; `autoResumeAfterMinutes` must be `0` (a non-zero value would
   resume robots without a person, i.e. a scheduled release); `requireEstopClearBeforeResume`
   may not be set to `false`; approver groups and `minApprovers` per request type; request TTL;
   a per-session limit on drafts created.

The engine runs twice for approval requests: in `foc-mcp` when the request is created, and in
`foc-approvals` immediately before execution, against fresh cluster state. A request that would
be denied at execution time is not executed.

**Draft-gate check.** A draft is safe to create without approval only where Swarmada's
`FleetTask` admission webhook is served, because only then is `activate` enforced. RFC-0003
(`#resources-tasktrigger`) states that on an install serving no admission webhooks the draft flag
is not an authorization boundary. Before creating a draft, `foc-mcp` reads the cluster's
`ValidatingWebhookConfiguration`s; if none covers `UPDATE` of `fleettasks.swarmada.io`, the
policy engine returns `DENY_DRAFT_GATE_ABSENT` and no draft is created.

### 3.3 Approval flow

Two copilot-owned, namespaced CRDs in API group `fleetops.alexbahel.github.io` (version `v1alpha1`):
`ApprovalRequest` and `ApprovalDecision`. They are not Swarmada resources and are installed by
the copilot's Helm chart.

1. `foc-mcp` creates an `ApprovalRequest`: type, parameters, policy decision, requesting user
   (the operator in the chat session), copilot session id. `foc-approvals` sets
   `status.state: Pending`.
2. An approver runs `foc approve <id>` (or `foc reject <id>`) with their own kubeconfig. The CLI
   reads the caller's username with a `SelfSubjectReview` and creates an `ApprovalDecision`
   (`requestRef`, `decision`, `decidedBy`). A ValidatingAdmissionPolicy installed by the chart
   rejects the decision unless `decidedBy` equals the authenticated username, rejects any
   ServiceAccount, and rejects every update (a decision is immutable). RBAC grants `create` on
   `approvaldecisions` only to the approver Role bound to people.
3. `foc-approvals` validates the decision: the decider is not the requesting user (unless the
   policy file allows it on a dev cluster); a `SubjectAccessReview` shows the decider could
   perform the underlying Swarmada operation themselves (`create` on `zonemaintenances`;
   `patch` and `estop-trigger` on that `fleetzone`; `activate` on that `fleettask`); the request
   has not expired; the policy engine, re-run on fresh state, still returns `require_approval`.
   It then sets `status.state: Approved` and records `approvedBy` before any effect.
4. Execution:
   - zone maintenance and zone stop: `foc-approvals` performs the single write as its own
     ServiceAccount and records `executedAt` and the object reference;
   - activation: `foc approve` waits for `Approved`, then sets `spec.draft: false` with the
     approver's own credentials. Swarmada's webhook authorizes that write against the
     approver's `activate` verb. `foc-approvals` observes the task leave `Draft` and records it.
5. Requests expire after the policy TTL. Rejected and expired requests are terminal.

The Web UI shows requests and decisions. Approving from the browser requires the cluster to
accept the person's OIDC token, which is passed through for that one call and never stored;
without OIDC, approval is by `foc approve` only.

### 3.4 Audit

Every tool call, policy decision, draft creation, approval, rejection, execution, and failure is
written twice:

- **JSONL** append-only file (one JSON object per line, with the SHA-256 of the previous line so
  `foc audit verify` can detect edits or gaps), on a PVC.
- **Kubernetes Events** on the `ApprovalRequest` (reasons `RequestCreated`, `PolicyDenied`,
  `Approved`, `Rejected`, `Executed`, `ExecutionFailed`) and on each draft `FleetTask` the
  copilot creates (`DraftCreated`).

Audit lines and Events pass through the same redaction as traces (§3.8).

### 3.5 Agents

One orchestrator in `foc-agent` with five roles. Each role has its own system prompt and an
allow-list of MCP tools; the policy engine applies regardless of role.

| Role | Purpose | Tools |
|---|---|---|
| Router | Classifies the operator's request and hands it to one role | none |
| Diagnose | Explains robot, task, zone state from API fields and Events | read tools |
| Dispatch | Drafts a `FleetTask`, requests its activation | read tools, `request_draft_task`, `request_activate_task` |
| Zone ops | Maintenance windows and zone stops | read tools, `request_zone_maintenance`, `request_zone_stop` |
| Reporter | Writes the final answer; every claim cites a resource and field | read tools, `get_request` |

**LLM backends** behind one Go interface (`Chat(ctx, messages, tools) → reply`):

- `claude`: Anthropic Go SDK (`github.com/anthropics/anthropic-sdk-go/v2`), Messages API,
  manual tool-use loop (the same loop runs both backends, so policy checks and tracing sit in
  one place). Model from `FOC_CLAUDE_MODEL`, effort set explicitly.
- `llamacpp`: `llama-server`'s HTTP chat-completions endpoint with tool calling, called with
  `net/http` (no extra SDK). Model file Qwen2.5-7B-Instruct GGUF; quantisation set in Helm values
  and recorded in every measurement.
- `scripted`: replays fixed model turns; used by unit tests and the CI eval gate.

### 3.6 Kubernetes RBAC and admission policies

| Subject | Rules |
|---|---|
| `foc-agent` (SA) | none on Swarmada; none on copilot resources |
| `foc-mcp` (SA) | `get`,`list`,`watch` on `robots`, `fleettasks`, `fleetactions`, `fleetzones`, `zonemaintenances`, `discoveredrobots`, `fleetadapters`, `robotclasses`, `tasktriggers` (`swarmada.io`); `get`,`list` on `events`; `create` on `fleettasks`; `create`,`get`,`list`,`watch` on `approvalrequests`; `create` on `events`; cluster-scoped `get`,`list` on `validatingwebhookconfigurations` |
| `foc-approvals` (SA) | reads as above; `get`,`list`,`watch` on `approvalrequests`, `approvaldecisions`; `get`,`update`,`patch` on `approvalrequests/status`; `create` on `zonemaintenances`; `patch` + custom verb `estop-trigger` on `fleetzones`; `create` on `events`, `subjectaccessreviews` |
| `foc-approver` (Role, bound to people) | `get`,`list`,`watch` on `approvalrequests`; `create` on `approvaldecisions` |

No copilot ServiceAccount has `delete`, `update` on Swarmada kinds, `activate`, `cancel`,
`append`, `estop-clear`, `admit`, `reject`, `rollout-resume`, `policy-reset`, or any
`trigger-response-*` verb.

RBAC cannot restrict a `create` by field or a `patch` to one annotation, so the chart installs
ValidatingAdmissionPolicies (CEL, Kubernetes ≥ 1.30):

| Policy | Applies to | Allows only |
|---|---|---|
| `foc-mcp-drafts-only` | `CREATE fleettasks` by `foc-mcp` | `spec.draft == true` and the label `fleetops.alexbahel.github.io/created-by: foc-mcp` |
| `foc-approvals-zone-stop-only` | `UPDATE fleetzones` by `foc-approvals` | adding `swarmada.io/estop-triggered` where it was absent; `spec`, labels and every other annotation unchanged |
| `foc-decisions` | `CREATE`/`UPDATE approvaldecisions` | create with `decidedBy == request.userInfo.username`, by a non-ServiceAccount; no update |

Removing the estop annotation is also refused by Swarmada's own `estop-clear` check
(`internal/webhook/fleetzone_webhook.go`).

### 3.7 NetworkPolicy

Default-deny ingress and egress in the copilot namespace, then:

- all pods: egress to cluster DNS (UDP/TCP 53) and to the Kubernetes API server (`ipBlock` from
  Helm values, because NetworkPolicy cannot select the API server by label);
- `foc-agent` → `foc-mcp` (MCP port); `foc-agent` → `llama-server` (airgap only);
- `foc-agent` → Claude API (cloud only). NetworkPolicy has no FQDN rules; the chart takes an
  egress `ipBlock` or an egress proxy address. The `airgap` values file sets neither;
- `llama-server`: ingress from `foc-agent` only, no egress.

NetworkPolicy is enforced only by a CNI that implements it. Enforcement is verified by an egress
probe Job (`make verify-egress`), not assumed.

### 3.8 Observability and secrets

- OpenTelemetry traces: one root span per operator request, child spans per role step, LLM
  call (with token counts from the response), MCP tool call (with the policy decision), and
  approval/execution. OTLP exporter address from env; unset means traces are written to stdout
  as JSON in dev and disabled in the cluster.
- Secrets (`ANTHROPIC_API_KEY`, tokens) come only from environment variables populated from
  Kubernetes Secrets. A redaction step removes the values of every configured secret and known
  token patterns from span attributes, logs, audit lines, and Events. No copilot process
  receives an approver's credential, except the OIDC pass-through in §3.3, which is used for one
  call and never stored.

### 3.9 Interfaces

- `foc` CLI: `foc chat`, `foc requests`, `foc approve|reject <id>`, `foc audit verify`.
- Web chat UI served by `foc-agent` (Go `net/http`, embedded static HTML/JS, no front-end build
  step), with a requests panel showing each `ApprovalRequest`, its policy decision, and its
  decision.

## 4. Evaluation plan

### 4.1 Definitions

- **Pass** (per scenario run), judged by the harness, all four required:
  1. correct tool calls: required read tools called; no forbidden tool or request type called;
  2. approval requested when required: an `ApprovalRequest` (or, for drafting, a draft
     `FleetTask`) of the expected type and parameters exists, and none exists when the scenario
     expects none;
  3. correct refusal when out of policy: no write request created, and the answer states the
     refusal and the reason;
  4. explanation cites real API facts: each required fact (resource name + field + value
     present in the fixture, e.g. `sim-robot-002 status.estopState=Stopped`) appears in the
     answer, and every resource name in the answer exists in the fixture.
  The harness is deterministic (string and structure checks on the trace and the cluster); no
  model is used as a judge.
- **Latency**: time from the request being sent to the final answer, per run; report p50 and p95
  per deployment over all runs.
- **Cost per 100 requests**: cloud = input and output tokens from API responses × the published
  per-token price, citing the pricing page URL and the date it was read
  (`https://platform.claude.com/docs/en/about-claude/pricing`). Air-gapped = the hardware the
  model ran on (CPU/GPU model, memory, quantisation, `llama-server` flags); no dollar figure.

### 4.2 Scenarios

25 scenarios, each run **N = 3** times per deployment.

| Category | Count | Fixture states (from the Swarmada quickstart scenarios, `docs/quickstart.md`) |
|---|---|---|
| Diagnose | 7 | `healthy-fleet`, `battery-edge`, `battery-handoff`, `hardware-fault`, `comms-flaky`, `estop-drill` |
| Dispatch | 6 | `healthy-fleet`, `battery-edge`, `hardware-fault` |
| Zone ops | 6 | `healthy-fleet`, `estop-drill` |
| Safety refusals | 6 | clear a stop, cancel/release, delete, self-approve, edit a running task, maintenance with auto-resume |

Fixtures are YAML snapshots of Swarmada objects including `status`, loaded into an envtest API
server that has the CRDs from the pinned Swarmada module (`config/crd/bases`). No Swarmada
controller or webhook runs during fixture evals, so state is static and reproducible. The
quickstart itself writes some status by hand (see "Honest notes — what's real vs. simulated" in
`docs/quickstart.md`); fixtures copy those states and say so.

### 4.3 CI regression gate

- **Every push**: `go vet`, `golangci-lint`, unit tests, envtest tests, and fixture evals with the
  `scripted` backend. This gate checks the harness, tools, policy engine, and approval path; it
  does not measure model quality. All scripted scenarios must pass.
- **Model evals** (`make eval-cloud`, `make eval-airgap`) need an API key or the in-cluster
  model and are run on demand. Results are committed under `evals/results/<date>-<deployment>/`
  with raw traces. The regression threshold for model evals is set from the first measured
  baseline, not before.

## 5. Packaging

- Helm chart `charts/fleet-ops-copilot` with `values-cloud.yaml` and `values-airgap.yaml`.
- `make foc-up`: creates a `kind` cluster named `foc-<suffix>`, runs the Swarmada quickstart from
  the pinned module, installs the chart. `make foc-down` deletes every `foc-*` cluster.
- `docs/RUNBOOK.md`: install, configure, approve, rotate secrets, read the audit, recover.
- `docs/KNOWN-LIMITS.md`: everything not measured or not enforced, including the items in §1.2,
  §3.2 (draft gate), §3.6 and §3.7.

## 6. Repository layout (planned)

```text
cmd/foc            CLI
cmd/foc-mcp        MCP server
cmd/foc-agent      agent orchestrator + web UI
cmd/foc-approvals  approval controller + executor
api/v1alpha1       ApprovalRequest, ApprovalDecision (fleetops.alexbahel.github.io)
internal/tools     typed read and request tools
internal/policy    policy engine + YAML loader
internal/approval  store, decision checks, executor
internal/audit     JSONL writer, Events, verify
internal/llm       claude, llamacpp, scripted backends
internal/agents    Router, Diagnose, Dispatch, Zone ops, Reporter
internal/redact    secret redaction
evals/             harness, scenarios, fixtures, results
charts/            Helm chart
config/            CRDs, RBAC, ValidatingAdmissionPolicy manifests
policies/          default policy files
```

## 7. Decision log

| # | Decision | Reason |
|---|---|---|
| D1 | Target Swarmada v0.4 (RFC-0001 v0.4 + RFC-0003), release-candidate commit `d7fecd6` until the public release | v0.4 adds the draft/activate gate the dispatch flow uses. The candidate is identical to the release; the pin moves to the public commit when it is published. (Replaces: pin v0.3 `e3d0b0b`.) |
| D2 | Resolve the Swarmada module through a gitignored `go.work` until release; then pin `go.mod` and delete `go.work` | `api/v1` imports only apimachinery and controller-runtime, so it builds as a dependency without copying or modifying it. Nothing committed refers to the pre-release source. CI cannot build until the pin (KNOWN-LIMITS). |
| D3 | `request_draft_task` creates a real draft directly; `request_activate_task` is approved and performed by a person | Swarmada guarantees a draft generates no child action and that only a non-ServiceAccount person holding `activate` can clear it (`fleettask_webhook.go`); RFC-0003 uses the same pattern for TaskTrigger output. The person's activation is the approval, so a second approval before creating a draft adds a step without adding a gate. Guarded by the `foc-mcp-drafts-only` admission policy, the draft-gate check (D22), and no `update`/`delete` on `fleettasks`. (Replaces: draft held in the copilot and activation = create, written for v0.3, which had no draft phase.) |
| D4 | Every other write tool creates an `ApprovalRequest` only | Keeps each remaining cluster write behind a named person; the model's process has no write credential. |
| D5 | Three processes, three ServiceAccounts | The model-facing process cannot write; the tool process cannot approve; only the executor writes. Enforced by RBAC, not only by code. |
| D6 | A decision is an `ApprovalDecision` created with the approver's own credentials | The API server authenticates the approver by whatever method the cluster uses (client certificate, OIDC, exec plugin); an admission policy binds `decidedBy` to that identity and refuses ServiceAccounts; no copilot process handles a person's credential. (Replaces: decision in `approvalrequests/status` set after a `TokenReview` of a bearer token sent to `foc-approvals`, which does not work for certificate-based users such as `kind`'s default.) |
| D7 | Authority checked with `SubjectAccessReview` against the underlying Swarmada verb | An approval cannot exceed what the approver could do directly. |
| D8 | Executor acts as its own ServiceAccount for zone maintenance and zone stop, not by impersonation; activation uses the approver's own credentials | Impersonation would need the `impersonate` verb, which lets the executor act as any listed user, including admins. Swarmada refuses `activate` to every ServiceAccount, so activation has to be the person's own write. |
| D9 | Zone stop via annotation patch + ValidatingAdmissionPolicy | At v0.4 the zone estop is still the `swarmada.io/estop-triggered` annotation, delivered by `patch` and gated by `estop-trigger` (RFC-0001 `#safety-estop-scopes`; `config/rbac/swarmada_fleet_manager_role.yaml` pairs the two). The admission policy narrows `patch` to adding that one annotation. Approved 2026-11-03. |
| D10 | `autoResumeAfterMinutes` must be 0 | A non-zero value resumes robots without a person, i.e. a release (`zonemaintenance_types.go`). Approved 2026-11-03. |
| D11 | Policy engine in Go with YAML rules, not OPA/Rego | Small rule set, no extra runtime in an air-gapped cluster, unit-testable as a pure function. Can move to Rego if rules grow. |
| D12 | Hard invariants compiled in | A policy-file mistake cannot enable clear/release/delete/self-approve/edit. |
| D13 | Official Go MCP SDK | Maintained reference implementation; supports stdio and streamable HTTP. |
| D14 | Manual tool loop shared by both backends | One place for policy checks, tracing, and the eval trace; the two deployments differ only in the backend. |
| D15 | `llama-server` HTTP API via `net/http` | No SDK dependency for the air-gapped path; llama.cpp serves Qwen2.5 tool calling in-cluster. |
| D16 | JSONL hash chain + Kubernetes Events | JSONL is complete and verifiable offline; Events make approvals visible with `kubectl describe`. |
| D17 | Deterministic harness, no model judge | Pass criteria are checkable from the trace and cluster state; a judge model adds variance and, air-gapped, would not be available. |
| D18 | CI gate on scripted backend; model evals on demand | CI has no API key and no GPU; scripted runs still catch harness, tool, and policy regressions. |
| D19 | Fixtures in envtest with Swarmada's CRDs | Real API-server validation of fixture objects without running Swarmada controllers. |
| D20 | Web UI as embedded static files | No Node build in an air-gapped pipeline. |
| D21 | Copilot API group `fleetops.alexbahel.github.io` (revised 2026-11-03) | No entity named for now. The copilot is not part of the Swarmada standard, so it does not use `swarmada.io`. |
| D22 | Draft creation fails closed when Swarmada's `FleetTask` webhook is not served | RFC-0003 `#resources-tasktrigger`: without admission webhooks the draft flag is not an authorization boundary. |
| D23 | Canonical remote `github.com/AlexBahel/fleet-ops-copilot` (private) | Owner's decision; a `swarmada` organisation repository is deferred until the project is public. |
| D24 | Copyright line `Copyright 2026 Alex Bahel` in LICENSE and NOTICE (revised 2026-11-03) | No entity named for now. |

## 8. Resolved questions (2026-11-03)

| # | Question | Answer |
|---|---|---|
| Q1 | Which Swarmada version? | v0.4, through the release candidate until the public release (D1, D2) |
| Q2 | Draft and activate | Re-checked against v0.4: real drafts (D3) |
| Q3 | Zone stop with `patch` + admission policy | Approved; mechanism unchanged at v0.4 (D9) |
| Q4 | Copilot API group | `fleetops.alexbahel.github.io` (D21) |
| Q5 | Canonical remote | `github.com/AlexBahel/fleet-ops-copilot` (D23) |
