# Air-gapped profile: llama.cpp in the cluster

Manifests for running the copilot's language model inside the cluster
(`docs/DESIGN.md` §2, §3.5, §3.7). The model is Qwen2.5-7B-Instruct (GGUF,
Q4_K_M, two shards) served by `llama-server`. The agent calls it through
`internal/llm/llamacpp.go`, which uses the OpenAI-compatible
`/v1/chat/completions` endpoint; the base URL comes from `FOC_LLAMACPP_URL`.
No credential is involved.

**Status:** the manifests are checked against the Kubernetes schemas with
kubeconform (`make validate-airgap`). They have not been applied to a cluster
yet; the resource values and the pod security settings below are the first
values to try, not measured ones.

## Files

| File | Contents |
|---|---|
| `llama-storage-hostpath.yaml` | PersistentVolume (`hostPath: /models`) and the claim `llama-models` |
| `llama-server.yaml` | Deployment (with an init check for the model files) and Service `llama-server:8080` |
| `networkpolicy.yaml` | Egress policies for the namespace, ingress policy for `llama-server` |
| `egress-probe.yaml` | Job that must be unable to reach hosts outside the cluster |

## Image

`ghcr.io/ggml-org/llama.cpp:server-b12004`, index digest
`sha256:b32890309c1a33a534a01b0b461e953dc2c5a6bac20213abcf48e22847a19c85`
(`linux/amd64`, `linux/arm64`, `linux/s390x`). The image is CPU-only. Its
entrypoint is `/app/llama-server`, and it contains `curl`, which the egress probe
uses. The manifests name the tag, because `kind load` stores images under
their tag; `make load-llama-image` pulls by digest, so the tag cannot drift.

## Offline behaviour

* The model is read from the claim at `/models`; the pod never downloads it.
  `--offline` additionally forbids network access in `llama-server`.
* `imagePullPolicy: IfNotPresent` and a pre-loaded image: a missing image is
  an error, not a pull.
* `--jinja` is required for tool calling. `--parallel 1` and `--ctx-size 8192`
  are set explicitly, because the defaults (automatic slot count, context from
  the model) change the context each request gets.

## Network policy

Together the policies allow, for egress, only cluster DNS in `kube-system` (every
pod except `llama-server`), the Kubernetes API server (`foc-mcp`,
`foc-approvals`, `foc-agent`), `llama-server` port 8080 (`foc-agent`,
`foc-egress-probe`) and `foc-mcp` port 8080 (`foc-agent`). `llama-server` may
open no connection at all. Ingress to `llama-server` is limited to `foc-agent`
and `foc-egress-probe`. Measured on a kind cluster:
`docs/evidence/airgap-2026-11-04/`.

`networkpolicy.yaml` contains two documentation addresses (`192.0.2.10`,
`192.0.2.11`) where the API server endpoint and Service address go;
`make deploy-airgap` substitutes them and refuses to apply otherwise.

A NetworkPolicy has an effect only if the cluster's network plugin enforces
it. `make verify-egress` runs the probe: the Job completes only if an
in-namespace control request succeeds and every external target (two IP
addresses and one name) is unreachable. `make verify-egress-baseline` runs the
same probe in a namespace without the policy, where it must report open egress;
without that result a passing probe does not show that the probe works.
The probe first makes one attempt at each external target at pod start and
prints it without counting it; then it waits until the policy is in force for
its own pod (a connection to the API server, which the policy denies it, is
refused or times out; up to 60 s, exit 3 otherwise); then the control; then
the attempt that decides. On kind's default CNI a new pod's egress was open for
up to about 1.2 s after its container started, so the at-start line there
reports open egress (`docs/KNOWN-LIMITS.md`, D63). The claim is therefore:
egress denied except the Kubernetes API and cluster DNS for the copilot
components, none for the model server, once the policy is in force for the
pod. Where a pod must not have that window, use a CNI whose documentation says
it enforces policy before the pod's containers start, and check it with
`make verify-egress`; this repository has not checked which CNIs do.

## Make targets (kind clusters named `foc-*` only)

```sh
make load-llama-image KIND_CLUSTER=foc-airgap          # image into the node
make load-llama-model KIND_CLUSTER=foc-airgap MODEL_DIR=<dir> MODEL_SUMS=<sha256sum file>
make deploy-airgap    KIND_CLUSTER=foc-airgap
make verify-egress-baseline KIND_CLUSTER=foc-airgap
make verify-egress    KIND_CLUSTER=foc-airgap
```

## Limits

* `hostPath` ties the model to one node: single-node clusters only.
* DNS is allowed so that Service names resolve. At a site with no upstream
  resolver this reaches nothing outside the cluster; elsewhere it is a path
  for DNS queries.
* The pod runs as UID 65532 with a read-only root file system. If the image
  needs a writable path that is not `/tmp`, the pod fails at start and the
  setting has to be relaxed.
