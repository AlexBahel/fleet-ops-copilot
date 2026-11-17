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
  `fleetops audit verify` can detect edits or gaps), on a PVC.
- **Kubernetes Events** on the `ApprovalRequest` (reasons `RequestCreated`, `PolicyDenied`,
  `Approved`, `Rejected`, `Executed`, `ExecutionFailed`, `DecisionRefused`, `Expired`).

Audit lines and Events pass through the same redaction as traces ([§3.8](components.md#38-observability-and-secrets)).
