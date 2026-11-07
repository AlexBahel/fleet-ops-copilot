# Architecture diagrams

| Source | Rendered | Shows |
|---|---|---|
| `components.d2` | `components.svg` | The three processes and their ServiceAccounts, the copilot's admission policies and CRDs, Swarmada's admission webhooks, the cloud and air-gapped model profiles, NetworkPolicy, the optional trace collector |
| `approval-flow.d2` | `approval-flow.svg` | A draft `FleetTask` from request to execution, then its activation, which the approver performs with their own credentials |

Solid boxes are built. The dashed box is the NetworkPolicy: in the chart, off by default, never
applied to a cluster. Dashed edges are optional (traces) or not wired by any manifest (fleetops-agent to
llama-server). The air-gapped profile is plain manifests (`config/airgap/`, `make deploy-airgap`),
not in the chart. fleetops-agent's agent roles (Router ... Reporter) are not built: one loop offers
every tool.

The diagrams summarise `docs/DESIGN.md` §3; where they differ, the design document is correct.

## Rendering

Rendered with [D2](https://d2lang.com) v0.9.0 (each SVG records its version in
`data-d2-version`):

```bash
d2 --layout elk components.d2 components.svg
d2 approval-flow.d2 approval-flow.svg
```

Edit the `.d2` source and re-render; do not edit the SVGs by hand.
