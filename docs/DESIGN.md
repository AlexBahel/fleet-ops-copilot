# Fleet Ops Copilot — Design

Status: draft for review (FOC-0). Nothing in this document is implemented yet unless a later
track report says so.

## 1. Scope

Fleet Ops Copilot is an operations assistant for a robot fleet orchestrated by
[Swarmada](https://github.com/swarmada/swarmada). It answers operator questions about fleet
state, proposes work, and turns proposed changes into approval requests that a named person
approves. It is a **client of Swarmada's public API**: it reads and creates Swarmada resources
through the Kubernetes API, and it changes no Swarmada CRD, proto, controller, or file.

### 1.1 Swarmada version targeted

| Item | Value |
|---|---|
| Repository | `github.com/swarmada/swarmada` (public, Apache-2.0) |
| Commit pinned | `e3d0b0b` (2026-10-16, `main`) |
| Spec version in that commit | `swarmada-version: v0.3` (`rfcs/dist/RFC-0001-core-spec.md` front matter); newest tag `rfc-0001-v0.3-draft3` |
| API group/version | `swarmada.io/v1` (`api/v1`) |

The project brief names RFC-0001 **v0.4**. On 2026-11-03 no v0.4 tag or spec header is present
in the public repository. The copilot pins the commit above and is re-pinned when v0.4 is
published (open question Q1). Every API fact below is cited to a file in that commit.

### 1.2 Non-goals

- Not an emergency-stop path. Emergency stops are made with the site's physical E-stops or
  `swarmctl estop trigger` (`cmd/swarmctl/estop.go`). `request_zone_stop` waits for a human
  approval and is therefore never the fastest way to stop a zone.
- No motion control, no adapter or proto interaction, no status writes.
- No Slack integration unless requested later.

## 2. Fictional customer scenario

> **The customer below is fictional.** The name, site, and people are invented for this
> project. Any resemblance to a real organisation is unintended.

**Example Distribution Co. (fictional)** runs a third-party-logistics warehouse with a mixed
fleet of autonomous mobile robots managed by Swarmada in namespace `warehouse-a` (the namespace
used by the public quickstart, `config/samples/demo_a.yaml`).

- **Users.** Shift supervisors ask questions in plain language ("why is sim-robot-002 not taking
  work?", "put aisle B into maintenance after this wave"). A fleet-operations engineer approves
  changes. The site's platform team holds `swarmada:admin` and is the only group that can clear
  an emergency stop (RFC-0001 §9.5.3.2, "The `estop-clear` restriction").
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
 operator ──► Web chat ┤                                     │  foc approve / Web UI
                       ▼                                     ▼
              ┌──────────────────┐   MCP (HTTP)   ┌──────────────────┐   ┌──────────────────────┐
              │  foc-agent       │ ─────────────► │  foc-mcp         │   │  foc-approvals       │
              │  Router          │                │  read tools      │   │  approve / reject    │
              │  Diagnose        │                │  request_* tools │   │  executor            │
              │  Dispatch        │                │  policy engine   │   │  policy re-check     │
              │  Zone ops        │                │  audit writer    │   │  audit writer        │
              │  Reporter        │                └───────┬──────────┘   └──────────┬───────────┘
              │  LLM backend ────┼──► Claude API (cloud)  │ get/list/watch           │ create (Swarmada),
              │                  │──► llama-server (airgap)│ create ApprovalRequest   │ annotate FleetZone
              └──────────────────┘                        ▼                          ▼
                                                    Kubernetes API (Swarmada swarmada.io/v1 + copilot ApprovalRequest)
```

Three processes with three ServiceAccounts. The process that talks to the model
(`foc-agent`) holds no Kubernetes write permission. The process that exposes tools to the
model (`foc-mcp`) can read Swarmada and create `ApprovalRequest` objects, but cannot write their
status (where the decision lives) or any Swarmada object. Only `foc-approvals` writes to
Swarmada, and only for a request whose status a named person set to approved.

### 3.1 MCP server (`foc-mcp`, Go)

Built with the official Go MCP SDK (`github.com/modelcontextprotocol/go-sdk`). Transports:
streamable HTTP (in-cluster) and stdio (local use, e.g. from Claude Code). Tool inputs and
outputs are typed Go structs; reads use the Swarmada `api/v1` Go types imported read-only from
`github.com/swarmada/swarmada` at the pinned commit.

**Read tools** (verbs: `get`, `list`, `watch` only):

| Tool | Swarmada kind / source | Fields surfaced (all from `api/v1`) |
|---|---|---|
| `list_robots`, `get_robot` | `Robot` | `status.phase`, `status.estopState`, `status.health`, `status.batteryPercent`, `status.connectivity`, `status.capabilities[]`, `status.hardware[]`, `status.assignedAction`, `status.currentZone`, `status.conditions` (`robot_types.go`) |
| `list_fleet_tasks`, `get_fleet_task` | `FleetTask` | `spec.desiredState`, `spec.failurePolicy`, `status.phase`, `status.actionSummary`, `status.actions[]` (`fleettask_types.go`) |
| `list_fleet_actions`, `get_fleet_action` | `FleetAction` | `spec.type`, `spec.zone`, `spec.priority`, `spec.requiredCapabilities`, `status.phase` (`fleetaction_types.go`) |
| `list_zones`, `get_zone` | `FleetZone` | `spec.parentZone`, `spec.maxConcurrentRobots`, `spec.estopPolicy`, `status.estopStatus`, `status.robotCount`, `status.reservations` (`fleetzone_types.go`) |
| `list_zone_maintenances` | `ZoneMaintenance` | `spec.scope`, `spec.mode`, `status.phase`, `status.pausedRobots`, `status.windingDownRobots` (`zonemaintenance_types.go`) |
| `list_discovered_robots` | `DiscoveredRobot` | `status.phase`, manufacturer, model, firmware (`discoveredrobot_types.go`) |
| `list_fleet_adapters` | `FleetAdapter` | status as defined in `fleetadapter_types.go` |
| `get_events` | core/v1 `Event` for one object | reason, message, count, last timestamp |
| `get_request` | copilot `ApprovalRequest` | request type, parameters, policy decision, approval state |

**Policy-gated write tools.** Each one creates an `ApprovalRequest` and nothing else. None of
them writes to a Swarmada object.

| Tool | What an approved request does (executed by `foc-approvals`) | Swarmada API fact |
|---|---|---|
| `request_draft_task` | Nothing in Swarmada. Stores a `FleetTask` spec as a draft after typed validation and a server-side dry-run create (`dryRun=All`). Returns the draft id and validation findings. | `FleetTask` has no draft phase: phases are `Pending;Running;Succeeded;Failed;Compensating;Compensated;Cancelled` (`fleettask_types.go`). |
| `request_activate_task` | Creates the drafted `FleetTask` (one `create`, deterministic name, so a repeat returns `AlreadyExists`). | `spec.actions` is append-only after creation (`fleettask_types.go`). The OnEvent→Auto flip is specified but not implemented at v0.3, so activation is the moment of creation. |
| `request_zone_maintenance` | Creates a `ZoneMaintenance` with `scope`, `mode`, `reason`, optional `scheduledStart`. | `spec.mode` is `Graceful` or `Immediate`; creation pauses robots in scope; deletion resumes them (`zonemaintenance_types.go`; RFC-0001 §9.1.11). |
| `request_zone_stop` | Sets annotation `swarmada.io/estop-triggered=<reason>` on one `FleetZone` with a merge patch that touches only that key. | Zone estop is triggered by that annotation and gated by the `estop-trigger` verb; removing it is the clear (RFC-0001 §9.6.2.1; `cmd/swarmctl/lifecycle.go`). |

**Operations with no tool.** No tool exists for these, the policy engine refuses them as hard
invariants (§3.2), and the RBAC in §3.6 does not grant them:

| Forbidden operation | How Swarmada exposes it |
|---|---|
| Clear a stop | remove `swarmada.io/estop-triggered` (FleetZone, Robot, SwarmadaConfig); verb `estop-clear` |
| Release a robot | delete a `ZoneMaintenance` (resumes robots); `swarmada.io/cancel-requested` (finalises the action and releases the robot binding, RFC-0001 §9.5.3); `rollout-resume` |
| Delete | any `delete` on any kind |
| Approve its own request | write `approvalrequests/status`; call the approval API with a ServiceAccount identity |
| Edit running work | `update`/`patch` on `FleetTask` (`desiredState`, `append`), `FleetAction`, `Robot`; `swarmada.io/requeue-requested` |
| Other custom verbs | `admit`, `reject`, `policy-reset`, `verify` |

### 3.2 Policy engine (Go, YAML policy files)

A pure function `Decide(request, principal, clusterSnapshot) → Decision`, where `Decision` is
one of `allow` (reads), `require_approval`, or `deny` with a stable reason code (for example
`DENY_FORBIDDEN_OPERATION`, `DENY_NAMESPACE`, `DENY_FIELD`, `DENY_ZONE_NOT_FOUND`).

Two layers:

1. **Hard invariants compiled into the binary** and not overridable by YAML: the forbidden list
   in §3.1; write tools can only produce `require_approval` or `deny`; the requester can never be
   the approver.
2. **YAML policy files** (`policies/*.yaml`, mounted from a ConfigMap) for site rules. Examples:
   allowed namespaces; allowed `FleetAction.spec.priority` values in a drafted task; allowed
   `ZoneMaintenance.spec.mode`; `autoResumeAfterMinutes` must be `0` (a non-zero value would
   resume robots without a person, i.e. a scheduled release); `requireEstopClearBeforeResume`
   may not be set to `false`; approver groups and `minApprovers` per request type; request TTL.

The engine runs twice: in `foc-mcp` when the request is created, and in `foc-approvals`
immediately before execution, against fresh cluster state. A request that would be denied at
execution time is not executed.

### 3.3 Approval flow

`ApprovalRequest` is a copilot-owned, namespaced CRD (API group: open question Q4). It is not a
Swarmada resource and is installed by the copilot's Helm chart.

1. `foc-mcp` creates the request with `spec` = type, parameters, policy decision, requesting
   user (the operator in the chat session), copilot session id. `status.state = Pending`.
2. An approver runs `foc approve <id>` or uses the Web UI and presents a Kubernetes bearer token.
   `foc-approvals` resolves it with `TokenReview` to a username. It rejects the approval if:
   the username is a ServiceAccount; it equals the requesting user (unless the policy file
   allows self-approval for a dev cluster); a `SubjectAccessReview` shows the person lacks the
   custom verb `approve` on `approvalrequests`; or a second `SubjectAccessReview` shows the
   person could not perform the underlying Swarmada operation themselves (for example
   `estop-trigger` on that `fleetzone`, `create` on `zonemaintenances`). The copilot therefore
   never lets someone do through approval what their own RBAC would not allow.
3. On approval, `foc-approvals` re-runs the policy engine, executes the single write, and records
   `approvedBy`, `approvedAt`, `executedAt`, and the created object's reference in status.
4. Requests expire after the policy TTL. Rejected and expired requests are terminal.

### 3.4 Audit

Every tool call, policy decision, approval, rejection, execution, and failure is written twice:

- **JSONL** append-only file (one JSON object per line, with the SHA-256 of the previous line so
  `foc audit verify` can detect edits or gaps), on a PVC.
- **Kubernetes Events** on the `ApprovalRequest` (reasons `RequestCreated`, `PolicyDenied`,
  `Approved`, `Rejected`, `Executed`, `ExecutionFailed`).

Audit lines and Events pass through the same redaction as traces (§3.8).

### 3.5 Agents

One orchestrator in `foc-agent` with five roles. Each role has its own system prompt and an
allow-list of MCP tools; the policy engine applies regardless of role.

| Role | Purpose | Tools |
|---|---|---|
| Router | Classifies the operator's request and hands it to one role | none |
| Diagnose | Explains robot, task, zone state from API fields and Events | read tools |
| Dispatch | Drafts a `FleetTask`, submits it for approval | read tools, `request_draft_task`, `request_activate_task` |
| Zone ops | Maintenance windows and zone stops | read tools, `request_zone_maintenance`, `request_zone_stop` |
| Reporter | Writes the final answer; every claim cites a resource and field | read tools, `get_request` |

**LLM backends** behind one Go interface (`Chat(ctx, messages, tools) → reply`):

- `claude`: Anthropic Go SDK (`github.com/anthropics/anthropic-sdk-go/v2`), Messages API,
  manual tool-use loop (the same loop runs both backends, so policy checks and tracing sit in
  one place). Model from `FOC_CLAUDE_MODEL`, effort set explicitly.
- `llamacpp`: `llama-server`'s HTTP chat-completions endpoint with tool calling, called with
  `net/http` (no extra SDK). Model file Qwen2.5-7B-Instruct GGUF; quantisation set in Helm values
  and recorded in every measurement.

### 3.6 Kubernetes RBAC

| ServiceAccount | Rules |
|---|---|
| `foc-agent` | none on Swarmada; none on `approvalrequests` |
| `foc-mcp` | `get`,`list`,`watch` on `robots`, `fleettasks`, `fleetactions`, `fleetzones`, `zonemaintenances`, `discoveredrobots`, `fleetadapters`, `robotclasses` (`swarmada.io`); `get`,`list`,`watch` on `events`; `create`,`get`,`list`,`watch` on `approvalrequests`; `create` on `fleettasks` **with `dryRun` only** (enforced by a ValidatingAdmissionPolicy that denies non-dry-run creates from this SA) |
| `foc-approvals` | reads as above; `create` on `fleettasks`, `zonemaintenances`; `patch` + custom verb `estop-trigger` on `fleetzones`; `get`,`update`,`patch` on `approvalrequests/status`; `create` on `events`; `create` on `tokenreviews`, `subjectaccessreviews` |

No ServiceAccount has `delete`, `update` on Swarmada kinds, `estop-clear`, `cancel`, `append`,
`admit`, `reject`, `rollout-resume`, or `policy-reset`.

`patch` on `fleetzones` is broader than the zone-stop needs (RBAC cannot restrict a patch to one
annotation). The chart therefore installs a ValidatingAdmissionPolicy (CEL, Kubernetes ≥ 1.30)
that denies any `foc-approvals` update to a `FleetZone` other than adding
`swarmada.io/estop-triggered`. Removal of the annotation is also blocked by Swarmada's own
admission check for `estop-clear` (RFC-0001 §9.6.2.1). See open question Q3.

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
  token patterns from span attributes, logs, audit lines, and Events. Bearer tokens presented by
  approvers are used for one `TokenReview` and never stored.

### 3.9 Interfaces

- `foc` CLI: `foc chat`, `foc requests`, `foc approve|reject <id>`, `foc audit verify`.
- Web chat UI served by `foc-agent` (Go `net/http`, embedded static HTML/JS, no front-end build
  step), with a requests panel showing each `ApprovalRequest`, its policy decision, and
  approve/reject buttons that call `foc-approvals`.

## 4. Evaluation plan

### 4.1 Definitions

- **Pass** (per scenario run), judged by the harness, all four required:
  1. correct tool calls: required read tools called; no forbidden tool or request type called;
  2. approval requested when required: an `ApprovalRequest` of the expected type and parameters
     exists, and none exists when the scenario expects none;
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

| Category | Count | Fixture states (from the public quickstart scenarios, `docs/quickstart.md`) |
|---|---|---|
| Diagnose | 7 | `healthy-fleet`, `battery-edge`, `battery-handoff`, `hardware-fault`, `comms-flaky`, `estop-drill` |
| Dispatch | 6 | `healthy-fleet`, `battery-edge`, `hardware-fault` |
| Zone ops | 6 | `healthy-fleet`, `estop-drill` |
| Safety refusals | 6 | clear a stop, cancel/release, delete, self-approve, edit a running task, maintenance with auto-resume |

Fixtures are YAML snapshots of Swarmada objects including `status`, loaded into an envtest API
server that has the CRDs from the pinned Swarmada commit (`config/crd/bases`). No Swarmada
controller runs during fixture evals, so state is static and reproducible. The public
quickstart itself writes some status by hand (`Robot.status.phase`, `status.hardware[]`, and on
`full-surface` `status.estopState`; see "Honest notes" in `docs/quickstart.md`); fixtures copy
those states and say so.

### 4.3 CI regression gate

- **Every push**: `go vet`, `golangci-lint`, unit tests, envtest tests, and fixture evals with a
  scripted backend that replays fixed model turns. This gate checks the harness, tools, policy
  engine, and approval path; it does not measure model quality. All scripted scenarios must pass.
- **Model evals** (`make eval-cloud`, `make eval-airgap`) need an API key or the in-cluster
  model and are run on demand. Results are committed under `evals/results/<date>-<deployment>/`
  with raw traces. The regression threshold for model evals is set from the first measured
  baseline, not before.

## 5. Packaging

- Helm chart `charts/fleet-ops-copilot` with `values-cloud.yaml` and `values-airgap.yaml`.
- `make foc-up`: creates a `kind` cluster, runs the public Swarmada quickstart from a clone at the
  pinned commit, installs the chart. `make foc-down` removes the cluster.
- `docs/RUNBOOK.md`: install, configure, approve, rotate secrets, read the audit, recover.
- `docs/KNOWN-LIMITS.md`: everything not measured or not enforced, including the items in §1.2,
  §3.6 and §3.7.

## 6. Repository layout (planned)

```text
cmd/foc            CLI
cmd/foc-mcp        MCP server
cmd/foc-agent      agent orchestrator + web UI
cmd/foc-approvals  approval API + executor
internal/tools     typed read and request tools
internal/policy    policy engine + YAML loader
internal/approval  ApprovalRequest types, store, executor
internal/audit     JSONL writer, Events, verify
internal/llm       claude, llamacpp, scripted backends
internal/agents    Router, Diagnose, Dispatch, Zone ops, Reporter
internal/redact    secret redaction
evals/             harness, scenarios, fixtures, results
charts/            Helm chart
config/rbac        RBAC + ValidatingAdmissionPolicy manifests
policies/          default policy files
```

## 7. Decision log

| # | Decision | Reason |
|---|---|---|
| D1 | Pin Swarmada `e3d0b0b` (spec v0.3) | It is the newest public state; v0.4 is not published. Pinning makes every cited fact checkable. |
| D2 | Import `github.com/swarmada/swarmada/api/v1` as a Go module, not a copied tree | `api/v1` imports only apimachinery and controller-runtime, so it builds as a dependency; `go.sum` pins it; nothing is copied or modified. |
| D3 | Draft lives in the copilot; activation = `create` | Swarmada has no draft phase, and the OnEvent→Auto flip is not implemented at v0.3. Creating with `desiredState: Paused` and patching later would need `patch` on `fleettasks`, which could also edit running work. |
| D4 | Write tools create `ApprovalRequest`s only | Keeps every cluster write behind a named person; the model's process has no write credential. |
| D5 | Three processes, three ServiceAccounts | The model-facing process cannot write; the tool process cannot approve; only the executor writes. Enforced by RBAC, not only by code. |
| D6 | Decision stored in `approvalrequests/status` | RBAC can grant `create` on the object to `foc-mcp` while withholding the status subresource, so "the agent cannot approve its own request" is enforced by the API server. |
| D7 | Approver identity from `TokenReview`, authority from `SubjectAccessReview` | Uses the cluster's existing identities and roles; approval cannot exceed what the approver could do directly. |
| D8 | Executor acts as its own ServiceAccount, not by impersonation | Impersonation would require the `impersonate` verb, which lets the executor act as any listed user, including admins. Attribution is kept in the request and audit instead. |
| D9 | Zone stop via annotation patch + ValidatingAdmissionPolicy | It is Swarmada's only zone-estop interface (RFC-0001 §9.6.2.1). The VAP narrows `patch` to the one annotation. |
| D10 | `autoResumeAfterMinutes` must be 0 | A non-zero value resumes robots without a person, i.e. a release (`zonemaintenance_types.go`). |
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

## 8. Open questions

- **Q1** RFC-0001 v0.4 is not public. Pin `e3d0b0b` (v0.3) now and re-pin at v0.4?
- **Q2** Is D3's reading of "draft" and "activate" the intended one?
- **Q3** The zone stop needs `patch` + `estop-trigger` on `fleetzones`, not only `create`. Is the
  ValidatingAdmissionPolicy an acceptable compensating control?
- **Q4** API group for the copilot's `ApprovalRequest` CRD (needs a DNS name the project controls).
- **Q5** The git remote is `github.com/AlexBahel/fleet-ops-copilot`; the brief names
  `github.com/swarmada/fleet-ops-copilot`. Which is canonical?
