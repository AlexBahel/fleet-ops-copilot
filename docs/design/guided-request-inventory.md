# Inventory (D70): what Swarmada v0.4 can express for a guided task request

Read-only inventory for rulings R62–R64. Source: the Swarmada module at the `go.mod` pin
(`go list -m -f '{{.Dir}}' github.com/swarmada/swarmada` →
`github.com/swarmada/swarmada@v0.0.0-20261104205854-558da47fdb44`, commit `558da47`). Paths below are
relative to that module unless they start with this repository's own directories. "RFC" means
`rfcs/dist/RFC-0001-core-spec.md`; `§` numbers are that document's.

Nothing here is a design decision. It records what the API accepts, what a robot action can report,
and what the scheduler checks, so the site catalogue (phase 1) maps only to what exists.

## 1. Work objects

### 1.1 FleetAction (`api/v1/fleetaction_types.go`)

One dispatch to one robot (RFC §9.1.4.7).

**Verbs (`spec.type`, closed enum):** `Navigate`, `PickUp`, `DropOff`, `Patrol`, `Charge`, `Clean`,
`Custom`, `External`. Per the type's own admission rule, the robot verbs are vocabulary: the control
plane branches on none of them except `External` (and the same-type test for supersession). A verb
outside the enum is `Custom` with `spec.customType` (1–63 chars, accepted only with `Custom`), shown in
the `Verb` printer column and sent to the adapter as `custom_type`.

**Spec fields a request can set:**

| Field | Meaning at v0.4 |
|---|---|
| `type`, `customType` | as above |
| `zone` | target zone; resolved to its leaf set by filter 2 (§3) |
| `requiredCapabilities[]` | names a robot must have `Active` (or `Degraded` with `acceptDegradedCapabilities`) in `status.capabilities` |
| `constraints{name: min}` | parametric minimums against `status.capabilities[].resolvedParameters` (filter 5) |
| `priority` | `Critical`/`High`/`Normal`/`Low`, default `Normal`. `Critical` and `High` preempt `Normal`/`Low` automatically with no confirmation (RFC §9.1.4.3, line 1035) |
| `acceptDegradedCapabilities` | pointer; unset uses `SwarmadaConfig.spec.scheduling.defaultAcceptDegradedCapabilities` |
| `estimatedDurationSeconds` | read by nothing at v0.4 |
| `payload` | opaque JSON object delivered to the adapter as `payload_json` on `validate_action` and on assignment (`fleetaction_payload.go`; RFC §9.1.4.7). Swarmada never parses it |
| `deadline` | latest time to be **assigned**; a `Pending` action past it fails with "deadline exceeded before scheduling" |
| `timeoutSeconds` | completion window from `status.startedAt`; enforced on `InProgress` only |
| `robotSelector` | pins one robot; skips filters 2, 3, 5 (they fail open) |
| `minBatteryPct` | battery floor (filter 4) |
| `preferredRobot` | soft rank, honoured when `honorPreferredRobot` (default true); unknown or ineligible names have no effect |
| `preferredManufacturer` | soft rank, when `preferSameManufacturer` |
| `onFailure` | `Requeue` (default; `retryPolicy` maxRetries 3, backoff 30 s), `Alert` (stays `Failed`, `FleetActionFailed` Event), `Abandon` |
| `desiredState` | `Running`/`Paused`/`Returning`/`Cancelled`; on a task member the FleetTask is authoritative |
| `executor` | `External` only; **no dispatch path at v0.4** (see 1.4) |

**Status a robot action reports (everything there is):**

| Field | Notes |
|---|---|
| `phase` | `Pending`, `Assigned`, `InProgress`, `Succeeded`, `Failed`, `Cancelled`, `Paused`, `Revoking`, `Preempted`. The only authoritative outcome (RFC §9.1.4.8) |
| `assignedRobot`, `assignmentGeneration`, `leaseExpiresAt` | who runs it |
| `pendingSince`, `scheduledAt`, `startedAt`, `completedAt`, `failedAt`, `completionTime`, `disconnectedAt` | timestamps |
| `progressPct` | adapter-reported 0–100, advisory |
| `message` | free text, "especially on failure"; the adapter's own words can reach it |
| `failureReason` | free text on `Failed`; five named zone refusals (`ZoneEntryDenied`, `ZoneCapabilityDenied`, `ZoneOperatingConstraintUnmet` (set while `Pending`), `ZoneRestrictionsUnresolved`, `ZoneUnknown`; `fleetaction_zone_refusals.go`) and "deadline exceeded before scheduling" |
| `cancellationReason`, `supersededBy` | `Operator`, `SupersededByDirective`, `CapabilityLoss` |
| `retryCount` | requeues so far |
| `outcomeCertainty` | **written by no controller at v0.4** (always empty) |
| `conditions[]` | written at v0.4: `AdapterDeclined` (adapter refused the concrete instance, with reason), `ZoneStopped` (destination under a stop, held `Pending`), `RenewalHeld`, `ExternalDependenciesReady`. `Scheduled`, `Started`, `DeadlineExceeded`, `TimedOut`, `Cancelling` are never written (RFC line 3532) |
| `result`, `events[]`, `parameterResolution[]` | **External actions only.** "No `fleet_adapter.v1` message carries a result … A robot action's `status.result` therefore stays absent" (RFC §9.1.4.8) |

