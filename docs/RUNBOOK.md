# Fleet Ops Copilot — Runbook

**Status: draft.** Each command below follows the chart and the code on main. `make foc-up`
(section 0, with the trace backend of section 8) has run on one kind cluster (2026-11-05); the
other sections have not been run as a sequence. Items that depend on parts not built yet (the
air-gapped profile in the chart) say so.

Conventions: every `kubectl` and `helm` command names its context explicitly. The examples use a
kind cluster `foc-<name>`, context `kind-foc-<name>`, a kubeconfig file that holds only that
cluster (`$KUBECONFIG`), the copilot namespace `foc-system`, and the Swarmada namespace
`warehouse-a`.

```bash
export KUBECONFIG=$PWD/local/foc-<name>.kubeconfig   # dedicated file, mode 600
CTX=kind-foc-<name>
```

## 0. Quick start on kind: `make foc-up`

One command from an empty machine state to a running stack. Needs Docker (running), kind,
kubectl, helm 3.8+, jq, openssl, curl, make, Go and python3 (Swarmada's live quickstart runs a
simulated adapter with the first `python3` on `PATH`, which must import `grpc` and
`google.protobuf`; `make foc-up` checks this before it creates anything and stops with the fix,
R59, D82). Everything it keeps (kubeconfig, the people's tokens and kubeconfigs, the
Swarmada copy, logs) is in `$FOC_UP_DIR`, by default `~/.local/state/fleet-ops-copilot/foc-up`,
**outside the repository**; the scripts refuse a directory inside it. It never reads or writes
`~/.kube/config`:

```bash
make foc-up                       # prints the plan's stages and, at the end, the elapsed time
FOC_UP_DRY_RUN=1 make foc-up      # prints every command that would change anything, runs none
FOC_UP_TRACING=1 make foc-up      # also installs the optional trace backend (section 8)
make foc-down                     # removes cluster foc-up, its kubeconfig and the people's tokens
```

What it does, in order (each stage prints its elapsed time; the total is the last line):

1. Creates the people's token file (section 7) and the kind cluster `foc-up` with that file wired
   into the API server. Node image `kindest/node:v1.37.0`, pinned by digest.
2. Checks that sam's, dana's and eve's tokens authenticate as sam, dana and eve. If the cluster
   already existed without the token configuration this fails at once: run `make foc-down foc-up`.
3. Runs Swarmada's warehouse quickstart (`examples/warehouse-quickstart/run.sh`, scenario
   `healthy-fleet`, `FOC_UP_SCENARIO` to change it) from a writable copy of the module at the
   commit `go.mod` pins, in `$FOC_UP_DIR/swarmada`. It builds Swarmada's controller image and
   leaves a simulated adapter and a port-forward running in the background; `make foc-down`
   stops them.
4. Turns on Swarmada's admission webhooks (`config/controlstream-tls`, section 6) and runs
   `scripts/foc-up-preflight.sh` until it passes (up to 3 minutes).
5. Builds the copilot image, loads it into the cluster, applies the CRDs and installs the chart
   into `foc-system`; binds the people's roles in `warehouse-a`.
6. Checks health: foc-mcp and foc-approvals rollouts, foc-mcp's `/healthz`, and, if the Secret
   `fleet-ops-copilot-claude` exists, foc-agent. Without the Secret foc-agent does not start and
   the script says so. To create it from a key file you hold (nothing calls the model):
   `FOC_UP_ANTHROPIC_KEY_FILE=key.txt make foc-up`, or section 2.

Then `make foc-live` runs the live approval paths with requests filed by sam's and dana's own
tokens (verified requesters, section 7). It needs `bin/foc` (built by the target).

## 1. Install

### Prerequisites

- Kubernetes 1.30 or newer (ValidatingAdmissionPolicy v1).
- Swarmada v0.4 running in the cluster, **with its admission webhooks served** (section 6).
  Without Swarmada's `FleetTask` webhook the copilot refuses every draft with
  `DENY_DRAFT_GATE_ABSENT` (docs/DESIGN.md §3.2, D22).
- The image `fleet-ops-copilot:<tag>` available to the nodes. Until the Swarmada module is pinned,
  it is built from binaries compiled on the host (`make image`, docs/DESIGN.md D2).
- Helm 3.8 or newer.

### Check the cluster first

```bash
KIND_CLUSTER=foc-<name> KUBECONFIG=$KUBECONFIG scripts/foc-up-preflight.sh
```

