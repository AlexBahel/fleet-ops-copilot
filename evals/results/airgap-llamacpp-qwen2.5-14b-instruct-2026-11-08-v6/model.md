# Model files of this run

| | |
|---|---|
| Model | Qwen2.5-14B-Instruct, GGUF, quantization Q4_K_M (`server.props.model_ftype` "Q4_K - Medium" in `results.json`) |
| Source | https://huggingface.co/Qwen/Qwen2.5-14B-Instruct-GGUF, the official Qwen repository, revision `b466e1f8c07172155743e8e1307507d8a4f91fbd` (read 2026-11-08T04:58:46Z) |
| License | Apache-2.0 (the repository's `LICENSE` file and model card) |
| Files | `qwen2.5-14b-instruct-q4_k_m-00001-of-00003.gguf` (3,991,999,872 bytes), `-00002-of-00003.gguf` (3,989,373,504 bytes), `-00003-of-00003.gguf` (1,006,737,120 bytes) |
| sha256 | [`model-SHA256SUMS`](model-SHA256SUMS), the repository's LFS object IDs for that revision |

The files were downloaded on the host from that revision, checked against `model-SHA256SUMS` there, copied into the
kind node's `/models` with `make load-llama-model` (`MODEL_FILES` set to the three names above) and checked again inside
the node; nothing was downloaded inside the cluster. llama-server was the same image, build, arguments (other than
`--model` and `--alias`), context size and slot count as the Qwen2.5-7B-Instruct run of the same day
(`../airgap-llamacpp-qwen2.5-7b-instruct-2026-11-08-v6/`); the Deployment was `config/airgap/llama-server.yaml` with the
model file names, the alias and the memory request and limit changed (11Gi / 13Gi instead of 5Gi / 7Gi), as recorded in
`backendSettings`. The Qwen2.5-7B-Instruct files are those of `docs/evidence/airgap-2026-11-05/model-SHA256SUMS`.

`memory-samples.log` holds the node's memory and the llama-server container's working set, sampled every 10 s from the
Deployment's apply through the run.
