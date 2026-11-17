# Fleet Ops Copilot — Approvals and audit

How a request is decided and executed, and what is recorded. The summary is in
[`DESIGN.md`](../DESIGN.md).

## 3.3 Approval flow

Two copilot-owned, namespaced CRDs in API group `fleetops.alexbahel.github.io` (version `v1alpha1`):
`ApprovalRequest` and `ApprovalDecision`. They are not Swarmada resources and are installed by
the copilot's Helm chart.

1. `fleetops-mcp` creates an `ApprovalRequest`: type, parameters, policy decision, requesting user,
   copilot session id, the confirmed preview (`spec.preview`) and the checks it passed when filed
   (`spec.checks`, D77). It is created only after the requester confirmed that preview ([§3.10.5](guided-requests.md#3105-preview-and-confirmation-d74)).
   The requesting user (`spec.requestedFor`) is the username of the caller's own bearer token,
   which `fleetops-mcp` verifies with a `TokenReview`; the client sends it in the `Authorization`
   header of its MCP requests (`fleetops chat` through `fleetops-agent`, the eval harness).
   An operator name in MCP `_meta` is only asserted: without a verified token the requester is
   `unverified:<asserted name or anonymous>` (D57). `fleetops-approvals` sets `status.state: Pending`.
2. An approver runs `fleetops approve <id>` (or `fleetops reject <id>`) with their own kubeconfig. The CLI
   reads the caller's username with a `SelfSubjectReview` and creates an `ApprovalDecision`
   (`requestRef`, `decision`, `decidedBy`). A ValidatingAdmissionPolicy installed by the chart
   rejects the decision unless `decidedBy` equals the authenticated username, rejects any
   ServiceAccount, and rejects every update (a decision is immutable). RBAC grants `create` on
   `approvaldecisions` only to the approver Role bound to people.
3. `fleetops-approvals` validates the decision: an approval of a request whose requester is
   unverified is refused (`DENY_REQUESTER_UNVERIFIED`, D56; a rejection is accepted); the decider
   is not the requesting user (unless the policy file allows it on a dev cluster); a
   `SubjectAccessReview` shows the decider could
   perform the underlying Swarmada operation themselves (`create` on `fleettasks`; `create` on
   `zonemaintenances`;
   `patch` and `estop-trigger` on that `fleetzone`; `activate` on that `fleettask`); the request
   has not expired; the policy engine, re-run on fresh state, still returns `require_approval`.
   It then sets `status.state: Approved` and records `approvedBy` before any effect.
4. Execution:
   - draft, zone maintenance and zone stop: `fleetops-approvals` performs the single write as its own
     ServiceAccount (a deterministic name, so a retry cannot write twice) and records
     `executedAt` and the object reference. Before a draft it re-runs `check_task` and the dry runs
     and records them in `status.executionChecks`; a failure refuses the write with the reason
     (D76);
   - activation: the copilot never writes it. `fleetops-approvals` records the approval with the exact
     command in `status.instruction` (`fleetops activate <request> -n <namespace>`, D79), and
     `fleetops approve` prints it. The approver runs it with their own credentials; Swarmada's webhook
     authorizes it against the approver's `activate` verb. `fleetops-approvals` watches for the task to
     leave `Draft`, then records `Executed`, an `ActivationObserved` Event, and an
     `activation_observed` audit line (D28). It then watches the task to a terminal phase and
     records the outcome; with the failure handling "prepare a follow-up request" it also records
     a proposed follow-up, which is never filed without a new preview and confirmation (D78).
5. Requests expire after the policy TTL. Rejected and expired requests are terminal.

The web UI shows requests and decisions and never decides (D52): for a pending request it
shows the exact write (D53) and the `fleetops approve` / `fleetops reject` command for a person to run
with their own kubeconfig. It has no login and sends no token, so a request filed from it is
recorded `unverified:<name>` and can be rejected but never approved (D56).

## 3.4 Audit

Every tool call, policy decision, request, approval, rejection, execution, and failure is
written twice:

- **JSONL** append-only file (one JSON object per line, with the SHA-256 of the previous line so
  `fleetops audit verify` can detect edits or gaps), one file per process on an `emptyDir` volume
  (`config/deploy`, the chart). It is lost when the pod is replaced, by a rollout, an upgrade or an
  eviction (KNOWN-LIMITS, "Audit storage"); a persistent volume is P5 in `docs/BACKLOG.md`.
- **Kubernetes Events** on the `ApprovalRequest` (reasons `RequestCreated`, `PolicyDenied`,
  `Approved`, `Rejected`, `Executed`, `ExecutionFailed`, `DecisionRefused`, `Expired`).

Audit lines and Events pass through the same redaction as traces ([§3.8](components.md#38-observability-and-secrets)).

## Step by step: a draft FleetTask and its activation

The [diagram](../architecture/approval-flow.svg) shows the main steps; this is the full list. A zone
maintenance window or a zone stop follows steps 1 to 10, 12 and 13, with its own write in step 12
and no step 11 ([§3.10.6](guided-requests.md#3106-re-check-at-execution-d76) applies to drafts only).

**Request**

1. The person asks for work ("inspect the receiving dock") with `fleetops chat`, which sends their
   own bearer token, or in the web chat, which sends none.
2. fleetops-agent calls the read tools and `check_task` on the proposed draft through fleetops-mcp,
   with `Authorization: Bearer <the person's token>`.
3. fleetops-agent shows at most two numbered questions, then a preview with the checks. The model
   is offered no request tool.
4. The person replies `1` (confirm).
5. fleetops-agent calls `request_draft_task` with the stored parameters and the preview id
   (`confirmedPreview` in `_meta`).
6. fleetops-mcp verifies the token with a TokenReview: the requester is the authenticated username;
   without a verified token it is `unverified:<name>` (D56, D57).
7. fleetops-mcp recomputes the preview id (`DENY_NOT_CONFIRMED`, `DENY_PREVIEW_MISMATCH`), runs the
   policy engine, and checks the draft gate: a fail-closed webhook covers `UPDATE` of
   `fleettasks.swarmada.io` (`DENY_DRAFT_GATE_ABSENT`).
8. fleetops-mcp creates the `ApprovalRequest` with `requestedFor` = the reviewed username; the
   `fleetops-requests` admission policy requires `requestedBy` = the creating identity.
   fleetops-approvals sets `status.state: Pending`.

**Decision**

9. An approver runs `fleetops approve <id>`, which creates an `ApprovalDecision` with their own
   credentials; the `fleetops-decisions` admission policy refuses a ServiceAccount and requires
   `decidedBy` = the caller.
10. fleetops-approvals checks: the requester is not unverified (`DENY_REQUESTER_UNVERIFIED`: a request
    from the web UI stops here); the decider is not the requester (`DENY_SELF_APPROVAL`); a
    SubjectAccessReview shows the decider may create `fleettasks` (`DENY_APPROVER_NOT_AUTHORIZED`);
    the request has not expired (`DENY_EXPIRED`); the policy, re-run on fresh state, still requires
    approval. Then `status.state: Approved` and `approvedBy`.

**Execution**

11. `check_task` again on fresh state, and dry runs of the FleetTask and of each member
    (`DENY_CHECK_FAILED`, `DENY_SWARMADA_REFUSED`: nothing is written).
12. fleetops-approvals creates the FleetTask with `spec.draft: true` and a deterministic name
    (admission policy `fleetops-approvals-drafts-only`); Swarmada's FleetTask webhook validates the
    create.
13. `status.state: Executed`; an Event; an audit line.

**Activation**

14. The person asks to activate it: the same questions, preview and confirmation.
15. fleetops-agent calls `request_activate_task` with the confirmed preview.
16. fleetops-mcp creates an `ApprovalRequest` of type `ActivateTask`.
17. An approver runs `fleetops approve <id>`.
18. fleetops-approvals makes the checks of step 10 (the SubjectAccessReview is for `activate` on that
    FleetTask) and records `status.instruction` = `fleetops activate <request> -n <namespace>`.
19. A person Swarmada allows to activate runs `fleetops activate <request>` with their own credentials:
    it refuses a ServiceAccount, runs `check_task` with the caller's reads, records an
    `ApprovalDecision` of type `Activation`, and sends the merge patch `{"spec":{"draft":false}}`.
    Swarmada's FleetTask webhook requires the `activate` verb, which it refuses to every
    ServiceAccount.
20. fleetops-approvals sees the task leave `Draft`: it links the Activation record (`activatedBy`),
    sets `Executed`, writes an `ActivationObserved` Event and an `activation_observed` audit line, then
    watches the task to a terminal phase and records the outcome.

## Refusal codes

Every `DENY_*` code the copilot returns (`internal/policy/policy.go`). The policy engine runs in
fleetops-mcp when a request is filed and again in fleetops-approvals before execution
([§3.2](tools-and-policy.md#32-policy-engine-go-yaml-policy-files)).

| Code | Raised by | When |
|---|---|---|
| `DENY_FORBIDDEN_OPERATION` | policy engine | an operation on the forbidden list ([§3.1](tools-and-policy.md#31-mcp-server-fleetops-mcp-go)), or an unknown one |
| `DENY_NAMESPACE` | policy engine | the namespace is not in the policy's `namespaces` |
| `DENY_MALFORMED` | policy engine; request tools | a request without its required fields, a bad scope type or task name, or no operator identity or session id |
| `DENY_FIELD` | policy engine; dialogue engine (D93) | a draft without `spec.draft: true`, too many actions, an External action, an action type or priority the policy does not allow, a maintenance mode the policy does not allow, namespace-scope maintenance when not allowed, `requireEstopClearBeforeResume: false` |
| `DENY_RELEASE` | policy engine | `autoResumeAfterMinutes` other than 0 (D10) |
| `DENY_DRAFT_GATE_ABSENT` | policy engine | no fail-closed admission webhook covers `UPDATE` of `fleettasks` (D22), for a draft or an activation |
| `DENY_DRAFT_LIMIT` | policy engine | the session has created `maxDraftsPerSession` drafts |
| `DENY_TASK_NOT_FOUND` | policy engine | an activation names a FleetTask that does not exist |
| `DENY_TASK_NOT_DRAFT` | policy engine | an activation names a FleetTask that is not a draft |
| `DENY_ZONE_NOT_FOUND` | policy engine | a maintenance window or zone stop names a FleetZone that does not exist |
| `DENY_ZONE_STOP_DISABLED` | policy engine | `zoneStop.enabled: false` |
| `DENY_ALREADY_STOPPED` | policy engine | the zone already has a stop in force |
| `DENY_NOT_CONFIRMED` | fleetops-mcp (D74) | a request without a confirmed preview |
| `DENY_PREVIEW_MISMATCH` | fleetops-mcp (D74) | the confirmed preview's id differs from the id recomputed from the request |
| `DENY_CHECK_FAILED` | fleetops-mcp at filing; fleetops-approvals before execution (D76) | `check_task` reports a blocker on current state |
| `DENY_SWARMADA_REFUSED` | fleetops-approvals (D76) | Swarmada refused a dry run of the draft or of a member |
| `DENY_DECIDER_MISMATCH` | fleetops-approvals | the decision's `decidedBy` is not the user who made it |
| `DENY_SERVICE_ACCOUNT_APPROVER` | fleetops-approvals | the decider is a ServiceAccount |
| `DENY_NOT_PENDING` | fleetops-approvals | the request is no longer `Pending` |
| `DENY_EXPIRED` | fleetops-approvals | the request expired before the decision (policy `approval.ttl`) |
| `DENY_REQUESTER_UNVERIFIED` | fleetops-approvals (D56) | an approval of a request whose requester is `unverified:<name>`; a rejection is accepted |
| `DENY_SELF_APPROVAL` | fleetops-approvals | the decider created the request, or asked for the change (the latter allowed only by `approval.allowSelfApproval`, for a dev cluster) |
| `DENY_APPROVER_NOT_AUTHORIZED` | fleetops-approvals | a SubjectAccessReview shows the decider could not make the change themselves (D7) |