The preflight refuses unless the context is `kind-foc-*`, the kubeconfig file holds only `foc-*`
contexts, and Swarmada's webhooks are installed, have a CA bundle, have ready endpoints, and answer
a server-side dry run of a held draft.

### Install the chart

```bash
make image IMG=fleet-ops-copilot:dev
kind load docker-image fleet-ops-copilot:dev --name foc-<name>
helm --kube-context $CTX install foc charts/fleet-ops-copilot \
  --namespace foc-system --create-namespace
kubectl --context $CTX -n foc-system rollout status deploy/foc-mcp deploy/foc-approvals --timeout=180s
# foc-agent starts once the API key Secret exists (section 2):
kubectl --context $CTX -n foc-system rollout status deploy/foc-agent --timeout=180s
```

The release namespace must be `foc-system`: the admission policies name the copilot's
ServiceAccounts in that namespace, and the chart refuses any other.

Values worth setting (see `charts/fleet-ops-copilot/values.yaml`):

| Value | Default | Purpose |
|---|---|---|
| `swarmadaNamespaces` | `[warehouse-a]` | Namespaces where foc-mcp and foc-approvals get RoleBindings. Every namespace in the policy file must be listed. |
| `policy.content` | chart copy of `policies/default.yaml` | Site policy file (`--set-file policy.content=<file>`) |
| `image.repository`, `image.tag` | `fleet-ops-copilot`, `dev` | Image |
| `llm.cloud.apiKeySecretRef` | `fleet-ops-copilot-claude` / `ANTHROPIC_API_KEY` | Existing Secret holding the Claude API key (section 2) |
| `agent.enabled` | `true` | foc-agent Deployment and Service (model loop and web UI) |
| `tracing.otlpEndpoint` | empty (off) | OTLP/gRPC collector for foc-agent and foc-mcp traces |
| `tracing.backend.enabled` | `false` | Development trace backend (OpenTelemetry Collector + Jaeger) and its wiring; exclusive with `tracing.otlpEndpoint` (section 8) |
| `networkPolicy.enabled` | `false` | Cloud-profile NetworkPolicy; needs `networkPolicy.apiServer.cidrs` |

A cluster on which `make deploy-kind` already installed the plain manifests has the same objects
without Helm's ownership metadata; `helm install` refuses to take them over. Use a fresh cluster,
or remove the objects `config/rbac`, `config/admission` and `config/deploy` created first
(keep the CRDs: deleting a CRD deletes every ApprovalRequest and ApprovalDecision).

### Open the web UI

foc-agent serves a chat and a list of ApprovalRequests. It cannot approve: each pending request
shows the exact object or patch it leads to and the `foc approve` / `foc reject` command to run
with your own kubeconfig. The page has no login, so the name typed in it is recorded as the
requester `unverified:<name>`, and foc-approvals refuses to approve any such request
(`DENY_REQUESTER_UNVERIFIED`, R38): a request filed from the page can be rejected or left to
expire, never approved. The Service is ClusterIP only and the chart renders no Ingress
(`make chart-parity` refuses one); reach the page with a port-forward:

```bash
kubectl --context $CTX -n foc-system port-forward svc/foc-agent 8090:8090
# then open http://127.0.0.1:8090/
```

### Who is recorded as the requester (R39)

foc-mcp records as `requestedFor` the username of the caller's own Kubernetes bearer token, which
it verifies with a TokenReview (its ServiceAccount holds `create` on `tokenreviews`). A name
the client asserts is never trusted. Without a token, or when the API server does not accept it,
the requester is `unverified:<name>` and the request can be rejected but never approved.

To file a request that can be approved, chat from the command line with your own kubeconfig, through
the same port-forward. `foc chat` sends your user's bearer token (static token, token file, or an
exec plugin that returns one) to foc-agent, which forwards it to foc-mcp:

```bash
foc chat --context $CTX "put aisle B into maintenance after this wave"
```

