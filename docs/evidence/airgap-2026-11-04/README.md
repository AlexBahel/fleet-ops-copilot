# Air-gapped profile on a kind cluster, 2026-11-04

Logs from one single-node kind cluster, `foc-airgap`, created for this run and deleted after it.
They are evidence for the egress claim in `docs/DESIGN.md` §3.7 (decision D50) and for "no image
pulled at runtime". Times are UTC.

| Item | Value |
|---|---|
| Copilot | `e333b9a` with `config/airgap/networkpolicy.yaml` from the commit that adds this directory (sha256 `6f3ce007d54a44d881074a0dab495aa3a254944f919bec9d7eb5fa422671a870`); `llama-server-egress-before.log` used the policy as it was at `e333b9a` |
| Swarmada | v0.4, `558da47` (tag `rfc-0001-v0.4`), quickstart `healthy-fleet`, admission webhooks served (`config/controlstream-tls`) |
| Cluster | kind v0.33.0, node image `kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5`, containerd 2.3.4, CNI kindnet (kind's default) |
| Host | Mac mini (Mac18,5), Apple M6; Docker Desktop VM: 12 CPUs, 8319172608 bytes memory |
| llama-server image | `ghcr.io/ggml-org/llama.cpp:server-b12004`, index `sha256:b32890309c1a33a534a01b0b461e953dc2c5a6bac20213abcf48e22847a19c85`, linux/arm64 manifest `sha256:7435aa4e1eab…` (`load-llama-image.log`) |

## Results

| Check | Result | File |
|---|---|---|
| Probe in a namespace without the policy (`make verify-egress-baseline`) | `https://1.1.1.1`, `https://8.8.8.8`, `https://example.com` answered: egress is open without the policy, so the probe can detect it | `verify-egress-baseline.log` |
| `make verify-egress` (probe Job as committed) | **FAIL**: control `llama-server:8080/health` 200, all three external targets answered. Repeated twice with the same result | `verify-egress.log`, `verify-egress-startup-window.log` (A) |
| Same probe, waiting 10 s before its first request | PASS: control 200, all three external targets blocked (curl exit 28) | `verify-egress-startup-window.log` (B) |
| New pod, `https://1.1.1.1` every ~0.5 s from container start | 2 runs: the attempt at 0 ms connected; every attempt from about 1.2 s (1230 ms, 1191 ms) to about 57 s was blocked | `egress-startup-window.log`, `egress-window-job.yaml` |
| `llama-server` pod (running 12 min), policy as committed at `e333b9a` | reached `foc-mcp` in the namespace (HTTP 200) and resolved external names; external addresses and the API server blocked | `llama-server-egress-before.log` |
| `llama-server` pod, this policy | no connection at all: name lookups time out; `foc-mcp` by pod IP, `1.1.1.1`, `8.8.8.8`, the API Service and endpoint addresses time out | `llama-server-egress-after.log` |
| `foc-mcp`, `foc-approvals` (ephemeral container in each running pod, same network namespace) | API server reachable at its Service address, endpoint address and DNS name (HTTP 200 `/version`); cluster DNS answers; `llama-server`, the other copilot pod, `1.1.1.1`, `8.8.8.8`, `example.com` time out | `copilot-pods-egress.log` |
| `kubectl port-forward svc/llama-server` from the host | works under the ingress policy (`/health` 200) | `port-forward-under-policy.log` |
| Images | every container in `foc-system` (including Jobs and ephemeral containers) uses `imagePullPolicy: IfNotPresent`; every `Pulled` event says the image was already present on the machine; no `Pulling` event | `no-runtime-pull.txt` |

## What this shows

Once the policy is in effect for a pod, egress from `foc-system` is denied except the Kubernetes
API server and cluster DNS for the copilot components, and denied entirely for the model server
(`llama-server`).

Limits, measured here and listed in `docs/KNOWN-LIMITS.md`:

- On this CNI a new pod's egress is open for its first moments: up to about 1.2 s after the
  container starts, in both measured runs. The committed probe makes its requests in that window,
  so `make verify-egress` fails on this cluster.
- The probe only shows that new connections are refused. Whether a connection opened in that
  window survives once the policy applies was not tested.
- Cluster DNS answered queries for external names (kind's CoreDNS forwards to the host's
  resolver). Copilot pods can resolve names; they cannot connect.
- `kubectl port-forward` is not subject to NetworkPolicy; who may use it is governed by RBAC
  (`pods/portforward`).
- Image pulls are made by the node, not by pods, so NetworkPolicy does not govern them. "Nothing
  pulled at runtime" covers `foc-system` and rests on `IfNotPresent` and the events above.
  Outside it, the node pulled the cert-manager images (`quay.io/jetstack/*:v1.16.2`) when the
  Swarmada quickstart installed cert-manager, and `busybox:1.36` for the ephemeral container the
  live approval-path test (`hack/live-approval-path.sh`) starts in Swarmada's namespace
  (`no-runtime-pull.txt`, last section).
