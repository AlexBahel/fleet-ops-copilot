# make demo, air-gapped profile (the default), 2026-11-13

Two runs of `make demo` with no options on one Mac mini, from a fresh clone of the branch that made the
air-gapped profile the default (D103), following the README's "How to run" in a new zsh. The first run
started with an empty model cache; the second reused it. Times are UTC. No eval was run: the two
questions below are single examples, not a pass rate.

| Item | Value |
|---|---|
| Copilot | `490fbf2`, a fresh clone (manifests, scripts and README as at that commit) |
| Host | Mac mini (Mac18,5), Apple M6; Docker Desktop VM: 8 CPUs, 23066771456 bytes memory (`docker info`) |
| Shell | zsh 5.9, `zsh -f -i` (no rc files, `interactivecomments` off), README blocks pasted unchanged |
| Model | Qwen2.5-7B-Instruct GGUF Q4_K_M, the files of `config/airgap/model-SHA256SUMS`, downloaded by `hack/fetch-model.sh` in run 1 |
| llama.cpp image | `ghcr.io/ggml-org/llama.cpp@sha256:b32890309c1a33a534a01b0b461e953dc2c5a6bac20213abcf48e22847a19c85`, already on the host (not pulled in either run) |
| Tracing | off (no `DEMO_TRACING`) |

## Results

| Check | Result | File |
|---|---|---|
| `make demo`, empty model cache | exit 0, **6m45s** (405 s); stage "model files, llama.cpp image" 3m37s (download of both files and their sha256 check), "llama-server, egress policy" 52 s | `demo-run1.txt` |
| `make demo`, cached model files | exit 0, **3m18s** (198 s); both files "in the cache, sha256 OK", stage 9 s | `demo-run2.txt` |
| sha256 | both files OK after the download (run 1), in the cache (run 2), on the host and in the kind node (`make load-llama-model`, both runs) | `demo-run1.txt`, `demo-run2.txt` |
| fleetops-agent | `-backend=llamacpp -llama-url=http://llama-server:8080 -model=qwen2.5-7b-instruct`, `FLEETOPS_LLAMACPP_TEMPERATURE` 0.7, `TOP_P` 0.8, `TOP_K` 20, `REPEAT_PENALTY` 1.05; log "backend llamacpp:qwen2.5-7b-instruct"; the only Secret in `fleetops-system` is Helm's release record | `fleetops-agent-args-env-run1.txt`, `cluster-run1.txt` |
| Pods and policies | fleetops-agent, fleetops-approvals, fleetops-mcp, llama-server 1/1 Running, 0 restarts; the six NetworkPolicies of `config/airgap/networkpolicy.yaml` | `cluster-run1.txt` |
| `fleetops chat` as sam, "Show me all robots" (run 1) | answered after **134 s** (prompt 22:04:12 to 22:06:26); called `list_robots`; named the three robots, Idle, battery 99% | `cli-question-run1.txt`, `llama-server-timings-run1.txt` |
| Web chat as sam, same question (run 1) | answered in **33227 ms**, 2 model turns, `list_robots`; recorded for `unverified:sam`. The answer misreads one condition: it says "position frame not reported (ZonePositionOverdue)" for each robot, whose condition has status False (reason PositionReported) | `web-question-run1.txt` |
| `make verify-egress` (run 2, the command the summary prints) | **PASS**: at start (not counted) the three external targets answered; the policy was in force at 2301 ms; control `llama-server:8080/health` 200; then all three blocked (curl exit 28) | `verify-egress-run2.log` |
| A key file holding clipboard text, `make demo DEMO_MODEL=cloud` | refused by `hack/check-key-file.sh` and by `make demo` before anything was created ("Nothing was started"); no part of the file printed | `bad-key-file-refused.txt` |

An earlier web question in run 1, typed into a name field that already held "sam", went in as
`unverified:samsam` and answered in 22877 ms; it is not counted above.

## Memory (the Docker VM threshold of D103)

Sampled every 10 s from before the cluster existed until `make demo-down`
(`memory-samples-run1.log`, 46 samples; `memory-samples-run2.log`, 21 samples): the VM's `MemTotal` and
`MemAvailable` (the kind node's `/proc/meminfo` is the VM's), the node container's cgroup
`memory.current` and `memory.peak`, and llama-server's working set (`crictl stats`).

| | Run 1 | Run 2 |
|---|---|---|
| VM memory in use, MemTotal − MemAvailable, highest sample | 7825125376 bytes (22:05:31) | 7810985984 bytes (22:12:39) |
| Node container `memory.peak` (includes page cache, e.g. of the model files) | 8953925632 bytes | 8753778688 bytes |
| llama-server working set, highest sample | 5108858880 bytes (22:07:44) | 4934344704 bytes (22:12:39) |

The 10 s samples can miss a short peak of the VM figure; `memory.peak` is the node cgroup's own high-water
mark. The highest of these, 8953925632 bytes, is below the 12 GiB (12884901888 bytes) that `make demo`
requires for this profile; the threshold was kept (D103). This was one stack with two questions; a long
conversation that fills llama-server's 8192-token context was not run.
