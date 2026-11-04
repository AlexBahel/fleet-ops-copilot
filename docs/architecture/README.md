# Architecture diagrams

| Source | Rendered | Shows |
|---|---|---|
| `components.d2` | `components.svg` | The three processes and their ServiceAccounts, the copilot's admission policies and CRDs, Swarmada's admission webhooks, the cloud and air-gapped model profiles, NetworkPolicy |
| `approval-flow.d2` | `approval-flow.svg` | A draft `FleetTask` from request to execution, then its activation, which the approver performs with their own credentials |

Solid boxes are on main. Dashed boxes are designed in `docs/DESIGN.md` but not on main:
`foc-agent` (only its ServiceAccount exists) and the NetworkPolicy (a chart value, off by default).
The air-gapped profile is on main as plain manifests (`config/airgap/`, `make deploy-airgap`), not
in the chart.

The diagrams summarise `docs/DESIGN.md` §3; where they differ, the design document is correct.

## Rendering

Rendered with [D2](https://d2lang.com): `components.svg` with v0.9.0, `approval-flow.svg` with
v0.7.1 (each SVG records its version in `data-d2-version`):

```bash
d2 --layout elk components.d2 components.svg
d2 approval-flow.d2 approval-flow.svg
```

Edit the `.d2` source and re-render; do not edit the SVGs by hand.
