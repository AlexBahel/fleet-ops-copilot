# Fleet Ops Copilot

An operations assistant for robot fleets orchestrated by
[Swarmada](https://github.com/swarmada/swarmada). It reads fleet state through the
Kubernetes API, drafts work, and turns proposed changes into requests that a named
person approves. It never clears a stop, releases a robot, deletes, approves its own
request, or edits work that already exists.

**Status: early development.** The design is in [`docs/DESIGN.md`](docs/DESIGN.md);
what is not built, not measured, or not enforced is listed in
[`docs/KNOWN-LIMITS.md`](docs/KNOWN-LIMITS.md).

## What is in the repository

| Path | Contents |
|---|---|
| `cmd/foc-mcp` | MCP server: read tools and policy-gated request tools |
| `cmd/foc-approvals` | Approval controller and executor |
| `cmd/foc-agent` | Agent process: model loop over MCP, web chat and approval-requests panel (shows `foc approve`/`foc reject` commands; cannot approve), OpenTelemetry traces |
| `cmd/foc` | CLI: `requests`, `approve`, `reject`, `audit verify` |
| `api/v1alpha1` | `ApprovalRequest`, `ApprovalDecision` (`fleetops.alexbahel.github.io`) |
| `internal/policy` | Policy engine; default policy in `policies/default.yaml` |
| `internal/llm` | Model backends: `claude` (Claude API) and `scripted` |
| `internal/webui`, `internal/requestview` | foc-agent's web UI; an ApprovalRequest shown with the exact write it leads to |
| `internal/tracing` | OpenTelemetry setup (OTLP/gRPC), redacting exporter, trace context over MCP `_meta` |
| `config/` | CRDs, RBAC, ValidatingAdmissionPolicies, Deployments (`config/deploy`) |
| `charts/fleet-ops-copilot` | Helm chart: the objects in `config/` plus an optional NetworkPolicy; `make chart-check` |
| `scripts/` | `foc-up-preflight.sh` (cluster and Swarmada webhook checks), `chart-parity` (chart vs `config/`) |
| `docs/RUNBOOK.md`, `docs/architecture/` | Operations runbook (draft); architecture diagrams (D2 source and SVG) |
| `evals/` | Evaluation harness, scenarios, fixtures (hand-written and captured live), results |
| `test/live` | Approval paths and guardrails against a live Swarmada control plane |

## Build and test

Requires Go 1.25+ and the envtest binaries (`setup-envtest use 1.37.0`).

```bash
make build
make test           # unit tests
make test-envtest   # API-server tests: RBAC, admission policies, approval path
make eval-fixtures  # fixture evaluations with the scripted backend
```

Model evaluations call the Claude API and read `ANTHROPIC_API_KEY` from the environment:

```bash
go run -tags envtest ./evals/cmd/foc-eval -backend claude -model claude-sonnet-5-5 -runs 3 \
  -out evals/results/<date>-cloud-claude-sonnet-5-5/results.json
```

On a kind cluster named `foc-<name>` that runs Swarmada (for example its quickstart),
`make deploy-kind KIND_CLUSTER=foc-<name>` installs the copilot, and
`hack/live-approval-path.sh` runs `test/live` against it.

The Helm chart renders the same objects (checked by `make chart-parity`); it has not been
installed on a cluster yet. See [`docs/RUNBOOK.md`](docs/RUNBOOK.md).

## Fictional scenario

Examples, fixtures, and evaluations use a fictional customer, "Example Distribution
Co.", described and labelled in `docs/DESIGN.md` §2. It is not a real organisation.

## How it was built

Built with [Claude Code](https://claude.com/claude-code) under Al Bahel's design.

## License

Apache-2.0. Copyright 2026 Alex Bahel. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
