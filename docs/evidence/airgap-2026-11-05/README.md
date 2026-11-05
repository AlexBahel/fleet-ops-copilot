# Air-gapped profile on a kind cluster, 2026-11-05

Logs from one single-node kind cluster, `foc-up`, created with `make foc-up` (tracing backend on)
for this run and deleted after it. They are evidence for the egress claim in `docs/DESIGN.md`
§2 and §3.7 (D50, D63), for "no image pulled at runtime" by the air-gapped components, and for
the model server's settings. Times are UTC.

| Item | Value |
|---|---|
| Copilot | `738f848` (manifests, probe and make targets as at that commit) |
| Swarmada | v0.4, `558da47` (go.mod pin), quickstart `healthy-fleet`, admission webhooks served (`config/controlstream-tls`) |
| Cluster | kind v0.33.0, node image `kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5`, containerd 2.3.4, CNI kindnet (kind's default) |
| Host | Mac mini (Mac18,5), Apple M6, 32 GB; Docker Desktop VM: 8 CPUs, 23066771456 bytes memory |
| llama-server image | `ghcr.io/ggml-org/llama.cpp:server-b12004`, index `sha256:b32890309c1a33a534a01b0b461e953dc2c5a6bac20213abcf48e22847a19c85`, already on the host under that digest, loaded with `kind load` (`load-llama-image.log`) |
| Model | Qwen2.5-7B-Instruct GGUF Q4_K_M, two shards; sha256 in `model-SHA256SUMS`, checked on the host and again inside the node (`load-llama-model.log`) |

## Results

| Check | Result | File |
|---|---|---|
| Image digest | host image `RepoDigests` = the pinned index digest; nothing pulled | `load-llama-image.log` |
| Model sha256 | both shards OK on the host and in the node | `load-llama-model.log` |
| `make deploy-airgap` | exit 0 in 12.3 s; pod created 03:59:49, Ready 04:00:00 (11 s); model loaded 5.46 s after the process started; 0 restarts | `deploy-airgap.log`, `llama-server-pod.txt`, `llama-server-startup.log` |
| Server settings | `/props`: `n_ctx` 8192, `total_slots` 1, build `b12004-3c809f443`, `Q4_K - Medium`; `--version` 0.8.0-dev build 12004; threads chosen by the server: `n_threads = 8` (no `--threads` flag); requests 2 CPU / 5Gi, limit 7Gi memory, no CPU limit | `llama-server-props.json`, `llama-server-version.txt`, `llama-server-startup.log` |
| NetworkPolicies | six policies in `foc-system`, API server addresses substituted | `networkpolicies.txt`, `networkpolicy.rendered.yaml` |
| Probe without the policy (`make verify-egress-baseline`) | all three external targets answered at start and after: egress is open without the policy, so the probe can detect it | `verify-egress-baseline.log` |
| `make verify-egress` (R40 probe, D63), 3 runs | **PASS** 3/3. At start (not counted) all three targets answered; the policy was in force when a sentinel attempt to the API server timed out, returning at 2361, 2266 and 2379 ms; control `llama-server:8080/health` 200; after the wait all three targets blocked (curl exit 28) | `verify-egress.log`, `verify-egress-run2.log`, `verify-egress-run3.log` |
| Images | every container in `foc-system` uses `imagePullPolicy: IfNotPresent`. The copilot, llama-server and probe containers: every `Pulled` event says "already present". Two `Pulling` events, both at chart install (03:55:14), before the air-gapped profile: the optional trace backend's `jaegertracing/jaeger:2.22.0` and `otel/opentelemetry-collector:0.164.0`, which are not part of the air-gapped profile (RUNBOOK §8) | `no-runtime-pull.txt` |
| Trace of one evaluation request | `foc.request` → `llm.chat` → 2 × (`tool.call` → `mcp.tool` → `policy.decide` `REQUIRE_APPROVAL`) → `llm.chat`, trace `896e1f3a6a70766dedd60386a116cb4c` (PILOT run of dispatch-05, `foc-eval -otlp-endpoint`, D65); Jaeger UI through `kubectl port-forward svc/foc-jaeger 16686:16686`, which shows the start in the browser's local time (20:07:46 UTC−8 = 04:07:46Z) | `trace-dispatch-05.png` |

## What this shows

Once the policy is in force for a pod, egress from `foc-system` is denied except the Kubernetes
API server and cluster DNS for the copilot components, and denied entirely for the model server.
On this CNI a new pod's egress is open at its start: each probe's at-start attempts (within
~0.35 s of the script starting) connected, as on 2026-11-04 (`../airgap-2026-11-04/`).

Not shown here: whether a connection opened before the policy applies survives it
(`docs/KNOWN-LIMITS.md`).