So a robot action can report **that** it ran, where, by whom, when, how far, and why it failed. It
cannot report **what it found**: there is no structured result field for a robot action at v0.4. An
adapter can put text in `message` or `failureReason`, and nothing makes that structured or required.

Indirect evidence, on other objects, that the copilot can read after an action: `Robot.status.position`,
`status.currentZone`, `status.batteryPercent`, `status.assignedAction`; Events on the action
(`FleetActionFailed` with `onFailure: Alert`, `AdapterDeclined` warnings, `ActionCancelledByOperator`).

### 1.2 FleetTask (`api/v1/fleettask_types.go`, `fleettask_flow_types.go`)

The composite; never crosses the adapter boundary.

- `spec.actions[]` (min 1, append-only after creation): `name`, `action` (a full `FleetActionSpec`,
  copied verbatim to the child), `dependsOn[]` (AND-join, acyclic; **validated on reconcile of a
  non-draft task, not at admission**, so a dangling or cyclic entry is admitted and fails later),
  `startCondition` (`Assigned`/`InProgress`/`Succeeded`, default `Succeeded`), `trigger`
  (`Auto`/`OnEvent`), `compensation`.
- `spec.completionPolicy` `All`/`Any`/`Quorum` (+`quorum`); `spec.failurePolicy`
  `FailFast` (default)/`ContinueOthers`/`Compensate`, immutable.
- `spec.draft`: while true no child is generated, phase `Draft`; clearing it needs the `activate`
  verb and is refused to every ServiceAccount where the webhooks are served.
- `spec.inputs[]` (typed, string-encoded; **no controller resolves `fromTaskInput` at v0.4**).
- `spec.flow` (optional, immutable, exclusive with every `dependsOn`): `run`, `sequence`, `parallel`
  (+join), `loop` (`Once`/`Times`+`times`/`Forever` with a required bound `until`, `maxIterations` or
  `maxDurationSeconds`), `select` (branches on a member's result or event, which a robot action does
  not produce, §1.1).
- Status: `phase` (`Draft`, `Pending`, `Running`, `Succeeded`, `Failed`, `Compensating`,
  `Compensated`, `Cancelled`), `actionSummary`, `startedAt`, `completionTime`, `conditions`
  (`DependencyGraphValid`, `ChildrenAdmitted`, `CompletionPolicyValid`), and per member `actionRef`,
  `phase`, `assignedRobot`, `dependenciesMet`, `iteration`, `iterationsCompleted`, `iterationsFailed`,
  `loopStartedAt`, `recentIterations[]` (bounded), `compensationPhase`; `attempt` is written by nothing.

### 1.3 ZoneMaintenance (`api/v1/zonemaintenance_types.go`)

Not a FleetTask; the copilot already requests it (`request_zone_maintenance`, DESIGN §3.1).
Spec: `scope{type: Zone|Namespace, zoneName}`, `mode` `Graceful`/`Immediate`, `desiredState`
`Active`/`Completed`, `reason`, `scheduledStart`, `autoResumeAfterMinutes` (the copilot pins 0, D10),
`requireEstopClearBeforeResume`. Status: `phase` (`Scheduled`/`Active`/`Completed`/`Cancelled`),
`activatedAt`, `activatedBy` (not written at v0.4, RFC line 510), `completedAt`, `autoResumeAt`,
`pausedRobots[]`, `pausedRobotsCount`, `windingDownRobots[]` (with `assignedAction`,
`estimatedCompletionAt`), `windingDownRobotsCount`, `continuousCapabilities[]`, `conditions`.

### 1.4 External actions and compute endpoints

