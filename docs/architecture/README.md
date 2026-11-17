# Architecture diagrams

| Source | Rendered | Shows |
|---|---|---|
| `overview.d2` | `overview.svg` | The people, the three processes, the model, the Kubernetes API server and Swarmada, one line each; colour by what each process may write |
| `approval-flow.d2` | `approval-flow.svg` | The life of a request in eleven steps: ask, preview, confirm, approve, execute, activate |

The diagrams summarise [`docs/DESIGN.md`](../DESIGN.md); where they differ, the design is correct.
What each ServiceAccount may do, the admission policies and Swarmada's webhooks are in DESIGN.md's
table "Guarantees and where each is enforced" and in
[`docs/design/rbac-and-admission.md`](../design/rbac-and-admission.md). The full approval step list,
with every check and refusal code, is in [`docs/design/approvals.md`](../design/approvals.md).

## Rendering

Rendered with [d2](https://d2lang.com) v0.9.0 (each SVG records its version in
`data-d2-version`):

```bash
d2 --layout dagre overview.d2 overview.svg
d2 approval-flow.d2 approval-flow.svg
```

Edit the `.d2` source and re-render; do not edit the SVGs by hand.