A kubeconfig user that authenticates with a client certificate (kind's default admin) has no bearer
token, so its requests are unverified; `foc chat` says so. On a `make foc-up` cluster use a
person's token kubeconfig: `KUBECONFIG=$FOC_UP_DIR/persons/sam.kubeconfig foc chat --context
sam@foc-up "..."` (section 7). `foc chat` sends the token only over https or
to a loopback address. foc-mcp never stores, logs or traces the token; it keeps a SHA-256 of it for 30
seconds as a cache key. Between foc-agent and foc-mcp the token travels in the cluster over plain HTTP.

### Grant approvers

The chart binds `fleetops:approver` to nobody. Bind it to each person who decides requests, in
each Swarmada namespace:

```bash
kubectl --context $CTX -n warehouse-a create rolebinding approver-<person> \
  --clusterrole=fleetops:approver --user=<person>
```

Approving also requires the Swarmada permission for the underlying change (for example
`swarmada:fleet-manager`); foc-approvals checks it with a SubjectAccessReview and refuses the
decision otherwise. Approvers must be people: a ServiceAccount's decision is refused at admission.

Activating an approved draft needs Swarmada's `activate` verb on `fleettasks` (and `patch`); any
person who holds it may run `foc activate`, not only the approver (R69). Reading the fleet for its
checks needs `get`/`list` on the Swarmada kinds; a read the person lacks is reported as not checked.

### Upgrade and policy changes

```bash
helm --kube-context $CTX upgrade foc charts/fleet-ops-copilot -n foc-system \
  --reuse-values --set-file policy.content=<policy.yaml>
```

A change of policy text changes the `checksum/policy` annotation, so both Deployments restart.

The site catalogue (section 9) is changed the same way, with `--set-file catalogue.content=<catalogue.yaml>`;
restart foc-agent afterwards (`kubectl --context $CTX -n foc-system rollout restart deploy/foc-agent`),
which refuses to start on a catalogue that does not validate. The catalogue is part of the eval
surface: run `make ci` with the new file in `catalogue/` before deploying it.
Helm does not upgrade CRDs in `crds/`; after a CRD change, apply them yourself:
`kubectl --context $CTX apply -f charts/fleet-ops-copilot/crds/`.

### Uninstall

`helm --kube-context $CTX uninstall foc -n foc-system` removes the workloads, RBAC and admission
policies. It leaves the CRDs and every ApprovalRequest and ApprovalDecision. The audit files are on
`emptyDir` volumes and are lost with the pods: copy them first (section 4).

## 2. Rotate the API key (cloud profile)

The key lives only in a Kubernetes Secret that the operator creates; the chart holds a reference
(`llm.cloud.apiKeySecretRef`), never the value. foc-agent receives it as the `ANTHROPIC_API_KEY`
environment variable and does not start without it. The model evaluations read
`ANTHROPIC_API_KEY` from the shell environment instead.

Create or replace the Secret without putting the key on a command line or in shell history:

```bash
# key in a file readable only by you, for example created with: umask 077; pbpaste > key.txt
kubectl --context $CTX -n foc-system create secret generic fleet-ops-copilot-claude \
  --from-file=ANTHROPIC_API_KEY=key.txt --dry-run=client -o yaml \
  | kubectl --context $CTX apply -f -
rm key.txt
```

Rotation order:

1. Create the new key in the Claude Console.
2. Replace the Secret as above.
3. Restart the process that reads it. Environment variables are read at start, so a running pod
   keeps the old key:
   `kubectl --context $CTX -n foc-system rollout restart deploy/foc-agent`.
4. Confirm a request succeeds, then revoke the old key in the Console.

foc-mcp and foc-approvals pass audit lines through `internal/redact`, which masks the values of
known secret environment variables and token patterns such as Anthropic API keys and bearer tokens
(docs/DESIGN.md §3.8). If a key appears in a log, audit line or Event
anyway, revoke it.

## 3. Switch between cloud and air-gapped

| Profile | Model | Status on main |
|---|---|---|
| `cloud` | Claude via the Claude API | Chart value `llm.profile: cloud` (default). The chart runs foc-agent with `-backend=claude` (`internal/llm/claude.go`); it starts once the API key Secret exists (section 2). foc-agent has not yet run against the model on a cluster (docs/KNOWN-LIMITS.md); Sonnet 5.5 and Opus 5.5 were measured through `foc-eval` (`evals/results/comparison-2026-11-05.md`). |
| `airgap` | Qwen2.5-7B-Instruct served by `llama-server` in the cluster | Plain manifests and make targets (`config/airgap/`, `hack/airgap.mk`) and the `llamacpp` backend (`internal/llm/llamacpp.go`); not templated in the chart. Run on single-node kind clusters with kindnet (`docs/evidence/airgap-2026-11-04/`, `docs/evidence/airgap-2026-11-05/`); the `llamacpp` backend ran one evaluation against Qwen2.5-7B-Instruct through `foc-eval` (`evals/results/airgap-llamacpp-qwen2.5-7b-instruct-2026-11-05/`). foc-agent does not run with it yet (docs/KNOWN-LIMITS.md). `llm.profile: airgap` makes the chart render fail on purpose. |

Until the air-gapped profile is in the chart, it is installed beside the chart with these targets,
in this order:

```bash
make load-llama-image KIND_CLUSTER=foc-<name>
make load-llama-model KIND_CLUSTER=foc-<name> MODEL_DIR=<dir> MODEL_SUMS=<sha256sum file>
make deploy-airgap    KIND_CLUSTER=foc-<name>
make verify-egress-baseline KIND_CLUSTER=foc-<name>   # must report open egress
make verify-egress    KIND_CLUSTER=foc-<name>         # must report every external target blocked
```

`make verify-egress` prints an `at-start` line (not counted), the time the policy came into force
for the probe pod, and the `after-wait` line that decides. On kind's default CNI the `at-start`
line reports open egress: a new pod's egress was open for up to about 1.2 s after its container
started (docs/KNOWN-LIMITS.md, D63). If a pod must not have that window, use a CNI whose own
documentation says it enforces NetworkPolicy before the pod's containers start, and run the probe
there.

Do not enable the chart's cloud NetworkPolicy and the air-gapped egress policy together without
reading both: NetworkPolicies are additive, so the cloud policy's Claude API rule would reopen an
egress path the air-gapped profile is meant to close.

Switching back to cloud: remove the air-gapped objects (`config/airgap`), set `llm.profile: cloud`,
and make sure the API key Secret exists (section 2).

## 4. Read the audit log

Each process writes its own append-only JSONL file; every line carries the SHA-256 of the previous
one. The files are on `emptyDir` volumes and are lost when a pod is replaced
(docs/KNOWN-LIMITS.md, "Audit storage"): copy them before a restart, upgrade or uninstall.

```bash
for p in foc-mcp foc-approvals; do
  kubectl --context $CTX -n foc-system exec deploy/$p -- cat /var/lib/fleetops/audit/$p.jsonl > $p-audit.jsonl
done
make build
bin/foc audit verify foc-approvals-audit.jsonl   # "<file>: N entries, chain intact" or the first bad entry
```

Each line has `seq`, `time`, `kind` (for example `approved`, `executed`, `activation_observed`),
`actor`, `sessionID`, `namespace`, `object` (`ApprovalRequest/<name>`), `code` (policy reason codes
such as `DENY_DRAFT_GATE_ABSENT`), `message`, `detail`, `prev`, `hash`.

The same history is visible as Events on each ApprovalRequest and in the requests themselves:

```bash
bin/foc requests -n warehouse-a --context $CTX
kubectl --context $CTX -n warehouse-a describe approvalrequest <name>
kubectl --context $CTX -n warehouse-a get events --field-selector involvedObject.kind=ApprovalRequest
```

A zone stop the executor triggered is attributed in Swarmada's own safety audit log to
`system:serviceaccount:foc-system:foc-approvals`; the approving person is recorded only in the
copilot's request, Events and audit file (docs/DESIGN.md D33).

## 5. When the agent is wrong

The copilot changes nothing without a named person's approval, and it never clears a stop,
releases a robot, deletes, approves, or activates. Every correction below is made by a person with
their own Swarmada permissions.

| Situation | What to do |
|---|---|
| Wrong explanation | Check the cited resource and field directly, for example `kubectl --context $CTX -n warehouse-a get robot <name> -o yaml`. Every claim should name a resource and field; a claim that does not is a defect. |
| Wrong request, still `Pending` | `bin/foc reject <request> -n warehouse-a --context $CTX -comment "<why>"`. A request nobody decides expires after the policy TTL (default 30m). |
| Wrong refusal | The answer and the audit line give the reason code. A site rule (namespaces, priorities, modes, limits) is changed in the policy file and rolled out with `helm upgrade` (section 1). The forbidden operations are compiled in and cannot be enabled. |
| Draft approved by mistake | It is held (`spec.draft: true`); nothing runs until someone activates it. Do not run the activation command. A person with the permission deletes it: `kubectl --context $CTX -n warehouse-a delete fleettask <name>`. |
| Maintenance window approved by mistake | Robots in scope are paused. A person ends the window by setting its `spec.desiredState: Completed` or deleting it; both resume the robots. |
| Zone stop approved by mistake | Clearing is admin-only in Swarmada: `swarmctl estop estop-clear <zone> --reason "<text>"`, then `swarmctl estop resume <robot>...` for robots left in `Resuming`. |
| A real emergency | Use the site's physical E-stops or `swarmctl estop trigger <zone> --reason "<text>"`. `request_zone_stop` waits for an approval and is never the fast path. |
| `DENY_DRAFT_GATE_ABSENT` on every draft | Swarmada's FleetTask webhook is not served. See section 6. |

Afterwards: copy both audit files (section 4) and keep the operator's question, the answer, and
the request names together, so the failure can become an evaluation scenario.

## 6. Enable Swarmada's admission webhooks

Swarmada's quickstart deploys its `config/overlays/quickstart-mtls` overlay, which keeps
`ENABLE_WEBHOOKS=false` and installs no webhook configuration. On such a cluster drafts are not an
authorization boundary, so the copilot refuses them (docs/DESIGN.md D22, D32). Swarmada's own
`config/controlstream-tls` overlay (what its `make deploy` applies) turns them on with a
cert-manager serving certificate. This is how the live approval-path test was run (D32).

Requires cert-manager in the cluster (the quickstart's live path installs it). From the Swarmada
module directory:

```bash
cd "$(go list -m -f '{{.Dir}}' github.com/swarmada/swarmada)"
kubectl --context $CTX apply -k config/controlstream-tls/ --server-side --force-conflicts
kubectl --context $CTX -n system rollout status deploy/swarmada-controller-manager --timeout=300s
```

Check:

```bash
kubectl --context $CTX get validatingwebhookconfigurations,mutatingwebhookconfigurations
KIND_CLUSTER=foc-<name> KUBECONFIG=$KUBECONFIG scripts/foc-up-preflight.sh
```

The preflight requires the `fleettasks` (fail-closed, covering `UPDATE`), `fleetzones` and
`zonemaintenances` validating webhooks and the `fleetzones` mutating webhook, a CA bundle on each,
ready endpoints behind their Service, and an answer from the FleetTask webhook to a dry run.

This overlay also switches Swarmada's ControlStream to mTLS; adapters then need a client
certificate (Swarmada's `docs/adapters.md`). Running it on a shared or production cluster is a
Swarmada operation, not a copilot one: follow Swarmada's documentation there.

## 7. People and bearer tokens: kind and OIDC

A request is approvable only if its requester is verified: the person's own bearer token, checked
with a TokenReview (docs/DESIGN.md D57). A person who authenticates with a client certificate
(kind's admin, `hack/kind-person.sh`) has no bearer token and files `unverified:<name>` requests
that can only be rejected. Two ways to give people tokens:

### On kind: a static token file (test only, R43)

`make foc-up` makes the fictional people **sam** (group `supervisors`; `fleetops:approver`,
`swarmada:operator`), **dana** (`fleet-ops`; `fleetops:approver`, `swarmada:fleet-manager`) and
**eve** (`auditors`; `fleetops:approver`), the same as envtest's. `hack/kind-people.sh gen` writes
`$FOC_UP_DIR/people/tokens.csv` (mode 600, a new random token per cluster; `$FOC_UP_DIR` is
outside the repository, by default `~/.local/state/fleet-ops-copilot/foc-up`, and `kind-people.sh gen`
refuses a directory inside it, so the tokens cannot be committed by accident), the kind
configuration mounts it into the node and gives it to the API server (`--token-auth-file`), and
`hack/kind-people.sh kubeconfig` writes `$FOC_UP_DIR/persons/<name>.kubeconfig`, context
`<name>@foc-up`, holding the person's token. The token is never printed or put on a command line.

```bash
KUBECONFIG=$FOC_UP_DIR/persons/sam.kubeconfig kubectl --context sam@foc-up auth whoami
KUBECONFIG=$FOC_UP_DIR/persons/sam.kubeconfig bin/foc chat --context sam@foc-up "put aisle B into maintenance"
KUBECONFIG=$FOC_UP_DIR/persons/dana.kubeconfig bin/foc approve <request> -n warehouse-a --context dana@foc-up
```

This is not an identity system: a static token never expires, cannot be revoked without
restarting the API server, and is readable by anyone who can read the file or the node. Use it on
a local kind cluster with fictional people, nowhere else. To add a person, edit the table in
`hack/kind-people.sh` and `internal/testenv` together (a test compares them) and recreate the
cluster.

### In production: OIDC

Not set up or run by anything in this repository. The shape, for a cluster whose API server you
configure (managed clusters have their own OIDC integration; follow the provider's documentation):

1. Point the API server at the identity provider: `--oidc-issuer-url`, `--oidc-client-id`,
   `--oidc-username-claim` (for example `email`), `--oidc-groups-claim`, and prefixes
   (`--oidc-username-prefix=oidc:` keeps provider names from colliding with cluster users). On
   newer Kubernetes versions the same is an `AuthenticationConfiguration` file; use the one your
   version documents.
2. Bind roles to the prefixed names, for example
   `kubectl -n warehouse-a create rolebinding approver-alice --clusterrole=fleetops:approver --user=oidc:alice@example.test`.
   The username the API server reports is what the copilot records as `requestedFor` and
   `decidedBy`; self-approval is refused by comparing them.
3. People log in with an exec credential plugin in their kubeconfig (for example `kubelogin`),
   which returns the ID token. `foc chat` sends the token an exec plugin returns, only over https
   or to a loopback address; `foc approve` and `kubectl` use the same kubeconfig user.
4. foc-mcp verifies the token with a TokenReview with the API server's default audiences. That has
   been tested with envtest's static tokens and TokenRequest tokens only, **not with an OIDC
   token** (docs/KNOWN-LIMITS.md). Try a request as a person and check `spec.requestedFor` on the
   ApprovalRequest before relying on it.
5. ID tokens are short-lived. A long `foc chat` session may need a fresh token; foc-mcp caches a
   review for 30 seconds by the token's SHA-256 and never stores the token.

## 8. Read the traces (optional backend)

foc-agent and foc-mcp export one trace per operator request when they have an OTLP/gRPC endpoint:
spans `foc.request`, `llm.chat` (tokens, latency), `tool.call`, `mcp.tool` and `policy.decide`,
joined across MCP by W3C trace context (D54). No prompt, answer, tool argument or result text is in
a span, and a redacting exporter masks configured secrets and token patterns before export.

Use your own collector with `--set tracing.otlpEndpoint=<host:port>`, or install the chart's
development backend, an OpenTelemetry Collector in front of Jaeger v2 (in-memory store, off by
default, D61):

```bash
helm --kube-context $CTX upgrade --install foc charts/fleet-ops-copilot -n foc-system \
  --reuse-values --set tracing.backend.enabled=true      # leave tracing.otlpEndpoint empty
kubectl --context $CTX -n foc-system rollout status deploy/foc-otel-collector deploy/foc-jaeger
kubectl --context $CTX -n foc-system port-forward svc/foc-jaeger 16686:16686
# open http://127.0.0.1:16686/ , service foc-agent, operation foc.request
```

Both Services are ClusterIP and the UI has no login: reach it only through the port-forward.
Traces live in the Jaeger pod's memory (the image's default of 100000 traces) and are lost when it
restarts. The images (`otel/opentelemetry-collector`, `jaegertracing/jaeger`, pinned by digest in
`values.yaml`) must be pullable by the nodes; they are not part of the air-gapped profile. With
`networkPolicy.enabled` the chart opens exactly foc-mcp/foc-agent to the Collector and the
Collector to Jaeger. With the backend on, the Collector's own egress policy (Jaeger's OTLP port and
cluster DNS only) is rendered also without `networkPolicy.enabled`, so the Collector can reach
Jaeger beside the air-gapped policy of `config/airgap` (D68). foc-mcp/foc-agent → Collector is
opened beside `config/airgap` by `config/airgap/networkpolicy-tracing.yaml`, which
`make deploy-airgap` applies when `deploy/foc-otel-collector` exists (install the chart with the
backend first), and in the chart only with `networkPolicy.enabled` (D69). Both rules are
template-verified only, until the next cluster run. The backend ran on a `make foc-up` cluster
on 2026-11-05 (`docs/evidence/airgap-2026-11-05/`), before D68 and D69.

## 9. Requests for work: questions, preview, confirmation, activation

Not run on a cluster yet: tested with the scripted backend, envtest and the fixture evaluations
(FOC-8).

Work is asked for in a conversation (docs/DESIGN.md §3.10). The copilot asks at most two short
questions, shows a preview, and files an `ApprovalRequest` only after the person replies `1`
(confirm) or `2` (confirm, and prepare a follow-up request if the task fails). `3 <change>` changes
the request and shows a new preview; `4` cancels. Nothing is filed without that reply: foc-mcp
refuses a request whose confirmed preview is missing or differs (`DENY_NOT_CONFIRMED`,
`DENY_PREVIEW_MISMATCH`).

**Conversations.** `foc chat` keeps the conversation per kubeconfig context and foc-agent URL in
`$XDG_STATE_HOME/foc/conversations/` (default `~/.local/state/foc/conversations/`, mode 600);
`foc chat --new` starts another. The web page keeps it in the page's memory; reload starts a new
one. foc-agent holds conversations in memory for 30 minutes after the last message, bound to the
requester foc-mcp verified: another person cannot continue one, even with its id. A foc-agent
restart loses them; the chart runs one foc-agent replica and refuses more.

**The preview's checks** (`check_task`, reads only): an eligible robot now under Swarmada's
candidate filters that a read can evaluate, zone state (stops, maintenance, restrictions), the
waypoint, dependencies, timing and the policy. The verdict is "likely feasible" or "blocked", never
a guarantee: the adapter's own check of the concrete action is not visible before assignment.
A blocked preview names each blocker with an alternative and cannot be confirmed. Common blockers:

| Code | Meaning | What to do |
|---|---|---|
| `NO_ELIGIBLE_ROBOT` | no robot passes the candidate filters now (the reason names each filter and robot) | wait, free a robot, or change the request |
| `ROBOTS_BUSY` (warning) | the only candidates are busy now | none: the task waits for them after activation |
| `CHARGE_BELOW_BATTERY_FLOOR` | the robot is below its `minBatteryPctForAction`; Swarmada gives it no new action, a Charge included | charge it by hand or with its own app, or a person lowers the floor |
| `NO_CARRYING_ROBOT` | no robot declares the carrying capability a delivery needs | move a robot there instead (no load) |
| `ZONE_STOPPED`, `NAMESPACE_STOPPED` | a stop is in force; Swarmada holds work there | a person clears the stop; the copilot cannot |
| `WAYPOINT_NOT_FOUND`, `ZONE_NOT_FOUND` | the place does not exist | the preview lists what exists |

**At execution.** Immediately before creating an approved draft, foc-approvals re-runs the checks
and dry-runs the FleetTask and each member as a FleetAction through Swarmada's admission. If
anything fails the request ends `Failed` with `DENY_CHECK_FAILED` or `DENY_SWARMADA_REFUSED` and the
reason, and nothing is written; `foc requests` and the web UI show it. `status.executionChecks`
holds what was checked.

**Activation.** A draft runs only after a separate activation request is approved and a person
runs it with their own kubeconfig:

```bash
KUBECONFIG=<your kubeconfig> foc activate <activation-request> -n warehouse-a [--context <ctx>]
```

`foc activate` refuses a ServiceAccount, a request that is not `Approved`, a task that is no longer
a draft and a blocked check. Then it records the activation as an `ApprovalDecision` of type
`Activation` naming you (the same admission policy as an approval admits only your own name), and
sends the plain `spec.draft=false` patch; nothing is added to the FleetTask. Swarmada checks your
`activate` verb. Recording needs `create` on `approvaldecisions` (the `fleetops:approver` role).
foc-approvals links the record to the request when the task leaves Draft (`status.activatedBy`,
`status.activationRef`); an activation made with `kubectl patch` is recorded with the activator
unknown.

**Outcome and follow-up.** foc-approvals records the task's terminal phase on the DraftTask request
(`status.outcome`). With choice 2 at the preview, a failed task also gets `status.followUp`, a
proposal only: `foc chat --follow-up <request>` opens it at a preview, and it is filed only after a
new confirmation.

**The site catalogue** (`catalogue/warehouse-a.yaml`, chart `files/site-catalogue.yaml`) lists the
kinds of work, their Swarmada mapping, defaults and the questions that lead to each; kinds Swarmada
cannot report a result for say so in the preview (`resultReported: false`). "Something else"
answers are written to foc-agent's audit file as `catalogue_review` entries, for deciding what to
add: read them with `kubectl --context $CTX -n foc-system exec deploy/foc-agent -- cat /var/lib/fleetops/audit/foc-agent.jsonl`.
The file is on an emptyDir volume and is lost with the pod.