`spec.executor` (`ComputeEndpoint`, `ResultSink`, `ArtifactStore`, sensor-stream inputs) is the only
place a structured result (`status.result.values`, `artifacts[]` with digest and pose) exists. Every
field says the same thing: "There is no dispatch path at v0.4. Nothing in the reference control plane
resolves an endpoint address, activates a run, or calls the compute runtime service." An External
member is admitted and never runs.

## 2. Robots, capabilities, locations

### 2.1 Robot (`api/v1/robot_types.go`)

Status fields relevant to eligibility: `phase` (`Discovered`, `Idle`, `Assigned`, `InProgress`,
`Charging` (never written at v0.4), `Error`, `Offline`, `Maintenance`), `estopState`
(`Normal`/`Stopping`/`Stopped`/`Resuming`/`Failed`), `capabilities[]` (`name`, `status`
`Active`/`Degraded`/`Paused`/`Inactive`/`Unavailable`/`Failed`, `paused`, `reason`,
`resolvedParameters{}`, `degradedSchedulable`, `degradedSince`), `installedModels[]`,
`modelGrantedCapabilities[]`, `batteryPercent`, `position`, `currentZone`, `containingZones[]`,
`honouredOperatingConstraints[]`, `assignedAction`, `hardware[]`, `health` (gates nothing, RFC line
3521), `connectivity`, `sensorStreams[]`. Spec: `adapter.name`, `zone`, `capabilities[]`,
`constraints.minBatteryPctForAction`, `charging.{dockName, minBatteryPctToCharge, targetBatteryPct}`
(the charging thresholds have no control-plane behaviour at v0.4, RFC §4 item 16).

**Capability names** are free strings; the API defines none. In the quickstart
(`config/samples/demo_a.yaml`) and this repository's fixtures the robots declare `navigation` and
`camera_front`. The simulated adapter's catalog lists `navigation.2d` and `transport.payload`
(`adapters/simulation/sim_adapter.py:102`), which no quickstart robot declares. Admission compares
the catalog with the action's **type** (and `customType`) only, not with its capabilities
(`validateSupported`, `internal/webhook/fleetaction_webhook.go:215`), so that mismatch blocks nothing
for `Navigate`; a `PickUp` requiring `transport.payload` would find no robot.

### 2.2 Adapter catalog (`api/v1/fleetadapter_types.go`)

`FleetAdapter.status.supportedActions[]`: `actionType`, `customType` (contract 1.4.0),
`requiredCapabilities[]`, `params[]` and `results[]` (`name`, `unit`, `kind`
number|string|bool|enum, `allowed[]`, `required`). Empty `results` means unknown. The quickstart's
simulated adapter serves `Navigate`, `PickUp`, `DropOff`, `Patrol` (each reads optional payload `x`,
`y` in metres; with none it drives to (10, 0)), `Charge` (moves nothing, ends on its own) and
`Custom` with the single verb `explore-floor` (declares results `map-ref`, `map-sha256`, `covered-m2`,
which no wire message can carry back, §1.1). It refuses any other `Custom` verb.

### 2.3 How a location is expressed

- **Zone:** `FleetAction.spec.zone` names a FleetZone; any level of the containment tree (a
  composite zone matches only a robot whose `currentZone` is the composite itself, RFC line 6360).
- **Point:** only inside the opaque `spec.payload`. Swarmada does not route robots (RFC §4 item 1).
- **Waypoint:** `FleetZone.spec.waypoints[]` (`name`, `x`, `y`, `yaw`, `altitude`) is "a
  DECLARATIVE shared vocabulary of named poses … A FleetAction's destination rides in its opaque
  spec.payload and MAY reference a waypoint by name … At v0.4 waypoints are stored and delivered to
  no adapter" (RFC line 5688). The quickstart zone `warehouse-a` declares `receiving-dock-1`,
  `staging-shelf-a`, `charging-bay`, and the live fixtures hold them.
- **Charging dock:** `FleetZone.spec.sharedResources[]` of type `ChargingDock`; `Robot.spec.charging.dockName`
  must name one in the robot's zone chain.

Consequence: a request can carry a location **only through the payload**, and an adapter that is not
told the coordinates does not know where a waypoint is. For the simulated adapter the payload must
carry `x` and `y`. A copilot that resolves a waypoint name to its pose from the FleetZone and writes
`{"waypoint": "<name>", "x": …, "y": …}` expresses the location in a form both a person and the
quickstart's adapter can read. That is a convention of this site, not a Swarmada contract.

## 3. Scheduler eligibility (RFC §9.4.2, lines 15021–15180)

