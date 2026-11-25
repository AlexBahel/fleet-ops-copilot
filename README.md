# Fleet Ops Copilot

An operations assistant for robot fleets orchestrated by [Swarmada](https://github.com/swarmada/swarmada).
It reads fleet state through the Kubernetes API, drafts work, and turns every change into a request that a
named person approves; it never clears a stop, releases a robot, deletes, approves its own request, or
edits work that already exists.

<!-- Demo video: a link or an embedded clip goes here. -->

It has run on kind clusters on one machine and is not deployed at any site.

[How to run](#how-to-run) · [Why it exists](#why-it-exists) ·
[How it fits together](#how-it-fits-together) · [Design](docs/DESIGN.md) ·
[Measured results](docs/RESULTS.md) · [Known limits](docs/KNOWN-LIMITS.md)

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
[`docs/RESULTS.md`](docs/RESULTS.md#air-gapped-what-a-small-local-model-does-and-what-it-cannot-do)).

Requires Docker (running), kind, kubectl, helm 3.8+, jq, openssl, curl, make, Go 1.26+ and
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

In the air-gapped profile the last step of `make demo` restarts the copilot's pods, so right after it
an old pod can still be listed for a few seconds as `Completed` or `Terminating`; run the `kubectl`
line again.

With `DEMO_TRACING=1` the list also has `fleetops-jaeger` and `fleetops-otel-collector`, the trace
backend.

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
pod name `get pods` printed in place of `POD_NAME`, then the namespace's recent events. For a pod that
has not restarted, `--previous` answers that the previous container was not found:

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

Other commands (dry run, egress check, live tests, `make ci`, model evaluations):
[`docs/RUNBOOK.md`](docs/RUNBOOK.md#other-commands).

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

## How it fits together

![Overview: the operator asks fleetops-agent, which talks to the model and to fleetops-mcp; fleetops-mcp files requests through the Kubernetes API server; an approver decides; fleetops-approvals makes the one write to Swarmada](docs/architecture/overview.svg)

Three processes, each with its own ServiceAccount: `fleetops-agent` runs the model and the guided
dialogue and may write nothing; `fleetops-mcp` reads Swarmada and files the requests a person
confirmed; `fleetops-approvals` makes the one approved write after a second person approves. The
design in brief, with every decision: [`docs/DESIGN.md`](docs/DESIGN.md). The diagrams and their
sources: [`docs/architecture/`](docs/architecture/).

## More

- [`docs/RESULTS.md`](docs/RESULTS.md): measured results, each linked to the committed file or CI
  run it comes from.
- [`docs/KNOWN-LIMITS.md`](docs/KNOWN-LIMITS.md): what is not built, measured or enforced;
  [`docs/BACKLOG.md`](docs/BACKLOG.md): the open items.
- [`docs/RUNBOOK.md`](docs/RUNBOOK.md): operations.
- [`docs/design/components.md`](docs/design/components.md#6-repository-layout): what is in the
  repository.

## Contributing and security

Contributions are signed off under the Developer Certificate of Origin:
[`CONTRIBUTING.md`](CONTRIBUTING.md). Report a vulnerability privately, as
[`SECURITY.md`](SECURITY.md) describes, not in a public issue.

## License

Apache-2.0. Copyright 2026 Alex Bahel. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
