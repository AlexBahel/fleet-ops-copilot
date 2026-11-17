# Fleet Ops Copilot — Tools and policy engine

The MCP server's tools, the operations no tool offers, and the policy engine. The summary is in
[`DESIGN.md`](../DESIGN.md).

## 3.1 MCP server (`fleetops-mcp`, Go)

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
| `check_task` | proposed draft (not stored) checked against `Robot`, `FleetZone`, `ZoneMaintenance`, `FleetAdapter`, `FleetTask`, `SwarmadaConfig` | verdict `likely feasible` / `blocked`, blockers, warnings, alternatives, the checks not made ([§3.10.4](guided-requests.md#3104-check_task-d73), D73) |
| `whoami` | the caller's `TokenReview` result | requester and whether it is verified; served to fleetops-agent, never offered to a model (D75) |

**Policy-gated write tools.** Each one creates an `ApprovalRequest` and nothing else; none writes
to a Swarmada object (decision D25). Each one files only a request whose preview the requester
confirmed: the call carries the preview's id, fleetops-mcp recomputes it from the parameters, and
fleetops-agent, not the model, makes the call after the person's confirmation ([§3.10.5](guided-requests.md#3105-preview-and-confirmation-d74), D74).

| Tool | Effect | Swarmada API fact |
|---|---|---|
| `request_draft_task` | Creates an `ApprovalRequest` of type `DraftTask`. On approval `fleetops-approvals` creates the `FleetTask` with `spec.draft: true` (decision D25). It sits at phase `Draft`; no child action is generated and nothing is dispatched. | `spec.draft` and phase `Draft` (`fleettask_types.go`). Creating a held draft needs only `create` (`internal/webhook/fleettask_webhook.go`); the TaskTrigger controller creates drafts the same way (RFC-0003 `#design-details-the-tasktrigger-resource`). |
| `request_activate_task` | Creates an `ApprovalRequest` naming the draft. After a named approval the copilot records it and gives the approver the exact command in `status.instruction`: `fleetops activate <request> -n <ns>`. The person runs it with their own credentials: it refuses a ServiceAccount caller, runs `check_task` with the caller's reads, records an `ApprovalDecision` of `type: Activation` (bound to the authenticated caller by the `fleetops-decisions` admission policy), and then sends the plain merge patch `{"spec":{"draft":false}}`, adding nothing to the FleetTask ([§3.10.9](guided-requests.md#3109-fleetops-activate-d79), D79). `fleetops-approvals` watches for the task to leave `Draft`, links the Activation record (`status.activatedBy`) and writes an audit record; an activation made with a plain `kubectl patch` is recorded as "activated outside fleetops activate; activator not recorded". The copilot never activates (D28). | Clearing `spec.draft` needs the `activate` verb and is refused to every ServiceAccount (`authorizeActivate`, `internal/webhook/fleettask_webhook.go`; RFC-0001 `#security-permission-matrix`); Swarmada checks it for the person who sends the patch. swarmctl v0.4 has no activate subcommand. |
| `request_zone_maintenance` | Creates an `ApprovalRequest`; on approval `fleetops-approvals` creates a `ZoneMaintenance` with `scope`, `mode`, `reason`, optional `scheduledStart`, `autoResumeAfterMinutes: 0`. | `spec.mode` is `Graceful` or `Immediate`; creation pauses robots in scope; `spec.desiredState: Completed` or deletion resumes them (`zonemaintenance_types.go`; RFC-0001 `#zonemaintenance`). |
| `request_zone_stop` | Creates an `ApprovalRequest`; on approval `fleetops-approvals` sets annotation `swarmada.io/estop-triggered=<reason>` on one `FleetZone` with a merge patch that touches only that key. Swarmada's mutating webhook then adds `swarmada.io/estop-actor` (the executor's ServiceAccount) and the `swarmada.io/estop-in-force` finalizer. | Zone estop is triggered by that annotation, delivered by `patch` and gated by the `estop-trigger` verb, which Swarmada checks for the requesting user with no ServiceAccount exception (`authorizeEstopChange`/`authorizeEstopVerb`, `internal/webhook/fleetzone_webhook.go`); removing it is the clear (RFC-0001 `#safety-estop-scopes`). Actor and finalizer: `stampEstopActor`, `internal/webhook/estop_actor_defaulter.go`. The actor annotation is transient: the zone-estop controller seals it into Swarmada's safety audit log (`ESTOP_TRIGGERED`) and removes it in the patch that sets `swarmada.io/estop-processed` (`consumeEstopActor`, `internal/controller/estop_actor.go`; D33). |

**Operations with no tool.** No tool exists for these, the policy engine refuses them as hard
invariants ([§3.2](#32-policy-engine-go-yaml-policy-files)), and the RBAC in [§3.6](rbac-and-admission.md#36-kubernetes-rbac-and-admission-policies) does not grant them:

| Forbidden operation | How Swarmada exposes it |
|---|---|
| Clear a stop | remove `swarmada.io/estop-triggered` (FleetZone, Robot, SwarmadaConfig); verb `estop-clear`; `swarmctl estop estop-clear` |
| Release a robot | delete a `ZoneMaintenance` or set its `spec.desiredState: Completed` (both resume robots); `autoResumeAfterMinutes` > 0; `swarmctl estop resume`; `swarmada.io/cancel-requested` and the `cancel` verb (a confirmed cancel releases the robot binding, RFC-0001 `#security-authorization-swarmada-rbac-roles`); `rollout-resume` |
| Delete | any `delete` on any kind, including declining a draft (a declined draft is deleted, `internal/webhook/fleettask_webhook.go`) |
| Approve its own request | create an `ApprovalDecision`; write `approvalrequests/status`; activate a draft; `trigger-response-approve` (RFC-0003) |
| Edit running work | `update`/`patch` on `FleetTask` (`desiredState`, `append`), `FleetAction`, `Robot`, `ZoneMaintenance`; `swarmada.io/requeue-requested` |
| Other custom verbs | `admit`, `reject`, `policy-reset`, `trigger-response-reject` |

## 3.2 Policy engine (Go, YAML policy files)

A pure function `Decide(request, principal, clusterSnapshot) → Decision`, where `Decision` is
one of `allow` (reads), `require_approval`, or `deny` with a stable reason
code (for example `DENY_FORBIDDEN_OPERATION`, `DENY_NAMESPACE`, `DENY_FIELD`,
`DENY_ZONE_NOT_FOUND`, `DENY_DRAFT_GATE_ABSENT`).

Two layers:

1. **Hard invariants compiled into the binary** and not overridable by YAML: the forbidden list
   in [§3.1](#31-mcp-server-fleetops-mcp-go); a drafted task always has `spec.draft: true`; write tools can only produce
   `require_approval` or `deny`; the requester can never be the approver; a request whose
   requester is unverified (`unverified:<name>`, [§3.3](approvals.md#33-approval-flow)) is never approved (D56); a request is filed
   only with a confirmed preview whose id equals the hash of its parameters (`DENY_NOT_CONFIRMED`,
   `DENY_PREVIEW_MISMATCH`, D74).
2. **YAML policy files** (`policies/*.yaml`, mounted from a ConfigMap) for site rules. Examples:
   allowed namespaces; allowed `FleetAction.spec.priority` values in a drafted task; allowed
   `ZoneMaintenance.spec.mode`; `autoResumeAfterMinutes` must be `0` (a non-zero value would
   resume robots without a person, i.e. a scheduled release); `requireEstopClearBeforeResume`
   may not be set to `false`; approver groups and `minApprovers` per request type; request TTL;
   a per-session limit on drafts created.

The engine runs twice for approval requests: in `fleetops-mcp` when the request is created, and in
`fleetops-approvals` immediately before execution, against fresh cluster state. A request that would
be denied at execution time is not executed. For a `DraftTask`, the execution re-check also re-runs
`check_task` and Swarmada's own admission as dry runs ([§3.10.6](guided-requests.md#3106-re-check-at-execution-d76), D76): a blocker gives
`DENY_CHECK_FAILED`, a dry run Swarmada refuses gives `DENY_SWARMADA_REFUSED` with Swarmada's
message, and nothing is written.

**Draft-gate check.** A draft holds work back only where Swarmada's `FleetTask` admission webhook
is served, because only then is `activate` enforced. RFC-0003 (`#resources-tasktrigger`) states
that on an install serving no admission webhooks the draft flag is not an authorization boundary.
Before a `DraftTask` or `ActivateTask` request is made, and again before `fleetops-approvals` executes
one, the cluster's `ValidatingWebhookConfiguration`s are read; if none with `failurePolicy: Fail`
covers `UPDATE` of `fleettasks.swarmada.io`, the policy engine returns `DENY_DRAFT_GATE_ABSENT`.
