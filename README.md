# Fleet Ops Copilot

An operations assistant for robot fleets orchestrated by [Swarmada](https://github.com/swarmada/swarmada).
It reads fleet state through the Kubernetes API, drafts work, and turns every change into a request that a
named person approves; it never clears a stop, releases a robot, deletes, approves its own request, or
edits work that already exists.

<!-- Demo video: a link or an embedded clip goes here. -->

Every number below links to the committed file or CI run it comes from. The design is in
[`docs/DESIGN.md`](docs/DESIGN.md); what is not built, not measured, or not enforced is in
[`docs/KNOWN-LIMITS.md`](docs/KNOWN-LIMITS.md). It has run on kind clusters on one machine and is not
deployed at any site.

[How to run](#how-to-run) · [Why it exists](#why-it-exists) · [Architecture](#architecture) ·
[Measured results](#measured-results) · [Known limits](#known-limits) ·
[Repository](#what-is-in-the-repository)

## How to run

### Start

`make demo` runs the whole stack in a kind cluster on one machine. By default it is the
**air-gapped profile**: the model, Qwen2.5-7B-Instruct, runs in the cluster on llama-server, the
copilot's namespace has no egress beyond the cluster, and no API key is needed or used. The cloud
model, Claude Sonnet 5.5 through the Claude API, is the second path below and needs a key.

The two differ in how often the answer is right. On the v6 record (50 scenarios × 3 runs, fixture
mode), the local Qwen2.5-7B-Instruct passed **26/150**
([results.json](evals/results/airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v6/results.json)) and
Claude Sonnet 5.5 **120/150**
([results.json](evals/results/cloud-claude-sonnet-5-5-2026-11-08-v6/results.json)); both are compared
in [`comparison-v6.md`](evals/results/comparison-v6.md). The safety design holds on either: the model
is offered no write tool, every change waits for the asker's confirmation and a second person's
approval, and in the 600 v6 runs the model changed 0 Swarmada objects
([`comparison-v6.md`](evals/results/comparison-v6.md), "Containment"; and
[above](#air-gapped-what-a-small-local-model-does-and-what-it-cannot-do)).

Requires Docker (running), kind, kubectl, helm 3.8+, jq, openssl, curl, make, Go 1.25+ and
python3 with the `grpcio` package. For the air-gapped profile, Docker's VM needs at least
**12 GiB of memory** (Docker Desktop: Settings, Resources, Memory); `make demo` checks this before it
creates anything and stops with the number it found. Swarmada's quickstart runs a simulated robot
adapter in Python with the first `python3` on `PATH`; `make demo` checks that interpreter too and
stops with the fix if it cannot import `grpc` and `google.protobuf`. Put a virtual environment that
has them first on `PATH`, outside the repository:

```bash
python3 -m venv ~/fleetops-venv && ~/fleetops-venv/bin/pip install grpcio protobuf
export PATH="$HOME/fleetops-venv/bin:$PATH"
```

Full list: [`docs/RUNBOOK.md`](docs/RUNBOOK.md), section 0.

Run every command below from the top directory of a clone of this repository, in a terminal
(the examples were run in zsh, the macOS default). Build the command-line client first:

```bash
make build
```

**Air-gapped, no key.** Start the demo:

```bash
make demo
```

The first run downloads the model files before it creates the cluster: two files, 4,683,073,632
bytes (4.4 GiB) together, from the official Qwen repository on Hugging Face at a fixed revision,
into `~/.cache/fleet-ops-copilot/models`. Each is checked against its sha256 in
[`config/airgap/model-SHA256SUMS`](config/airgap/model-SHA256SUMS), the files of the v6 record runs. It
also pulls the pinned llama.cpp server image, the one the record used. Later runs check the cached
files and reuse them; nothing is downloaded inside the cluster. On a Mac mini the first run took
6m45s, 3m37s of it for the model files, and a run with them cached 3m18s
([`docs/evidence/demo-airgap-2026-11-13/`](docs/evidence/demo-airgap-2026-11-13/README.md)). The last
line `make demo` prints is the total time, followed by what to run next.

**Cloud, with a Claude API key.** Instead of the command above, put the key in a file only you can
read; `make demo` creates the Secret from it, and the key never appears on a command line. Copy the
key from the Claude Console, then run:

```bash
(umask 077; pbpaste > ~/fleetops-key.txt)
```

Check the file right away. The check says whether it holds a key alone (one line, starting with
`sk-ant-`, no spaces) and never prints it. If it says `not a Claude API key`, the clipboard held
something else: copy the key again and repeat both commands.

```bash
hack/check-key-file.sh ~/fleetops-key.txt
```

Start the demo with the cloud model. It runs the same check first and starts nothing if the file
fails it. On a Mac mini it took 2m47s (above):

```bash
make demo DEMO_MODEL=cloud DEMO_ANTHROPIC_KEY_FILE=$HOME/fleetops-key.txt
```

Once it has finished, the key is in the Secret; delete the file:

```bash
rm ~/fleetops-key.txt
```

`make demo` creates the kind cluster `fleetops-demo`, runs Swarmada's quickstart at the commit
`go.mod` pins, turns on Swarmada's admission webhooks, keeps the simulated fleet's three robots
live (one sim adapter per robot, in the background; `make demo-down` stops them), builds the image
and installs the chart into `fleetops-system`, and creates three fictional people with test-only
tokens: **sam**, **dana** and **eve**. In the air-gapped profile it then loads the llama.cpp image
and the model files into the cluster's node and applies llama-server and the egress NetworkPolicies
from [`config/airgap/`](config/airgap/): the copilot's pods may reach only the Kubernetes API, cluster
DNS, llama-server and fleetops-mcp, and llama-server reaches nothing. The machine itself is not
air-gapped: Swarmada's quickstart builds and pulls its own images. Everything `make demo` keeps is in
`~/.local/state/fleet-ops-copilot/demo` (`$DEMO_DIR`), outside the repository, including its own
kubeconfig: it never reads or writes `~/.kube/config`. To change the profile, run `make demo-down`
first; `make demo` does not switch a cluster that is running.

Because of that, `kubectl` finds the demo cluster only after you point `KUBECONFIG` at the demo's
kubeconfig. Do it in this terminal, and again in every new terminal you open for the demo. Then
check that the workloads, fleetops-agent, fleetops-approvals, fleetops-mcp and, in the air-gapped
profile, llama-server, are each `1/1 Running`:

```bash
export KUBECONFIG=$HOME/.local/state/fleet-ops-copilot/demo/kubeconfig
kubectl --context kind-fleetops-demo -n fleetops-system get pods
```

### Ask

Both ways of asking reach fleetops-agent through a port-forward. Open a second terminal (in
Terminal, Cmd-N), go to the repository directory, and run these two lines there. The
port-forward stays in the foreground: leave that terminal open while you use the demo.

```bash
export KUBECONFIG=$HOME/.local/state/fleet-ops-copilot/demo/kubeconfig
kubectl --context kind-fleetops-demo -n fleetops-system port-forward svc/fleetops-agent 8090:8090
```

If it stops with `error: lost connection to pod`, fleetops-agent's pod was replaced; run the
`kubectl` line again in the same terminal.

**Verified: ask with `bin/fleetops chat` as sam.** Back in the first terminal, ask as a person,
with that person's own kubeconfig. The copilot records who asked from the person's verified token,
never from a typed name (RUNBOOK section 7), so a request sam files this way can be approved by
dana:

```bash
P=$HOME/.local/state/fleet-ops-copilot/demo/persons
KUBECONFIG=$P/sam.kubeconfig bin/fleetops chat --context sam@fleetops-demo "Show me all robots"
```

**Web chat: its requests can only be rejected.** With the port-forward running, open
http://127.0.0.1:8090/ in a browser. "Your name" at the top is a separate field from the message
box at the bottom: replace any text in it with `sam`, then type the question in the message box and
send it. The page has no login, so it cannot tell who you are: a request filed from it is recorded
for `unverified:sam` (`unverified:<name>` for any name), fleetops-approvals refuses to approve it
(`DENY_REQUESTER_UNVERIFIED`), and a person can only reject it. For a request that can be approved,
use `bin/fleetops chat` as above. The page also lists pending requests with the exact
`fleetops approve` / `fleetops reject` command to run; it cannot approve (RUNBOOK section 1, "Open
the web UI").

Three kinds of question show what the copilot does:

| Ask | What happens |
|---|---|
| A question about the fleet, for example the one above | It calls read-only tools and answers with the objects and fields it read. |
| Work, for example `"Inspect the receiving dock."` or `"Pause zone warehouse-a for maintenance, graceful."` | It asks at most two short questions, shows a preview with its checks, and files an approval request only after you reply `1` (below). It changes nothing itself, and the person who asked cannot approve it. |
| A forbidden action, for example `"Clear the emergency stop on warehouse-a."` | It refuses and gives the reason. Clearing stops, releasing robots, deleting, approving and editing running work are never available. |

Asking for work is a short conversation. `fleetops chat` keeps the conversation for the context until
`fleetops chat --new`, so answer with the number of a choice. Every question and preview has a
"Checked:" block: what fleetops-agent read itself, one object per line, never the model's words
(D92). A question's block is the fleet state read for it; a preview's is what `check_task` read,
with each robot it considered and why that robot is eligible now or not:

```text
$ fleetops chat ... "Inspect staging-shelf-a."
Checked (fleet state read now; eligibility is checked for the preview):
  FleetZone/warehouse-a: status.estopStatus=Clear, status.robotCount=3.
  Robot/sim-robot-001: status.phase=Idle, status.batteryPercent=99, status.currentZone=warehouse-a.
  ...
What should the robot inspect?
1 Damage
2 Location
3 Both
4 Something else — describe it
$ fleetops chat ... 1
Preview — Inspect for damage (catalogue kind inspect-damage)
  Location: staging-shelf-a (FleetZone warehouse-a, x 12.1, y 8.1)
  ...
  Swarmada records arrival and completion, not what the robot found; a person reviews the result on the robot's side.
Checks: likely feasible. Eligible now: inspect: sim-robot-001, sim-robot-003.
Checked:
  FleetZone/warehouse-a: status.estopStatus=Clear, status.robotCount=3, no ZoneMaintenance Active or Scheduled over it.
  Robot/sim-robot-001: status.phase=Idle, status.batteryPercent=99, status.currentZone=warehouse-a, camera_front=Active, navigation=Active; eligible now.
  Robot/sim-robot-002: status.phase=InProgress, status.batteryPercent=99, status.currentZone=warehouse-a, camera_front=Active, navigation=Active; not eligible now: filter 1 (not Idle).
  Robot/sim-robot-003: status.phase=Idle, status.batteryPercent=99, status.currentZone=warehouse-a, camera_front=Active, navigation=Active; eligible now.
Checked against current fleet state; Swarmada's own acceptance is checked at execution.
1 Confirm · 2 Confirm, and prepare a follow-up request if it fails · 3 Change something — describe it · 4 Cancel
$ fleetops chat ... 1
Filed ApprovalRequest/areq-... (inspect-damage). It waits for a named person: ...
```

The filters are Swarmada's candidate filters (RFC-0001 §9.4.2). A value the site policy does not
allow (priority Critical under `policies/default.yaml`) is not offered to the model and is refused
before any question (D93).

The kinds of work the site offers, and the questions that lead to each, are in the site catalogue
([`catalogue/warehouse-a.yaml`](catalogue/warehouse-a.yaml)). "Something else" takes your own
words: the copilot keeps them verbatim, maps them to a known kind or to basic steps for a person
to review, or says it cannot be done and files nothing. A preview with a blocker (no robot can do
it, the zone is stopped, the robot is below its battery floor) explains why and offers an
alternative; it cannot be confirmed.

A request is decided by a different person, with their own kubeconfig:

```bash
KUBECONFIG=$P/dana.kubeconfig bin/fleetops requests -n warehouse-a --context dana@fleetops-demo
KUBECONFIG=$P/dana.kubeconfig bin/fleetops approve <request> -n warehouse-a --context dana@fleetops-demo
KUBECONFIG=$P/dana.kubeconfig bin/fleetops reject  <request> -n warehouse-a --context dana@fleetops-demo -comment "<why>"
```

An approved draft runs once it is activated. Activation is its own request (`"Activate the
draft inspect-damage-..."`); once a person approves it, a person Swarmada allows to activate runs
it with their own kubeconfig. `fleetops activate` checks the fleet again first and refuses if the work
could not run now:

```bash
KUBECONFIG=$P/dana.kubeconfig bin/fleetops activate <activation-request> -n warehouse-a --context dana@fleetops-demo
```

With the air-gapped model nothing leaves the cluster, and answers take longer and are more often
wrong: on the v6 record its latency was 10.23 s at p50 and 124.78 s at p95, against 4.22 s and
7.44 s for Claude Sonnet 5.5 ([`comparison-v6.md`](evals/results/comparison-v6.md)). Nothing is
shown while the model works; "Is it working?" below shows how to see that it is working. With the cloud model each
question is a call to the Claude API; on the v6 record, 100 requests cost 1.5449 USD with Claude
Sonnet 5.5.

### Is it working? Watching and debugging

The chat shows nothing until the answer is complete. With the air-gapped model one answer can take
minutes: 124.78 s at p95 on the v6 record, and 134 s for one `fleetops chat` question on a Mac mini
([`docs/evidence/demo-airgap-2026-11-13/`](docs/evidence/demo-airgap-2026-11-13/README.md)). To see
that it is working, or why it is not, open another terminal in the repository directory and point it
at the demo cluster:

```bash
export KUBECONFIG=$HOME/.local/state/fleet-ops-copilot/demo/kubeconfig
```

Every workload, each `1/1 Running`:

```bash
kubectl --context kind-fleetops-demo -n fleetops-system get pods
```

The model at work (air-gapped profile). While an answer is being made, llama-server logs each request
with its prompt and generation timings; Ctrl-C stops following the log and changes nothing:

```bash
kubectl --context kind-fleetops-demo -n fleetops-system logs -f deploy/llama-server --tail=20
```

The cluster's CPU use. While the model answers it is several hundred percent; near 0% during a wait
means nothing is running:

```bash
docker stats --no-stream fleetops-demo-control-plane
```

What each process did, newest last:

```bash
kubectl --context kind-fleetops-demo -n fleetops-system logs deploy/fleetops-agent --tail=50
kubectl --context kind-fleetops-demo -n fleetops-system logs deploy/fleetops-mcp --tail=50
kubectl --context kind-fleetops-demo -n fleetops-system logs deploy/fleetops-approvals --tail=50
```

The requests filed, in every namespace (`areq` is the short name of ApprovalRequest):

```bash
kubectl --context kind-fleetops-demo get areq -A
```

A pod that is not `1/1 Running`, or restarts: describe it and read the log of its last run, with the
pod name `get pods` printed in place of `POD_NAME`, then the namespace's recent events:

```bash
kubectl --context kind-fleetops-demo -n fleetops-system describe pod POD_NAME
kubectl --context kind-fleetops-demo -n fleetops-system logs POD_NAME --previous
kubectl --context kind-fleetops-demo -n fleetops-system get events --sort-by=.lastTimestamp
```

Each step of one conversation (model calls, tool calls, tokens): start the demo with
`DEMO_TRACING=1` (the node pulls the trace backend's images from the internet), then run this and open
http://127.0.0.1:16686/:

```bash
kubectl --context kind-fleetops-demo -n fleetops-system port-forward svc/fleetops-jaeger 16686:16686
```

**Changing the model.** `make demo` does not switch a running cluster from one profile to the other.
For faster answers from the cloud model, remove the cluster and start it again with the key file made
as in "Start" above:

```bash
make demo-down
make demo DEMO_MODEL=cloud DEMO_ANTHROPIC_KEY_FILE=$HOME/fleetops-key.txt
```

### Finish

Stop the port-forward in the second terminal with Ctrl-C. Then, in the first terminal, remove the
cluster `fleetops-demo`, its kubeconfig, the people's tokens and the conversations `fleetops chat`
kept for them, and stop pointing `kubectl` at the removed kubeconfig:

```bash
make demo-down
unset KUBECONFIG
```

### Connect your own Swarmada

`make demo` bundles Swarmada for a one-command demo. The copilot itself is a separate Helm
chart that talks to Swarmada only through the Kubernetes API, so it can be installed next to a
Swarmada that is already running. It is installed **into the cluster where Swarmada runs**: it
needs that cluster's API server for token checks, RBAC and Swarmada's webhooks.

Requirements for that Swarmada:

- Swarmada **v0.4** (the version `go.mod` pins; other versions are not supported yet).
- Its **admission webhooks served**. Swarmada's quickstart turns them off; without them the copilot
  refuses every draft (`DENY_DRAFT_GATE_ABSENT`). Turning them on is RUNBOOK section 6.
- The namespaces the copilot may work in, for example `warehouse-a`.

**1. Run Swarmada.** For a local try, use Swarmada's warehouse quickstart
(`examples/warehouse-quickstart/run.sh` in the
[Swarmada repository](https://github.com/swarmada/swarmada) at tag `rfc-0001-v0.4`) on a kind
cluster whose name starts with `fleetops-`, for example `fleetops-sw`. The copilot's scripts only act on
`kind-fleetops-*` contexts. For any other cluster, follow Swarmada's own documentation.

**2. Turn on Swarmada's webhooks and check the cluster** (RUNBOOK sections 6 and 1):

```bash
export KUBECONFIG=<kubeconfig with only the fleetops-sw context>
CTX=kind-fleetops-sw
cd "$(go list -m -f '{{.Dir}}' github.com/swarmada/swarmada)"
kubectl --context $CTX apply -k config/controlstream-tls/ --server-side --force-conflicts
kubectl --context $CTX -n system rollout status deploy/swarmada-controller-manager --timeout=300s
cd -
KIND_CLUSTER=fleetops-sw KUBECONFIG=$KUBECONFIG scripts/demo-preflight.sh
```

The preflight's last line must be `PASSED`.

**3. Install the copilot:**

```bash
make image IMG=fleet-ops-copilot:dev
kind load docker-image fleet-ops-copilot:dev --name fleetops-sw
helm --kube-context $CTX install fleetops charts/fleet-ops-copilot \
  --namespace fleetops-system --create-namespace --set 'swarmadaNamespaces={warehouse-a}'
kubectl --context $CTX -n fleetops-system rollout status deploy/fleetops-mcp deploy/fleetops-approvals --timeout=180s
```

The chart's default is the cloud profile (Claude through the Claude API). For the air-gapped model
instead, add `--set llm.profile=airgap` to the `helm` command above: fleetops-agent then runs against
llama-server in the cluster and reads no key. llama-server, its model files and the egress
NetworkPolicies are not in the chart; install them beside it with `hack/fetch-model.sh` and the
`make load-llama-image`, `load-llama-model` and `deploy-airgap` targets (RUNBOOK section 3), and skip
the API key in step 4.

**4. Add the API key and the people.** Create the key Secret from a file (RUNBOOK section 2), then
let people decide requests by binding `fleetops:approver` to them in each Swarmada namespace:

```bash
(umask 077; pbpaste > ~/fleetops-key.txt)
hack/check-key-file.sh ~/fleetops-key.txt
```

Go on only if the check says `looks like a Claude API key`:

```bash
kubectl --context $CTX -n fleetops-system create secret generic fleet-ops-copilot-claude \
  --from-file=ANTHROPIC_API_KEY=$HOME/fleetops-key.txt --dry-run=client -o yaml | kubectl --context $CTX apply -f -
rm ~/fleetops-key.txt
kubectl --context $CTX -n warehouse-a create rolebinding approver-<person> \
  --clusterrole=fleetops:approver --user=<person>
```

People ask with their own kubeconfig, as in section 2 above. A request is approvable only if the
person's kubeconfig carries a bearer token (an OIDC login, for example); a client-certificate user
is recorded as unverified (RUNBOOK section 7).

**5. Remove the copilot** without touching Swarmada:

```bash
helm --kube-context $CTX uninstall fleetops --namespace fleetops-system
```

The copilot's two CRDs stay; deleting them deletes every ApprovalRequest and ApprovalDecision.
The audit files are lost with the pods, so copy them first (RUNBOOK section 4).

### Other commands

| Command | What it does |
|---|---|
| `DEMO_DRY_RUN=1 make demo` | Prints every command that would change anything; runs none. |
| `DEMO_TRACING=1 make demo` | Also installs the development trace backend (RUNBOOK section 8). Its images are pulled by the node, so it is not part of the air-gapped profile. |
| `make verify-egress KIND_CLUSTER=fleetops-demo` | Air-gapped profile: a probe Job in `fleetops-system` that passes only if no external target is reachable (RUNBOOK section 3). |
| `make test-live` | Live approval paths on the demo stack, requests filed with sam's own token. |
| `make ci` | Without a cluster: build, vet, lint, ShellCheck 0.11.0, unit tests, envtest, fixture evals held to `evals/baseline.json`. |
| `make chart-check` | Without a cluster: helm lint, kubeconform, chart parity with `config/`. |

Model evaluations call the Claude API and read `ANTHROPIC_API_KEY` from the environment:

```bash
go run -tags envtest ./evals/cmd/fleetops-eval -backend claude -model claude-sonnet-5-5 -runs 3 \
  -out evals/results/<date>-cloud-claude-sonnet-5-5/results.json
```

## Why it exists

A shift supervisor at a robot site asks "why is sim-robot-002 not taking work?" or "put aisle B into
maintenance after this wave". The answers are in Kubernetes objects (Robot, FleetTask, FleetZone,
ZoneMaintenance and their Events), which a language model can read and explain. Letting the same model
change them is the risk: a wrong or manipulated answer could stop a zone, release a robot or start
work. So every change passes through a named person, and that has to hold when the model is wrong.

The customer in this repository, "Example Distribution Co.", is **fictional**
([`docs/DESIGN.md`](docs/DESIGN.md)). Its robot network has no route to the internet, so a copilot at
the site runs its model in the cluster with egress denied by NetworkPolicy: the air-gapped profile,
`make demo`'s default (D103). Its offsite operations centre can use a hosted model, Claude through the
Claude API: the cloud profile, the Helm chart's default. The agent, tools, policy and approval path are
the same; only the model backend differs. A model small enough for site hardware is weaker, so the
design has to keep a weak model harmless, not only a strong one useful.

## Architecture

![Overview: the operator asks fleetops-agent, which talks to the model and to fleetops-mcp; fleetops-mcp files requests through the Kubernetes API server; an approver decides; fleetops-approvals makes the one write to Swarmada](docs/architecture/overview.svg)

The copilot runs in the Kubernetes cluster where Swarmada runs, in the namespace
`fleetops-system`, as three processes, each with its own ServiceAccount
(`config/rbac/roles.yaml`, `config/admission/policies.yaml`):

| Process | What it does | What its ServiceAccount may do |
|---|---|---|
| `fleetops-agent` | Runs the model loop (Claude through the Claude API, or with `llm.profile=airgap` Qwen2.5-7B-Instruct on llama-server in the cluster), the guided dialogue from the site catalogue, and the web chat. Files a request only after the person confirms its preview. | `get` and `list` ApprovalRequests, for the web UI's requests panel. No write. |
| `fleetops-mcp` | MCP server: read tools, `check_task`, and the request tools. Verifies the caller's bearer token, runs the policy engine, refuses a request without a confirmed preview. | Read Swarmada objects and webhook configurations; create ApprovalRequests, Events and TokenReviews. No Swarmada write. |
| `fleetops-approvals` | Checks each decision, re-checks before executing, makes the one approved write, watches activations and outcomes. | Read Swarmada objects, requests and decisions; create held-draft FleetTasks and ZoneMaintenances; create FleetActions as a dry run only; add the estop annotation to a FleetZone and change nothing else on it; write ApprovalRequest status; create Events and SubjectAccessReviews. |

**A question.** The person asks with `fleetops chat`, which sends their own bearer token, or in
the web chat, which sends none. fleetops-agent gives the model the read tools, `check_task` and
`propose_task`, and forwards the token on every call to fleetops-mcp. fleetops-mcp verifies the
token with a TokenReview, reads Swarmada objects and returns them; nothing is written. The system
prompt tells the model to cite each fact as Kind/name and field.

**A change** is never made by the model:

![Approval flow: a confirmed preview filed as a request, decided by a second person, executed by fleetops-approvals, then activated by a person with fleetops activate](docs/architecture/approval-flow.svg)

1. The person asks for work in plain words. The model reads them into `propose_task`; it is
   offered no request tool (D74).
2. fleetops-agent's dialogue engine walks the site catalogue: at most two numbered questions, then
   a preview with the result of `check_task` (`likely feasible` or `blocked`, from reads only)
   (D71, D73).
3. The person replies `1` (confirm) or `2` (confirm, and prepare a follow-up if it fails).
   fleetops-agent files the stored request with the preview's id; fleetops-mcp verifies the token,
   refuses a request with no confirmation or a changed preview (`DENY_NOT_CONFIRMED`,
   `DENY_PREVIEW_MISMATCH`), runs the policy engine and creates an `ApprovalRequest` naming the
   verified requester.
4. A different person runs `fleetops approve` (or `fleetops reject`) with their own kubeconfig.
   That creates an `ApprovalDecision`, which an admission policy binds to the authenticated user and
   refuses from any ServiceAccount.
5. fleetops-approvals refuses an unverified requester and self-approval, checks with a
   SubjectAccessReview that the approver could make the change directly, and re-runs the policy on
   current state. Before a draft it runs `check_task` again and dry-runs the task through
   Swarmada's admission (D76). Then it makes one write: a held draft task, a maintenance window or
   a zone stop. Swarmada's webhooks check that write as they check any client's.
6. A draft runs only once it is activated, and activation is its own request through steps 1–5.
   Once that request is approved, a person Swarmada allows to activate runs
   `fleetops activate <request>` with their own credentials. It refuses a ServiceAccount, checks
   the fleet again, records an `ApprovalDecision` of type `Activation`, then clears the draft flag.
   Swarmada's webhook checks that person's `activate` verb. fleetops-approvals records who
   activated, then the task's outcome (D79, D78).

Every request, decision, execution and activation is written to the audit files and as Events
([§3.4](docs/design/approvals.md#34-audit)). The air-gapped profile (`config/airgap`) adds llama-server in `fleetops-system`, with no
egress at all; the copilot's pods may reach only cluster DNS, the Kubernetes API, and (for
fleetops-agent) llama-server and fleetops-mcp. Diagram sources:
[`docs/architecture/`](docs/architecture/).

### Approach

**Three processes, three ServiceAccounts** ([`docs/DESIGN.md`](docs/DESIGN.md#three-processes), D5). `fleetops-agent` talks to the
model and holds no Kubernetes write permission. `fleetops-mcp` exposes the tools: it reads Swarmada
and can create `ApprovalRequest` objects, nothing else. `fleetops-approvals` writes to Swarmada, and
only for a request a named person approved: a held draft task, a maintenance window, a zone stop.
The split is enforced by RBAC and ValidatingAdmissionPolicies ([§3.6](docs/design/rbac-and-admission.md#36-kubernetes-rbac-and-admission-policies)), checked by `TestRBACMatrix`
and `TestFleetopsAgentCannotWrite` (`test/envtest`).

**Guided creation: a rule engine asks, the model only reads** ([§3.10](docs/design/guided-requests.md#310-guided-task-requests-d70d81), D70–D79, D92–D94). Work is
asked for in a short conversation driven by a site catalogue
([`catalogue/warehouse-a.yaml`](catalogue/warehouse-a.yaml)):

- The model extracts the person's request into `propose_task` and is offered no write tool (D74).
- A rule engine in fleetops-agent, not the model, asks at most two numbered questions and builds
  the preview, with a fleet check (`check_task`, reads only: an eligible robot now under Swarmada's
  candidate filters, zone state, location, dependencies, timing) (D71, D73).
- Every question and preview ends with a **"Checked:" block**: what fleetops-agent read itself,
  one object per line, never the model's words (D92). A value the site policy does not allow is not
  offered to the model and is refused before any question (D93); a field the kind does not use is
  refused to the model with the kind's fields (D94).
- fleetops-agent files the request only on the person's own confirmation of that preview, and
  fleetops-mcp refuses a request without one (`DENY_NOT_CONFIRMED`, `DENY_PREVIEW_MISMATCH`).

**Approvals by a second person** ([§3.3](docs/design/approvals.md#33-approval-flow)). An approver runs `fleetops approve` with their own
kubeconfig; it creates an `ApprovalDecision` that an admission policy binds to the authenticated user
and refuses from any ServiceAccount. `fleetops-approvals` then refuses self-approval
(`DENY_SELF_APPROVAL`), checks with a SubjectAccessReview that the approver could make the change
directly, re-runs the policy on fresh cluster state, and, before a draft, re-checks it and dry-runs
it through Swarmada's admission (D76), then makes the single write.

- **A person activates.** Swarmada refuses draft activation to every ServiceAccount, so the
  copilot never activates: after approval a person Swarmada allows to activate runs
  `fleetops activate` with their own credentials, and `fleetops-approvals` records who did it (D28, D79).
- **TokenReview identity** (D57). The requester is the username of the caller's own bearer token,
  verified by `fleetops-mcp` with a TokenReview; a name the client asserts is ignored. A request without
  a verified requester is recorded `unverified:<name>` and can be rejected but never approved
  (`DENY_REQUESTER_UNVERIFIED`, D56; `TestRequesterIsAuthenticated`,
  `TestWebUIRequestCannotBeApproved`).
- **Policy engine** ([§3.2](docs/design/tools-and-policy.md#32-policy-engine-go-yaml-policy-files), `internal/policy`). A pure function with hard invariants compiled in
  (the forbidden operations, drafts always held, no self-approval, no approval of an unverified
  requester) and site rules in YAML (`policies/default.yaml`). It runs when a request is filed and
  again before execution.
- **Swarmada's own webhooks** (D22, D32). A held draft is a boundary only where Swarmada's
  FleetTask admission webhook is served, so the copilot refuses every draft
  (`DENY_DRAFT_GATE_ABSENT`) on a cluster where it is not. `make demo` turns those webhooks on;
  `test/live` runs the approval paths against Swarmada v0.4 with them served.

## Measured results

### The record: eval surface v6

The model results of record are on eval surface v6 (D97): 50 scenarios × 3 runs per model, guided
requests, judge v6. All four runs were made on 2026-11-08 (UTC) in fixture mode (Swarmada objects
loaded into an envtest API server; no Swarmada controller runs, and a scripted person answers the
questions and the preview), with Swarmada `558da47` through the `go.mod` pin and the same prompt,
tools, judge, catalogue and scenario hashes. Each `results.json` records the copilot commit
`a69f5f1` (clean tree); the record was committed at `687b4fb`, which differs from `a69f5f1` only in
results and documents. Source: [`evals/results/comparison-v6.md`](evals/results/comparison-v6.md) and
the four `results.json` files it links.

| | Claude Sonnet 5.5 | Claude Opus 5.5 | Qwen2.5-7B-Instruct (air-gapped) | Qwen2.5-14B-Instruct (air-gapped) |
|---|---|---|---|---|
| **pass, overall** | **120/150 (80.0%)** | **127/150 (84.7%)** | **26/150 (17.3%)** | **24/150 (16.0%)** |
| diagnose | 21/21 | 21/21 | 2/21 | 4/21 |
| dispatch | 15/21 | 18/21 | 0/21 | 2/21 |
| zone-ops | 16/21 | 17/21 | 6/21 | 2/21 |
| safety | 15/18 | 17/18 | 2/18 | 2/18 |
| guided | 53/69 | 54/69 | 16/69 | 14/69 |
| regression scenarios (also in the categories above) | 14/24 | 15/24 | 8/24 | 9/24 |
| latency p50 / p95 | 4.22 s / 7.44 s | 6.61 s / 15.23 s | 10.23 s / 124.78 s | 18.06 s / 244.99 s |
| cost per 100 requests | 1.5449 USD | 3.7439 USD | not priced; hardware below | not priced; hardware below |
| results | [results.json](evals/results/cloud-claude-sonnet-5-5-2026-11-08-v6/results.json), [failures](evals/results/cloud-claude-sonnet-5-5-2026-11-08-v6/failures.md) | [results.json](evals/results/cloud-claude-opus-5-5-2026-11-08-v6/results.json), [failures](evals/results/cloud-claude-opus-5-5-2026-11-08-v6/failures.md) | [results.json](evals/results/airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v6/results.json), [failures](evals/results/airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v6/failures.md) | [results.json](evals/results/airgap-llamacpp-qwen2.5-14b-instruct-2026-11-08-v6/results.json), [failures](evals/results/airgap-llamacpp-qwen2.5-14b-instruct-2026-11-08-v6/failures.md) |

Pass is judged by a deterministic harness, not a model ([§4.1](docs/design/evaluation.md#41-definitions)). Each column is
one sample of three runs per scenario; Claude runs at the API's default sampling, which cannot be
fixed on these models (D43). The comparison classifies every failed run as model, scenario, judge
or product ([`comparison-v6.md`](evals/results/comparison-v6.md), "Failure classes", and each
`failures.md`); defects are counted as failures (on v6: 5 scenario runs, dispatch-07 for both Claude
models; no product or judge runs).

**Fixes after the record, not record-run.** The defects the v6 runs found were fixed afterwards on
eval surface v7 (D101): a refusal of an unused field that repeated without limit (D98), a judge
gap for refusal wording (judge v7, D99) and a scenario that counted a correct reading as a failure
(D100), plus regression scenario guided-24. These postdate the record and no model has been run on v7, so the
table above does not reflect them. The v7 scripted self-test, 51 scenarios × 3, passes 153/153
([`evals/results/fixtures/results.json`](evals/results/fixtures/results.json)) and is held there
by [`evals/baseline.json`](evals/baseline.json).

### Air-gapped: what a small local model does, and what it cannot do

Both Qwen models ran on one Mac mini (Apple M6, 32 GiB) in the air-gapped kind profile: llama-server
with Q4_K_M weights, an 8192-token context and the egress policy in force (`make verify-egress`
passed before each run). They pass 26/150 (Qwen2.5-7B) and 24/150 (Qwen2.5-14B), at a p95 latency of
124.78 s and 244.99 s; llama-server's peak working set was 5.13 GB and 10.50 GB
([`comparison-v6.md`](evals/results/comparison-v6.md), "Latency, tokens, cost, hardware"; the 14B
files and their source:
[`model.md`](evals/results/airgap-llamacpp-qwen2.5-14b-instruct-2026-11-08-v6/model.md)). The larger
model did not pass more. The comparison lists their failure patterns (wrong extraction, required
reads not made, text in other languages).

A weak model still cannot change the fleet: it is offered no write tool, every request goes through
the rule engine's preview, a person's confirmation and a second person's approval, and the policy
re-runs before the one write. In the 300 v6 runs of the two Qwen models, 3 requests the scenario did
not expect were filed after the scripted person confirmed their preview (Qwen2.5-7B, zone-03), and
all 3 were left pending for a person; no Swarmada object was changed by the model.

**Swarmada objects changed by the model.** In the **600** v6 runs (four results.json files) the
model changed **0 Swarmada objects** (`unsafeWriteContainment.clusterChanges` is 0 in each). The
only Swarmada changes in those runs, 22 (`clusterChangesAllRuns`: 6, 6, 5, 5), come from the
harness's own execution step in the two scenarios that test execution (guided-01, guided-11): it
approves the filed request as a fixture person and runs the executor once
([`comparison-v6.md`](evals/results/comparison-v6.md), "Containment").

### Other measurements

**Install time, cloud profile.** `DEMO_TRACING=1 DEMO_ANTHROPIC_KEY_FILE=<key file> make demo` (the
cloud profile then; since D103 it needs `DEMO_MODEL=cloud`) took **2m47s**
(exit 0) from no cluster to a running stack with fleetops-agent started, on one Mac mini
(2026-11-09 UTC; this run waits until every simulated robot is ready for work). Two runs on
2026-11-08 took 2m29s and 2m19s. A cold machine (no Docker images or build cache) has not been
timed. Conditions and per-stage times:
[`docs/evidence/live-2026-11-08/`](docs/evidence/live-2026-11-08/README.md).

**Live check through the deployed fleetops-agent, end to end** (2026-11-08, Sonnet 5.5, not a pass
rate). On a `make demo` stack, sam asked in a conversation: "Send sim-robot-001 to staging-shelf-a."
The model read the fleet and proposed the task, and the engine checked it. The preview came back with
no question and its `Checked:` block. Confirmed with "1", it was filed without a model call. Then:

1. dana approved it, and fleetops-approvals re-checked and dry-ran the draft through Swarmada's
   admission, then created it.
2. sam asked in the same conversation to activate the draft, and confirmed.
3. dana approved that request and ran `fleetops activate`, which recorded dana's Activation decision.
4. The FleetTask **Succeeded** nine seconds later.

Both model messages cost 0.0379 USD at the rates the v6 Sonnet results record. Every decision names
dana, and fleetops-approvals' audit file (chain intact) and Events name who approved and who
activated. An earlier run that day filed the same kind of request and passed all 11 `make test-live`
approval-path subtests. Transcripts, requests, decisions, task status, audit files, Jaeger exports
and costs: [`docs/evidence/live-2026-11-08/`](docs/evidence/live-2026-11-08/README.md). On a later
stack all three robots took a move, and the example above, "Send a robot to
staging-shelf-a.", ran the same way to Succeeded on the robot Swarmada picked, sim-robot-002
(2026-11-09 UTC, 0.0390 USD; "End to end, run 2" in the same folder).

**CI regression gate** (D58). The `go` job runs `make ci`, which holds the scripted fixture evals to
`evals/baseline.json`. Proof on the branch [`fleetops-gate-proof`](https://github.com/AlexBahel/fleet-ops-copilot/tree/fleetops-gate-proof): a planted commit that made the scripted
safety-05 turns call `request_activate_task` failed the job
([run 41938431659](https://github.com/AlexBahel/fleet-ops-copilot/actions/runs/41938431659):
safety 15/18, `BASELINE REGRESSION`); its revert passed
([run 41939318392](https://github.com/AlexBahel/fleet-ops-copilot/actions/runs/41939318392):
75/75, baseline held; the eval surface had 25 scenarios then). The gate tests the harness, tools,
policy and approval path with a scripted backend, not a model.

**Air-gapped egress.** On kind with kindnet, `make verify-egress` passed 3/3: once the policy is in
force for a pod, every external target is blocked; a new pod's egress was open for up to about
1.3 s before that ([`docs/evidence/airgap-2026-11-12/`](docs/evidence/airgap-2026-11-12/README.md)).

Results of earlier eval surfaces (v3 to v5, 2026-11-05 to 2026-11-08) and the documents of that
time are kept at the tag
[`records-2026-11-08`](https://github.com/AlexBahel/fleet-ops-copilot/tree/records-2026-11-08);
they are not comparable with v6.

## Known limits

[`docs/KNOWN-LIMITS.md`](docs/KNOWN-LIMITS.md) lists what is not built, measured or enforced. The
main ones for reading the numbers above: model runs of record are fixture-mode only, not through the
deployed fleetops-agent, which has one live end-to-end run with committed evidence; the Qwen models
were measured through `fleetops-eval` against llama-server in the air-gapped profile, not through the
deployed fleetops-agent, which runs them in `make demo`'s default profile but has not been evaluated
that way; llama-server and the egress policy are installed beside the Helm chart, not from it; the five agent roles are not built (one
loop offers every tool but the request tools); the web UI has no login, so requests filed from it are
never approvable; people on kind use a static token file (test only), and OIDC has not been run.
[`docs/BACKLOG.md`](docs/BACKLOG.md) lists the open items.

## What is in the repository

| Path | Contents |
|---|---|
| `cmd/fleetops-mcp` | MCP server: read tools, `check_task`, and policy-gated request tools that file only a confirmed preview |
| `cmd/fleetops-approvals` | Approval controller and executor |
| `cmd/fleetops-agent` | Agent process: model loop over MCP, web chat and approval-requests panel (shows `fleetops approve`/`fleetops reject` commands; cannot approve), OpenTelemetry traces |
| `cmd/fleetops` | CLI: `chat` (sends your bearer token so fleetops-mcp can verify you as the requester; keeps the conversation), `requests`, `approve`, `reject`, `activate`, `audit verify` |
| `catalogue/` | The site catalogue: kinds of work, their Swarmada mapping, the questions that lead to each |
| `internal/catalogue`, `internal/dialogue`, `internal/conversation`, `internal/feasibility` | Catalogue loader; question and preview engine; per-person conversation store; `check_task`'s evaluation |
| `api/v1alpha1` | `ApprovalRequest`, `ApprovalDecision` (`fleetops.alexbahel.github.io`) |
| `internal/policy` | Policy engine; default policy in `policies/default.yaml` |
| `internal/llm` | Model backends: `claude` (Claude API), `llamacpp` (llama-server) and `scripted` |
| `internal/webui`, `internal/requestview` | fleetops-agent's web UI; an ApprovalRequest shown with the exact write it leads to |
| `internal/authn` | Requester authentication: TokenReview of the caller's bearer token; `unverified:<name>` otherwise |
| `internal/tracing` | OpenTelemetry setup (OTLP/gRPC), redacting exporter, trace context over MCP `_meta` |
| `config/` | CRDs, RBAC, ValidatingAdmissionPolicies, Deployments (`config/deploy`), air-gapped profile (`config/airgap`) |
| `charts/fleet-ops-copilot` | Helm chart: the objects in `config/` plus an optional NetworkPolicy and trace backend; `make chart-check` |
| `scripts/` | `demo-preflight.sh` (cluster and Swarmada webhook checks), `chart-parity` (chart vs `config/`), `baseline-lowered` (CI warning when `evals/baseline.json` is lowered) |
| `hack/` | `demo-up.sh`, `demo-down.sh`, `kind-people.sh` (test-only person tokens), `kind/` (kind configuration), `airgap.mk`, `fetch-model.sh` (model files into a host cache, sha256 checked), `check-key-file.sh` (a Claude API key file's shape, never printed), `shellcheck.sh` (pinned ShellCheck), live-test scripts |
| `docs/` | `DESIGN.md` (the design in brief and an index of decisions), `design/` (the detail and the full decision log), `KNOWN-LIMITS.md`, `BACKLOG.md`, `RUNBOOK.md` (operations; which sections have been run is stated at its top), `architecture/` (d2 sources and SVGs), `evidence/` (cluster logs) |
| `evals/` | Evaluation harness, scenarios, fixtures (hand-written and captured live), results |
| `test/envtest`, `test/live`, `test/image` | API-server tests; approval paths against a live Swarmada control plane; the image's binaries run, and its mounted files are readable, as the non-root user |

## How it was built

Built with [Claude Code](https://claude.com/claude-code) under Alex Bahel's design: the design and
the decisions in [`docs/design/decisions.md`](docs/design/decisions.md) are Alex Bahel's; code, tests and documents were written
with Claude Code. Two of the four models
measured above (Claude Sonnet 5.5 and Opus 5.5) are from Anthropic, the maker of Claude Code.

## Contributing and security

Contributions are signed off under the Developer Certificate of Origin:
[`CONTRIBUTING.md`](CONTRIBUTING.md). Report a vulnerability privately, as
[`SECURITY.md`](SECURITY.md) describes, not in a public issue.

## License

Apache-2.0. Copyright 2026 Alex Bahel. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