A FleetTask is not scheduled as a whole; each child action is. Candidate filters, as documented (the
reference implementation status is the RFC's own maturity table at the top of §9.4.2):

| # | Filter | Reads | v0.4 status |
|---|---|---|---|
| 1 | robot `status.phase == Idle` | Robot | implemented |
| 2 | `status.currentZone` in the leaf set of `spec.zone` (empty `currentZone` excluded) | FleetZone tree, Robot | implemented; skipped for a pinned robot |
| 3 | every `requiredCapabilities` entry `Active` (or `Degraded` with accept) in `status.capabilities` | Robot | implemented; skipped when pinned |
| 4 | `batteryPercent` ≥ max(`spec.minBatteryPct`, `Robot.spec.constraints.minBatteryPctForAction`); unreported battery with a floor set excludes | Robot | implemented. **No exemption for `Charge`** (`excludedByBatteryFloor`, `internal/controller/dispatch_filters.go:440`): a robot below its floor cannot be given a Charge action |
| 5 | parametric `constraints` | Robot capabilities | implemented; skipped when pinned |
| 6 | `robotSelector` pins one robot | — | implemented |
| 7 | no model granting a required capability is `Updating` | Robot | implemented |
| 8 | not in `pausedRobots`/`windingDownRobots` of an `Active` ZoneMaintenance | ZoneMaintenance | implemented, fails closed |
| 9 | adapter `Connected`, `conformance: Passed`, contract version in range, `SafetyStream`, latch contract, `servesRobotClasses` lists the class | FleetAdapter | partial (no `SafetyStream`; latch version only behind a flag) |
| 10 | robot not under a stop: own `estopState`, zone chain estop (annotation or processed), namespace stop on `SwarmadaConfig`, duplicate wire robot-id | Robot, FleetZone, SwarmadaConfig | partial (reported stops not seen) |
| 11 | zone restrictions (entry or capability denied, unknown zone → terminal `Failed`; unresolvable → held `Pending`) | FleetZone `status.restrictions.effective[]` | partial (capability narrowing over the subtree not done); eliminates the action, not a candidate |
| 12 | `honouredOperatingConstraints[]` covers every operating rule in force at the robot's leaf and containing zones | Robot, FleetZone | implemented |
| — | resume window: `estopState: Resuming` withheld until released | Robot | named, not numbered |
| — | robot with `deletionTimestamp` is not a candidate | Robot | not numbered |
| — | assignment-time `validate_action`: the chosen robot's adapter judges the concrete type + payload; `servable=false` drops the candidate (`AdapterDeclined`), `unsupported` passes | adapter | per instance, not visible beforehand |

Rank (soft): `preferredRobot` (if honoured), `preferredManufacturer` (if enabled), battery highest
first. No proximity or load ranking.

Before scheduling, at **admission** of each FleetAction (so at activation of a draft, when the
children are created): catalog check by type/`customType` (fails open when no adapter has a catalog),
External references, and the per-zone Pending cap `SwarmadaConfig.spec.scheduling.maxPendingActionsPerZone`
(`ValidateCreate`, `internal/webhook/fleetaction_webhook.go:131`). At admission of a **FleetTask**:
flow structure, parameter bindings and producer contracts only (`fleettask_webhook.go:132`). Both
webhooks are `sideEffects: None`, so a server-side dry run reaches them.

Zone state that holds or refuses work: estop on the zone or an ancestor (`status.estopStatus`
`Triggered`/`ChildTriggered`, annotation `swarmada.io/estop-triggered`), an `Active` ZoneMaintenance
covering it, `status.restrictions.effective[]`, `maxConcurrentRobots` (enforced by the TDE at the zone
boundary, not by a candidate filter), and an unknown zone name.

## 4. Classification of the proposed kinds

(a) expressible, with its result reportable from a field · (b) expressible, but the result asked
for has no field · (c) needs a Swarmada change.

| Kind | Swarmada mapping at v0.4 | What is reportable | Class |
|---|---|---|---|
| **Inspect: damage** (look for damage at a place) | `Navigate` with `requiredCapabilities [navigation, camera_front]`, `zone`, payload `{waypoint, x, y, lookFor: damage, instruction}`; or `Custom` + `customType` where the site's adapter serves that verb (the quickstart's does not, and admission then refuses it) | arrival and completion (`phase`, `startedAt`, `completedAt`, `assignedRobot`), failure reason. Damage found / not found, where, photos: **no field** | **(b)** |
| **Inspect: location** (state of a spot: clear, blocked, occupied) | same | same; the observed state has no field | **(b)** |
| **Inspect: both** | same, `lookFor: [damage, location]` | same | **(b)** |
| **Inspect: something else** | one of the above with the person's text as instruction, or a composition of basic steps | as the kind it maps to | (b), or (c) if it needs a result |
| **Move** (send a robot to a waypoint or zone) | `Navigate`, `zone`, payload `{waypoint, x, y}` | `phase: Succeeded` = arrived; `Robot.status.position`/`currentZone` corroborate | **(a)** |
| **Deliver** (pick up at A, drop at B) | two members: `PickUp` then `DropOff` with `dependsOn`, payloads `{waypoint, x, y}`, capability for carrying (`transport.payload` in the sim's catalog), optional `constraints.maxPayloadKg` | each member's `phase`; the task's `phase` and `actionSummary` | **(a)**; on the quickstart no robot declares `transport.payload` (a blocker, not a gap) |
| **Charge** | `Charge`, `robotSelector` (or `preferredRobot`), payload `{dock}` naming a `ChargingDock` or the `charging-bay` waypoint | the action's `phase`; `Robot.status.batteryPercent` afterwards | **(a)** for "the charge ran". Caveats: Swarmada never sends a robot to charge on its own; filter 4 refuses a Charge to a robot below its battery floor; the sim's Charge moves nothing |
| **Patrol** (go round a route, once or N times) | `Patrol`, `zone`, payload `{route: [waypoints with x, y]}`; repeats as `spec.flow.loop` (`Times` + `times`, or `Forever` with `maxIterations`/`maxDurationSeconds`) | per member `phase`, `iterationsCompleted`, `iterationsFailed`, `recentIterations[]` | **(a)** for completion and count. What was seen on patrol: **(b)**. The sim's Patrol drives to one `x`,`y` |
| **Survey / map** | `Custom` `explore-floor` where served (the sim serves it, with bounds in the payload) | `phase`; the declared results (`map-ref`, `covered-m2`) have no field to land in | **(b)** |
| **Zone maintenance** | `ZoneMaintenance` (existing request type) | `phase`, `activatedAt`, `pausedRobots[]`, `windingDownRobots[]`, counts, `continuousCapabilities[]` | **(a)** |

### Needs a Swarmada change (class (c), for draft 5)

| # | Gap | Where it shows |
|---|---|---|
| C1 | A structured result for a robot action: a field on `ActionStatusUpdate` and the `status.result` of a non-External action, so "damage: yes/no", "spot: blocked" or a map reference can land | every inspect kind; patrol findings; survey |
| C2 | Evidence artifacts (image references with digest and pose) for a robot action; today `ActionResultArtifact` exists only for External | inspect with photos |
| C3 | A dispatch path for `External` actions (ComputeEndpoint runs, sensor-stream inputs) | inspect done by a vision service on a robot's camera stream |
| C4 | Waypoints delivered to the adapter, or a typed destination, so a waypoint name is enough | every kind with a location; today the copilot must copy `x`, `y` into the payload |
| C5 | An eligibility dry run: "would this action find a robot now, and if not, which filter withholds whom" without creating it, including the adapter's `validate_action` (ruling R62 b) | `check_task` can only approximate filters 1–12 from reads, and cannot see `validate_action` |
| C6 | A Charge exemption, or a charging path, for a robot below `minBatteryPctForAction` | charge request for a nearly empty robot |
| C7 | `dependsOn` validated at admission (today on reconcile of a non-draft task) | a bad dependency in a draft is found only after activation |

## 5. What a pre-check can see from reads (input to `check_task`)

fleetops-mcp's ServiceAccount reads Robots, FleetTasks, FleetActions, FleetZones, ZoneMaintenances,
DiscoveredRobots, FleetAdapters, RobotClasses and TaskTriggers (DESIGN §3.6). From these it can
evaluate filters 1–8, 11, 12, the resume window, the deletion exclusion, filter 9 except the contract
range and `SafetyStream`, and filter 10 except the namespace stop and reported stops. The namespace
stop and the per-zone Pending cap need `SwarmadaConfig`, which fleetops-mcp cannot read today. The
adapter's per-instance `validate_action` is not observable by any client before assignment (C5). A
dry-run `create` reaches Swarmada's FleetTask and FleetAction webhooks (both `sideEffects: None`); the
FleetTask dry run checks flow, bindings and producer contracts, and only a FleetAction dry run per
member checks the catalog and the Pending cap. The API server authorizes a dry run like the real
request, so it needs `create` on those resources.
