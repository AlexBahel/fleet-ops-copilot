# Known limits

What is not built, not measured, or not enforced. Each item stays here until code and a test
remove it.

## Swarmada version

- Swarmada v0.4 is resolved through a local, uncommitted `go.work` until its public release
  (docs/DESIGN.md D1, D2). `go.mod` has no `require` for `github.com/swarmada/swarmada` yet, so a
  plain checkout does not build. The pin, the removal of `go.work`, and a full re-run of every
  test happen at the public release.
- Because of that, the Go jobs in `.github/workflows/ci.yml` are disabled (D26). Only the
  repository-hygiene job runs in CI; build, vet, lint, unit tests, envtest and fixture evals run
  with `make ci` on a developer machine.

## Not built yet

- Model backends: `claude` (Claude API) and `llamacpp` (`llama-server`). Only the `scripted`
  backend exists (`internal/llm`).
- The five agent roles (Router, Diagnose, Dispatch, Zone ops, Reporter) and their tool
  allow-lists. The tool-use loop (`internal/agent`) offers every tool.
- `foc-agent`, the web chat UI, and the OIDC pass-through for approving in a browser.
- Helm chart, `make foc-up`, NetworkPolicy, egress probe, OpenTelemetry tracing, `docs/RUNBOOK.md`.
- More than one approver per request (`minApprovers`): one accepted decision approves.

## Not measured yet

- No model has been evaluated. `evals/results/fixtures/results.json` is a harness self-test with
  the scripted backend: it shows the harness, tools, policy and approval path behave as designed
  on fixture states, and says nothing about a model's pass rate.
- Model latency (p50, p95) and cost per 100 requests, for both deployments. The latency in the
  fixture results covers tool execution against a test API server only.
- 8 of the 25 planned scenarios exist.

## Not enforced, or enforced only partly

- **Operator identity.** The operator and session id reach `foc-mcp` in MCP request `_meta`,
  set by the calling process. `foc-mcp` does not authenticate it. A client that can reach
  `foc-mcp` can name any operator as `requestedFor`, which weakens the rule that the requester
  cannot approve their own change. The ServiceAccount, `requestedBy`, and approver-authority
  checks do not depend on it.
- **Impersonation.** A person allowed to impersonate users can create a decision as another
  person; the admission policy sees the impersonated identity. This is how Kubernetes
  authentication works, not something the copilot can detect.
- **Draft gate detection.** `foc-mcp` and `foc-approvals` treat any validating webhook with
  `failurePolicy: Fail` covering `UPDATE` of `fleettasks.swarmada.io` as the gate. They do not
  verify that it is Swarmada's webhook or that it is serving.
- **ServiceAccount names** in the admission policies are fixed to namespace `foc-system`.
- **Per-session draft limit** is applied when a request is made, not when it is approved.
- **Estop attribution.** When `foc-approvals` triggers an approved zone stop, Swarmada records the
  executor's ServiceAccount as `swarmada.io/estop-actor` (its mutating webhook stamps the
  admission user). The approving person is recorded only in the copilot's `ApprovalRequest`,
  Events and audit file.
- **Activation is done by the approver, outside the copilot.** After approval the copilot gives
  the exact command (`status.instruction`) and watches for the task to leave `Draft`; it cannot
  tell who ran the command, and it waits without a deadline. swarmctl v0.4 has no activate
  subcommand, so the command is a `kubectl patch`. The planned end state is FOC-4's web UI
  performing the write with the approver's own credentials (OIDC, passed through for that one
  call); until then the approver runs the command.
- **Audit storage.** Each process writes its own JSONL file; there is no shared store, rotation
  or retention yet. A failed Event write is recorded in the JSONL file only.

## Test environment

- **Swarmada's admission webhooks do not run in envtest**, so the approval-path tests do not show
  what Swarmada v0.4 accepts. envtest runs a real `kube-apiserver` with the Swarmada CRDs and
  Swarmada's own RBAC roles, but none of the webhooks in Swarmada's
  `config/webhook/manifests.yaml`: its webhook code is in its `internal/webhook` package, which
  another module cannot import. `internal/testenv` registers two stand-ins written from that code:
  the FleetTask activation rule (validating) and the FleetZone estop-actor stamp (mutating).
  Not exercised at all: the rest of `vfleettask` (flow, bindings, producer contracts),
  `vfleetzone` (the `estop-trigger` SubjectAccessReview, composite-zone refusal),
  `vzonemaintenance` (zone exists), and every other Swarmada webhook. Running the copilot's
  approval path against a live Swarmada v0.4 control plane is FOC-1 work.
- Fixtures under `evals/fixtures/` are hand-written snapshots modelled on the Swarmada quickstart
  scenarios, not captured from a live run. The quickstart itself writes some status by hand (see
  its "Honest notes" section), and so do the fixtures.

## By design (not limits to remove)

- The copilot is not an emergency-stop path; `request_zone_stop` waits for a person.
- The copilot never ends a maintenance window, clears a stop, deletes, approves, or activates:
  a window it requested, or a draft nobody activates, stays until a person acts.
