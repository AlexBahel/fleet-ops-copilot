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

- The five agent roles (Router, Diagnose, Dispatch, Zone ops, Reporter) and their tool
  allow-lists. The tool-use loop (`internal/agent`) offers every tool.
- `foc-agent`, the web chat UI, and the OIDC pass-through for approving in a browser.
- Helm chart, `make foc-up`, NetworkPolicy, egress probe, OpenTelemetry tracing, `docs/RUNBOOK.md`.
  Until the chart exists, `make deploy-kind` installs plain manifests (`config/deploy`) on a kind
  cluster; the image is built from binaries compiled on the host.
- More than one approver per request (`minApprovers`): one accepted decision approves.

## Not measured yet

- No model evaluation of record has been run. `evals/results/fixtures/results.json` is a harness
  self-test with the scripted backend: it shows the harness, tools, policy and approval path
  behave as designed on fixture states, and says nothing about a model's pass rate.
- Model latency (p50, p95) and cost per 100 requests, for both deployments. The latency in the
  fixture results covers tool execution against a test API server only.
- The `llamacpp` backend (`internal/llm/llamacpp.go`, `foc-eval -backend llamacpp`) and the
  air-gapped manifests (`config/airgap/`) have not run against a model or a cluster: the backend
  is tested against a fake server, the manifests with `kubeconform`, and of the `hack/airgap.mk`
  targets only `validate-airgap` has run.
- Sampling is at each API's defaults (D43). The Claude API rejects `temperature` on the models
  used, so repeated claude runs vary; `results.json` records the settings sent (`backendSettings`).
- No eval scenario reaches `DENY_DRAFT_GATE_ABSENT`: fixture evals always run with the draft
  gate served. The refusal is covered by the policy unit tests (`internal/policy/policy_test.go`),
  an envtest test (`test/envtest/approval_test.go`) and the live test (`test/live`).

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
- **Estop attribution.** When `foc-approvals` triggers an approved zone stop, Swarmada attributes
  it to the executor's ServiceAccount: its mutating webhook stamps `swarmada.io/estop-actor`,
  its controller seals that actor into an `ESTOP_TRIGGERED` entry of Swarmada's safety audit log
  and then removes the annotation (D33). The approving person is recorded only in the copilot's
  `ApprovalRequest`, Events and audit file.
- **Activation is done by the approver, outside the copilot.** After approval the copilot gives
  the exact command (`status.instruction`) and watches for the task to leave `Draft`; it cannot
  tell who ran the command, and it waits without a deadline. swarmctl v0.4 has no activate
  subcommand, so the command is a `kubectl patch`. The planned end state is FOC-4's web UI
  performing the write with the approver's own credentials (OIDC, passed through for that one
  call); until then the approver runs the command.
- **Audit storage.** Each process writes its own JSONL file; there is no shared store, rotation
  or retention yet. In `config/deploy` the files are on `emptyDir` volumes and are lost when a
  pod is replaced. A failed Event write is recorded in the JSONL file only.

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
  served (`config/controlstream-tls` applied over the quickstart). It has been run on the release
  candidate only; a run after the public release pin has not happened yet. To read Swarmada's
  safety audit log it starts an ephemeral busybox container in the manager pod that mounts the
  manager's `audit-log` volume read-only as user 65532; both names come from Swarmada's
  manifests and are not checked. The quickstart's own overlay serves no admission webhooks, and
  there the copilot refuses every draft (`DENY_DRAFT_GATE_ABSENT`), so dispatch cannot be used on
  a plain quickstart install.
- Fixtures directly under `evals/fixtures/` are hand-written snapshots modelled on the Swarmada
  quickstart scenarios. Those under `evals/fixtures/live/` were captured from live quickstart runs
  of the release candidate after the quickstart script exited (its simulated adapter stops then,
  so robot phase and connectivity drift); they differ from the hand-written ones in places (D31). The quickstart itself writes some status by hand (see
  its "Honest notes" section), and so do the fixtures.

## By design (not limits to remove)

- The copilot is not an emergency-stop path; `request_zone_stop` waits for a person.
- The copilot never ends a maintenance window, clears a stop, deletes, approves, or activates:
  a window it requested, or a draft nobody activates, stays until a person acts.
