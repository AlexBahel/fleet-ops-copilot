# `make foc-up` timed on one machine, 2026-11-05

Two runs of `FOC_UP_TRACING=1 make foc-up`, each followed by `make foc-down`, on one Mac mini.
They time an empty FOC state to a running stack, not a cold machine. Times are UTC.

| Item | Value |
|---|---|
| Copilot | `d08c345`: the latest commit when both runs started, by commit time (the logs do not record the commit). Since then `hack/foc-up.sh` is unchanged; `Makefile` and the chart's NetworkPolicy template changed (D68, D69) |
| Swarmada | v0.4, `558da47` (go.mod pin), quickstart `healthy-fleet`, admission webhooks served (`config/controlstream-tls`) |
| Cluster | kind v0.33.0, single node, `kindest/node:v1.37.0` pinned by digest (`hack/foc-up.sh`) |
| Host | Mac mini (Mac18,5), Apple M6, 12 cores, 32 GB, macOS 27.0.1; Docker Desktop 29.9.2, VM 8 CPUs, 23066771456 bytes memory (`host-and-docker.txt`, read 03:37:55Z) |
| Options | `FOC_UP_TRACING=1` (the chart's trace backend: OpenTelemetry Collector and Jaeger); no `FOC_UP_ANTHROPIC_KEY_FILE` |

## Results

| Run | Ended | Total (script) | `time` | Exit | File |
|---|---|---|---|---|---|
| 1 | 03:52:42 | 2m14s (134 s) | 2:14.09 | 0 | `run1-last-run.txt`, `time.txt` |
| 2 | 03:55:24 | 2m20s (140 s) | 2:19.60 | 0 | `run2-last-run.txt`, `time.txt` |

Per stage (run 1 / run 2): kind cluster 25 s / 24 s; Swarmada quickstart 1m14s / 1m27s; Swarmada
webhooks 11 s / 12 s; copilot image 11 s / 5 s; health checks 10 s / 10 s; every other stage 1 s or
less. `*-last-run.txt` is the summary `hack/foc-up.sh` writes at the end of a run.

## Conditions that bound these numbers

- **Before each run** there was no kind cluster and no `$FOC_UP_DIR` for run 1. `make foc-down`
  leaves `$FOC_UP_DIR/logs` and `$FOC_UP_DIR/swarmada`, so run 2 started with those present.
- **Docker's image and build caches were already filled.** Image pulls and the first build of
  Swarmada's controller image on a machine without them are not timed here.
- **foc-agent was not started.** Without the API key Secret the pod waits in
  `CreateContainerConfigError` and `foc-up` does not wait for it (it says so in the run's output).
  The health checks covered the foc-mcp and foc-approvals rollouts, foc-mcp's `/healthz`, the trace
  backend's rollouts and each person's token.
- **"Helm chart 0m00s"** is `helm upgrade --install` without `--wait`; the rollouts are counted
  under the health checks.
- The quickstart's simulated adapter needs Python with gRPC first on `PATH`; `foc-up` does not check
  for it.
- One machine, two runs. The full run logs are not committed (they hold local paths).
