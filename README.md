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
| `cmd/foc` | CLI: `requests`, `approve`, `reject`, `audit verify` |
| `api/v1alpha1` | `ApprovalRequest`, `ApprovalDecision` (`fleetops.alexbahel.github.io`) |
| `internal/policy` | Policy engine; default policy in `policies/default.yaml` |
| `config/` | CRDs, RBAC, ValidatingAdmissionPolicies |
| `evals/` | Evaluation harness, scenarios, fixtures |

## Build and test

Requires Go 1.25+ and the envtest binaries (`setup-envtest use 1.37.0`).

```bash
make build
make test           # unit tests
make test-envtest   # API-server tests: RBAC, admission policies, approval path
make eval-fixtures  # fixture evaluations with the scripted backend
```

## Fictional scenario

Examples, fixtures, and evaluations use a fictional customer, "Example Distribution
Co.", described and labelled in `docs/DESIGN.md` §2. It is not a real organisation.

## How it was built

Built with [Claude Code](https://claude.com/claude-code) under Al Bahel's design.

## License

Apache-2.0. Copyright 2026 Alex Bahel. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
