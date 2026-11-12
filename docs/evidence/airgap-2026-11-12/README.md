# Air-gapped profile on a kind cluster, 2026-11-12

Logs from one single-node kind cluster, `fleetops-demo`, created with `DEMO_TRACING=1 make demo`
(the chart with its trace backend, D61) for this run and deleted after it with `make demo-down`.
They are evidence for the egress claim in `docs/DESIGN.md` §2 and §3.7 (D50, D63), for the
trace backend's policies beside the air-gapped profile (D68, D69), for "no image pulled at
runtime" by the air-gapped components, and for the limits in `docs/KNOWN-LIMITS.md`, "Egress in
the air-gapped profile". No eval and no model request was run. Times are UTC.

| Item | Value |
|---|---|
| Copilot | `218c6b0` (manifests, probe and make targets as at that commit) |
| Swarmada | v0.4, `558da47` (go.mod pin), quickstart `healthy-fleet`, admission webhooks served, three simulated robots (`demo.log`) |
| Cluster | kind v0.33.0, node image `kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5`, containerd 2.3.4, CNI kindnet (`kindest/kindnetd:v20260820-69b56db7`, kind's default) (`environment.txt`) |
| Host | Mac mini (Mac18,5), Apple M6, 34359738368 bytes memory; Docker Desktop VM: 8 CPUs, 23066771456 bytes memory |
| llama-server image | `ghcr.io/ggml-org/llama.cpp:server-b12004`, index `sha256:b32890309c1a33a534a01b0b461e953dc2c5a6bac20213abcf48e22847a19c85`, already on the host under that digest, loaded with `kind load` (`load-llama-image.log`) |
| Model | Qwen2.5-7B-Instruct GGUF Q4_K_M, two shards; sha256 in `model-SHA256SUMS`, checked on the host and again inside the node (`load-llama-model.log`) |
| fleetops-agent | not started: no Claude API key Secret was created (`no-runtime-pull.txt`, event `Failed`) |

## Results

| Check | Result | File |
|---|---|---|
| Image digest | host image `RepoDigests` = the pinned index digest; nothing pulled | `load-llama-image.log` |
| Model sha256 | both shards OK on the host and in the node | `load-llama-model.log` |
| `make deploy-airgap` | exit 0 in 12 s; pod created 17:39:39, Ready 17:39:50 (11 s); model loaded 4.60 s after the process started; 0 restarts | `deploy-airgap.log`, `llama-server-pod.txt`, `llama-server-startup.log` |
| Server settings | `/props`: `n_ctx` 8192, `total_slots` 1, build `b12004-3c809f443`; `n_threads = 8` chosen by the server (no `--threads` flag); requests 2 CPU / 5Gi, limit 7Gi memory, no CPU limit | `llama-server-props.json`, `llama-server-startup.log`, `llama-server-pod.txt` |
| NetworkPolicies | seven from `config/airgap`, created 17:39:50, API server addresses substituted, including `fleetops-airgap-egress-otlp` (the Collector was installed); the chart's `fleetops-otel-collector-egress-jaeger`, created 17:35:29 | `networkpolicies.txt`, `networkpolicy.rendered.yaml` |
| Probe without the policy (`make verify-egress-baseline`) | all three external targets answered at start and after: egress is open without the policy, so the probe can detect it | `verify-egress-baseline.log` |
| `make verify-egress` (D63), 3 runs | **PASS** 3/3. At start (not counted) all three targets answered; the policy was in force when a sentinel attempt to the API server timed out, returning at 2517, 2295 and 2475 ms; control `llama-server:8080/health` 200; after the wait all three targets blocked (curl exit 28) | `verify-egress-run1.log`, `verify-egress-run2.log`, `verify-egress-run3.log` |
| Start-up window: a new pod with the probe's label tries `https://1.1.1.1` every ~0.5 s | 2 runs: the attempt at 0 ms connected; every attempt from 1302 ms and 1214 ms after the script started to the last (57344 ms, 57267 ms) was blocked | `egress-startup-window.log`, `egress-startup-window-run1.log`, `egress-startup-window-run2.log`, `egress-window-job.yaml` |
| `fleetops-mcp`, `fleetops-approvals` (ephemeral container in each running pod, same network namespace; pods restarted by `make deploy-airgap`) | API server reachable at its Service address, endpoint address and DNS name (HTTP 200 `/version`); `llama-server`, the other copilot pod, Jaeger, `1.1.1.1`, `8.8.8.8`, `example.com` time out. `fleetops-mcp` connects to the Collector's OTLP port 4317 (curl exit 1: TCP connected, plain HTTP is not gRPC); `fleetops-approvals` times out there (D69) | `copilot-pods-egress.log`, `pod-addresses.txt` |
| `fleetops-otel-collector` (ephemeral container; pod started 17:35:29, before the air-gapped policies) | Jaeger's OTLP port 4317 connects (curl exit 1); Jaeger's UI port, the API server, the other pods and the external targets time out (D68) | `copilot-pods-egress.log` |
| `llama-server` (`kubectl exec`) | every destination times out, its own Service name included; only its own pod address answered | `llama-server-egress.log` |
| `kubectl port-forward svc/llama-server` from the host | works under the ingress policy (`/health` 200) | `port-forward-under-policy.log` |
| A connection opened before the policy | pod `fleetops-conn-check` (started 17:39:30, before the policies) kept one HTTP/1.1 connection to the API server's `/version` and reused it every 3 s: 299 reused requests, all HTTP 200, the last at 17:54:28, 14 min 38 s after the policies were created. Its new connections every 15 s: HTTP 200 at 17:39:30 and 17:39:45, then all 44 from 17:40:05 to 17:54:26 timed out | `conn-check.log`, `conn-check-pod.yaml` |
| Collector restart under the air-gapped policy | 1. one scripted trace (`fleetops-eval -only zone-02-stop -otlp-endpoint`, D65) through the Collector started before the policies: 9 spans in Jaeger. 2. `rollout restart`. 3. The first trace after it did not reach the Collector: the local port-forward had attached to the terminating pod and refused the exporter's connections (4 `connection refused` errors in fleetops-eval's log), so its 0 spans say nothing about the policy. Repeated with the port-forward on the running pod: 9 spans; after a second restart: 9 spans; 0 export failures in the restarted Collectors' logs. 5. New connections from the restarted Collector's network namespace: Jaeger 4317 connects; Jaeger 16686 and `1.1.1.1` time out | `collector-restart-check.log` |
| Images | every container, init container, ephemeral container and Deployment template in `fleetops-system` uses `imagePullPolicy: IfNotPresent`. Two `Pulling` events, both at chart install (17:35:29), before the air-gapped profile: the trace backend's `otel/opentelemetry-collector:0.164.0` and `jaegertracing/jaeger:2.22.0`, which are not part of the air-gapped profile (RUNBOOK §8); every other `Pulled` event says the image was already present | `no-runtime-pull.txt` |

Measurement-only pods, not part of the profile, also appear in the events: `fleetops-conn-check`
was started three times (the first two versions held no connection: Jaeger's UI answers with
`Connection: close`, and the API server ends an anonymous HTTP/2 connection after one request),
and `hdrcheck`, `hdrcheck2`, `hdrcheck3` were the one-off curl pods that showed this.

## What this shows

Once the policy is in force for a pod, egress from `fleetops-system` is denied except the
Kubernetes API server and cluster DNS for the copilot components, and denied entirely for the
model server. With the chart's trace backend installed, `fleetops-mcp` may also reach the
Collector's OTLP port (D69) and the Collector may reach Jaeger's OTLP port (D68), and a Collector
started under the policy delivered spans to Jaeger.

On this CNI a new pod's egress is open at its start: each probe's at-start attempts connected,
and the start-up window measured 1.2 to 1.3 s. A connection opened before the policy existed kept
working under it for as long as it was used (14 min 38 s here), while new connections from the
same pod were refused. So a connection opened in a new pod's start-up window, or by any pod before
the policy is applied, can outlive the policy (`docs/KNOWN-LIMITS.md`).
