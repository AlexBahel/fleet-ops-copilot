# Fleet Ops Copilot — RBAC, admission policies and NetworkPolicy

What each ServiceAccount may do, the admission policies that narrow it, and the network
paths. The summary is in [`DESIGN.md`](../DESIGN.md).

## 3.6 Kubernetes RBAC and admission policies

| Subject | Rules |
|---|---|
| `fleetops-agent` (SA) | `get`,`list` on `approvalrequests` (`fleetops.alexbahel.github.io`) in each Swarmada namespace (ClusterRole `fleetops:fleetops-agent`, RoleBinding `fleetops-agent`), for the web UI's requests panel. Nothing else: no Swarmada access, no `watch`, no write of any kind, no `approvaldecisions`, no `approvalrequests/status` (D51). |
| `fleetops-mcp` (SA) | `get`,`list`,`watch` on `robots`, `fleettasks`, `fleetactions`, `fleetzones`, `zonemaintenances`, `discoveredrobots`, `fleetadapters`, `robotclasses`, `tasktriggers` (`swarmada.io`); `get`,`list` on `swarmadaconfigs` (`check_task`: namespace stop and Pending cap; D80); `get`,`list`,`create` on `events`; `create`,`get`,`list`,`watch` on `approvalrequests`; cluster-scoped `get`,`list` on `validatingwebhookconfigurations`; cluster-scoped `create` on `tokenreviews` (`authentication.k8s.io`), to verify the requester's bearer token (D57). No Swarmada write. |
| `fleetops-approvals` (SA) | reads as above; `get`,`list`,`watch` on `approvalrequests`, `approvaldecisions`; `get`,`update`,`patch` on `approvalrequests/status`; `create` on `fleettasks` and `zonemaintenances`; `create` on `fleetactions`, admitted for dry runs only (`fleetops-approvals-dry-run-only`, D76); `get`,`list` on `swarmadaconfigs`; `patch` + custom verb `estop-trigger` on `fleetzones`; `create` on `events`, `subjectaccessreviews`; cluster-scoped `get`,`list` on `validatingwebhookconfigurations` |
| `fleetops:approver` (ClusterRole, bound to people per namespace with a RoleBinding) | `get`,`list`,`watch` on `approvalrequests`; `create` on `approvaldecisions` |

No copilot ServiceAccount has `delete`, `update` on Swarmada kinds, `activate`, `cancel`,
`append`, `estop-clear`, `admit`, `reject`, `rollout-resume`, `policy-reset`, or any
`trigger-response-*` verb. fleetops-agent holds no write permission at all.

RBAC cannot restrict a `create` by field or a `patch` to one annotation, so the chart installs
ValidatingAdmissionPolicies (CEL, Kubernetes ≥ 1.30):

| Policy | Applies to | Allows only |
|---|---|---|
| `fleetops-approvals-drafts-only` | `CREATE`/`UPDATE`/`DELETE fleettasks` by `fleetops-approvals` | `CREATE` with `spec.draft == true` and the label `fleetops.alexbahel.github.io/created-by: fleetops-approvals` |
| `fleetops-approvals-dry-run-only` | `CREATE`/`UPDATE`/`DELETE fleetactions` by `fleetops-approvals` | `CREATE` with `request.dryRun == true`; nothing else (D76) |
| `fleetops-approvals-zone-stop-only` | `fleetzones` writes by `fleetops-approvals` | `UPDATE` adding `swarmada.io/estop-triggered` where it was absent; besides it, only what Swarmada's estop-actor mutation adds (`swarmada.io/estop-actor`, the `swarmada.io/estop-in-force` finalizer); `spec`, labels, other annotations and other finalizers unchanged |
| `fleetops-decisions` | `CREATE`/`UPDATE approvaldecisions`, both types (`Verdict`, `Activation`) | `CREATE` by a non-ServiceAccount with `decidedBy` and `decidedByGroups` equal to the authenticated user's; no update |
| `fleetops-requests` | `CREATE approvalrequests` | `spec.requestedBy` equal to the creating identity, so a hand-made request cannot name someone else as requester |

Removing the estop annotation is also refused by Swarmada's own `estop-clear` check
(`internal/webhook/fleetzone_webhook.go`).

Every policy that matches a Swarmada resource matches only `fleetops-approvals`' own writes. The copilot
puts no annotation on a Swarmada object a person writes, and no policy of its own on a person's
activation; who activated is recorded in the copilot's own API (D79).

## 3.7 NetworkPolicy

Default-deny ingress and egress in the copilot namespace, then:

- all pods except `llama-server`: egress to cluster DNS (UDP/TCP 53);
- `fleetops-mcp`, `fleetops-approvals`, `fleetops-agent`: egress to the Kubernetes API server (`ipBlock`, because
  NetworkPolicy cannot select the API server by label);
- `fleetops-agent` → `fleetops-mcp` (MCP port); `fleetops-agent` → `llama-server` (airgap only);
- `fleetops-agent` → Claude API (cloud only). NetworkPolicy has no FQDN rules; the chart takes an
  egress `ipBlock` or an egress proxy address. The air-gapped profile (`config/airgap/`) sets neither;
- `llama-server`: ingress from `fleetops-agent` (and the egress probe) only, no egress (D50);
- `fleetops-agent`, `fleetops-mcp` → the OTLP collector, when tracing is on and peers are given;
- the chart's trace Collector → Jaeger's OTLP port and cluster DNS, nothing else, whenever
  `tracing.backend.enabled`, with or without `networkPolicy.enabled` (D68); `fleetops-mcp`, `fleetops-agent`
  → the Collector's OTLP port, with `networkPolicy.enabled` from the chart and beside
  `config/airgap` from `config/airgap/networkpolicy-tracing.yaml` (D69);
- no ingress rule for `fleetops-agent`'s web UI: its Service is ClusterIP only and the chart renders
  no Ingress (D56); it is reached with `kubectl port-forward`.

NetworkPolicy is enforced only by a CNI that implements it. Enforcement is verified by an egress
probe Job (`make verify-egress`), not assumed. A CNI may program a new pod's policy only after its
containers start: on kindnet a new pod's egress was open for up to ~1.3 s (D63). The probe records
egress at pod start, waits until the policy is in force for its own pod, and decides on the
attempt after the wait.
