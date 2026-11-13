#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# make demo: from an empty machine state to a running stack on a kind
# cluster, in one command, and the elapsed time at the end.
#
#   1. a kind cluster fleetops-demo whose API server has the static token file for
#      the fictional people sam, dana and eve (D60, test only);
#   2. Swarmada's warehouse quickstart at the commit go.mod pins;
#   3. Swarmada's admission webhooks turned on (config/controlstream-tls, the
#      overlay its `make deploy` applies; docs/RUNBOOK.md section 6);
#   4. scripts/demo-preflight.sh (webhooks present, with endpoints, answering);
#   4b. hack/demo-sim-fleet.sh start: the simulated fleet kept live (one sim
#      adapter per robot, in the background; RobotClass simbot-v1 as a
#      labelled demo workaround), each Robot pointed at its adapter before
#      that adapter starts, every robot registered and ready for work (D59);
#   5. the image built on the host and loaded into the cluster;
#   6. the Helm chart installed into fleetops-system;
#   6b. air-gapped profile (the default, D103): llama-server, its model files and
#      the egress NetworkPolicy from config/airgap (make load-llama-image,
#      load-llama-model, deploy-airgap);
#   7. the people's RoleBindings and token kubeconfigs;
#   8. health checks.
#
# Model profile (D103): DEMO_MODEL=airgap (the default) runs fleetops-agent with
# Qwen2.5-7B-Instruct on llama-server in the cluster, egress denied, no API key.
# Its model files are fetched once into a cache on this machine (sha256 checked,
# hack/fetch-model.sh) before the cluster is created, and the Docker VM must have
# at least 12 GiB of memory. DEMO_MODEL=cloud runs it with Claude through the
# Claude API and needs DEMO_ANTHROPIC_KEY_FILE, whose shape is checked
# (hack/check-key-file.sh) before anything starts.
#
#   CLUSTER=fleetops-demo KUBECONFIG=<dedicated file> hack/demo-up.sh        (make demo sets both)
#
# Cluster and kubeconfig rules are hack/lib/guard.sh's: CLUSTER matches
# ^fleetops-[a-z0-9-]+$, KUBECONFIG is a dedicated file and never ~/.kube/config
# (the Swarmada quickstart runs `kubectl config use-context`, kind writes its
# context into KUBECONFIG). Every kubectl and helm call here names the
# kubeconfig and the context kind-$CLUSTER.
#
# Environment (all optional):
#   DEMO_DIR               working directory (default ~/.local/state/fleet-ops-copilot/demo, outside the repository: it holds tokens): people/ (tokens), persons/ (kubeconfigs),
#                            swarmada/ (writable copy of the pinned module), logs/, last-run.txt
#   DEMO_SCENARIO          Swarmada quickstart scenario (default healthy-fleet)
#   DEMO_NODE_IMAGE        kind node image (default: kind v0.33.0's own default, pinned by digest)
#   DEMO_IMAGE             copilot image (default fleet-ops-copilot:dev)
#   DEMO_TRACING=1         install the chart's optional Collector + Jaeger and export traces to it (their images are
#                            pulled by the node: not part of the air-gapped profile)
#   DEMO_MODEL             airgap (default) or cloud
#   DEMO_MODEL_CACHE       airgap: directory for the model files (default ${XDG_CACHE_HOME:-~/.cache}/fleet-ops-copilot/models/qwen2.5-7b-instruct-q4_k_m)
#   DEMO_ANTHROPIC_KEY_FILE  cloud only, required: file holding the Claude API key alone; becomes Secret
#                            fleet-ops-copilot-claude. Refused with DEMO_MODEL=airgap. Nothing here calls the API.
#   DEMO_DRY_RUN=1         print every command that would change anything, run none of them. Reads nothing
#                            from a cluster. Still creates DEMO_DIR with the tokens and the rendered kind
#                            configuration, and reports missing tools instead of stopping.
#
# Run on kind on one machine (docs/evidence/live-2026-11-08/).
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/guard.sh
source "$repo/hack/lib/guard.sh"
guard_cluster demo-up.sh
export CLUSTER KUBECONFIG

DRY="${DEMO_DRY_RUN:-0}"
dir="${DEMO_DIR:-$HOME/.local/state/fleet-ops-copilot/demo}"
scenario="${DEMO_SCENARIO:-healthy-fleet}"
# The default node image of kind v0.33.0 (pkg/apis/config/defaults/image.go).
node_image="${DEMO_NODE_IMAGE:-kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5}"
img="${DEMO_IMAGE:-fleet-ops-copilot:dev}"
ctx="kind-$CLUSTER"
ns=fleetops-system
swarm_ns="${SWARMADA_NAMESPACE:-warehouse-a}"

# ---- model profile (D103), before anything is created -----------------------------
profile="${DEMO_MODEL:-airgap}"
key_file="${DEMO_ANTHROPIC_KEY_FILE:-}"
# make demo DEMO_ANTHROPIC_KEY_FILE=~/key.txt passes the ~ unexpanded from zsh.
# shellcheck disable=SC2088 # the literal ~/ is what is matched here
if [[ "$key_file" == "~/"* ]]; then key_file="$HOME/${key_file#\~/}"; fi
refuse() { echo "demo: $* Nothing was started." >&2; exit 1; }
case "$profile" in
  airgap)
    [[ -z "$key_file" ]] || refuse "DEMO_ANTHROPIC_KEY_FILE is set, but the model profile is airgap (the default), which uses no API key.
For the cloud model, run: make demo DEMO_MODEL=cloud DEMO_ANTHROPIC_KEY_FILE=$key_file
For the air-gapped model, unset DEMO_ANTHROPIC_KEY_FILE."
    ;;
  cloud)
    [[ -n "$key_file" ]] || refuse "DEMO_MODEL=cloud needs DEMO_ANTHROPIC_KEY_FILE=<file holding the Claude API key alone>."
    "$repo/hack/check-key-file.sh" "$key_file" || refuse "DEMO_ANTHROPIC_KEY_FILE=$key_file is not a key file (above)."
    ;;
  *) refuse "DEMO_MODEL=$profile: use airgap or cloud (airgap is the default)." ;;
esac
model_cache="${DEMO_MODEL_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/fleet-ops-copilot/models/qwen2.5-7b-instruct-q4_k_m}"
model_sums="$repo/config/airgap/model-SHA256SUMS"
# config/airgap/llama-server.yaml's image, pulled on this machine by digest (hack/airgap.mk).
llama_ref="ghcr.io/ggml-org/llama.cpp@sha256:b32890309c1a33a534a01b0b461e953dc2c5a6bac20213abcf48e22847a19c85"
# Docker VM memory the air-gapped profile needs (D103), and the slack for the VM
# reporting a little less than its setting.
airgap_mem_gib=12
mem_slack=$((256 * 1024 * 1024))

guard_outside_repo demo-up.sh "$dir" "$repo"
mkdir -p "$dir/logs"
chmod 700 "$dir"

started=$SECONDS
stage_name="" stage_start=$SECONDS timings=""
fmt() { printf '%dm%02ds' $(($1 / 60)) $(($1 % 60)); }
stage() {
  stage_done
  stage_name="$1" stage_start=$SECONDS
  echo
  echo "==> demo [$(fmt $((SECONDS - started)))] $1"
}
stage_done() {
  [[ -n "$stage_name" ]] || return 0
  timings+="  $(printf '%-34s' "$stage_name") $(fmt $((SECONDS - stage_start)))"$'\n'
  stage_name=""
}
fail() { echo "demo: $*" >&2; exit 1; }
# shellcheck disable=SC2154 # rc is assigned inside the single-quoted trap body
trap 'rc=$?; [[ $rc -eq 0 ]] || echo "demo: FAILED${stage_name:+ in stage \"$stage_name\"} after $(fmt $((SECONDS - started))) (exit $rc). Logs: $dir/logs. Start over with: make demo-down demo" >&2' EXIT

# x runs a command that changes something; with DEMO_DRY_RUN it only prints it.
x() {
  if [[ "$DRY" == 1 ]]; then
    case "$1" in
      k) shift; set -- kubectl --kubeconfig "$KUBECONFIG" --context "$ctx" "$@" ;;
      h) shift; set -- helm --kubeconfig "$KUBECONFIG" --kube-context "$ctx" "$@" ;;
    esac
    printf '  [dry-run] '; printf '%q ' "$@"; echo
  else
    "$@"
  fi
}
k() { kubectl --kubeconfig "$KUBECONFIG" --context "$ctx" "$@"; }
h() { helm --kubeconfig "$KUBECONFIG" --kube-context "$ctx" "$@"; }

# ---- 0. tools ----------------------------------------------------------------
stage "tools and Docker"
missing=""
for t in docker kind kubectl helm jq openssl curl go make python3; do
  command -v "$t" >/dev/null || missing+=" $t"
done
if [[ -n "$missing" ]]; then
  if [[ "$DRY" == 1 ]]; then
    echo "  [dry-run] missing tools (a real run stops here):$missing"
  else
    fail "missing tools:$missing"
  fi
fi
# D82: the quickstart's simulated adapter runs on this host with the first
# python3 (else python) on PATH, as its run.sh picks it, and imports grpc and
# google.protobuf. Check that interpreter now, before anything is created.
py="$(command -v python3 || command -v python || true)"
if [[ -n "$py" ]] && ! "$py" -c 'import grpc, google.protobuf' >/dev/null 2>&1; then
  pymsg="the Swarmada quickstart's simulated adapter runs with $py, which cannot import grpc and google.protobuf. Put a Python with grpcio and protobuf first on PATH, for example: python3 -m venv ~/fleetops-venv && ~/fleetops-venv/bin/pip install grpcio protobuf && export PATH=\"\$HOME/fleetops-venv/bin:\$PATH\"; then run make demo again"
  if [[ "$DRY" == 1 ]]; then
    echo "  [dry-run] python check (a real run stops here): $pymsg"
  else
    fail "$pymsg"
  fi
elif [[ -n "$py" ]]; then
  echo "  quickstart python: $py (imports grpc and google.protobuf)"
fi
if [[ "$DRY" != 1 ]]; then
  docker info >/dev/null 2>&1 || fail "the Docker daemon is not reachable"
  if [[ "$profile" == airgap ]]; then
    mem="$(docker info --format '{{.MemTotal}}' 2>/dev/null || true)"
    [[ "$mem" =~ ^[0-9]+$ ]] || fail "could not read the Docker VM's memory (docker info --format '{{.MemTotal}}')"
    if ((mem < airgap_mem_gib * 1024 * 1024 * 1024 - mem_slack)); then
      fail "Docker's VM has $(awk -v b="$mem" 'BEGIN { printf "%.1f", b / 1073741824 }') GiB of memory; the air-gapped profile needs at least $airgap_mem_gib GiB (llama-server alone may use 7 GiB). In Docker Desktop: Settings, Resources, Memory: $airgap_mem_gib GB or more, then Apply & restart. Or use the cloud model: make demo DEMO_MODEL=cloud DEMO_ANTHROPIC_KEY_FILE=<key file>. Nothing was started"
    fi
    echo "  Docker VM memory: $(awk -v b="$mem" 'BEGIN { printf "%.1f", b / 1073741824 }') GiB (the air-gapped profile needs $airgap_mem_gib GiB)"
  fi
  gowork="$(go env GOWORK)"
  [[ -z "$gowork" || "$gowork" == off ]] || fail "go env GOWORK is '$gowork': a go.work file would replace the Swarmada commit go.mod pins; unset GOWORK or remove the file"
fi
if [[ "$DRY" == 1 && "$profile" == airgap ]]; then
  echo "  [dry-run] Docker VM memory check (a real run stops here below $airgap_mem_gib GiB)"
fi
echo "  cluster $CLUSTER (context $ctx), kubeconfig $KUBECONFIG, scenario $scenario"
if [[ "$profile" == airgap ]]; then
  echo "  model profile: airgap (Qwen2.5-7B-Instruct on llama-server in the cluster, egress denied, no API key)"
else
  echo "  model profile: cloud (Claude Sonnet 5.5 through the Claude API, key from $key_file)"
fi

# ---- 0b. air-gapped: model files and llama.cpp image on this machine ---------------
if [[ "$profile" == airgap ]]; then
  stage "model files, llama.cpp image"
  echo "  Qwen2.5-7B-Instruct GGUF Q4_K_M, 2 files, 4,683,073,632 bytes (4.4 GiB). The first run downloads them"
  echo "  into $model_cache and checks each sha256 (config/airgap/model-SHA256SUMS); later runs reuse them."
  x "$repo/hack/fetch-model.sh" "$model_cache"
  if [[ "$DRY" == 1 ]] || ! docker image inspect "$llama_ref" >/dev/null 2>&1; then
    if [[ "$DRY" == 1 ]]; then arch="<go env GOARCH>"; else arch="$(go env GOARCH)"; fi
    x docker pull --platform "linux/$arch" "$llama_ref"
  else
    echo "  llama.cpp server image already here: $llama_ref"
  fi
fi

# ---- 1. people tokens, kind cluster ------------------------------------------
stage "people tokens (D60, test only)"
"$repo/hack/kind-people.sh" gen "$dir/people"

stage "kind cluster $CLUSTER"
sed "s|@PEOPLE_DIR@|$dir/people|" "$repo/hack/kind/demo-cluster.yaml.tmpl" >"$dir/kind-config.yaml"
echo "  kind configuration: $dir/kind-config.yaml"
exists=0
if [[ "$DRY" != 1 ]] && kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then exists=1; fi
if ((exists)); then
  # A cluster made with the other profile has its Secret or its egress policy; it is not switched in place.
  before="$(cat "$dir/profile" 2>/dev/null || echo cloud)"
  [[ "$before" == "$profile" ]] || fail "kind cluster $CLUSTER exists with the $before model profile; run make demo-down first, then make demo again"
  echo "  kind cluster $CLUSTER exists; reusing it (make demo-down first for a clean start)"
  [[ -f "$KUBECONFIG" ]] || kind export kubeconfig --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
else
  x kind create cluster --name "$CLUSTER" --image "$node_image" --config "$dir/kind-config.yaml" --kubeconfig "$KUBECONFIG" --wait 180s
fi
[[ "$DRY" == 1 ]] || { chmod 600 "$KUBECONFIG"; echo "$profile" >"$dir/profile"; }

stage "people: tokens accepted by the API server"
mkdir -p "$dir/persons"
for person in sam dana eve; do
  if [[ "$DRY" == 1 ]]; then
    x "$repo/hack/kind-people.sh" kubeconfig "$ctx" "$person" "$dir/people/tokens.csv" "$dir/persons/$person.kubeconfig"
    continue
  fi
  "$repo/hack/kind-people.sh" kubeconfig "$ctx" "$person" "$dir/people/tokens.csv" "$dir/persons/$person.kubeconfig" >/dev/null
  # kubectl auth whoami is a SelfSubjectReview, which every authenticated user may create.
  got="$(kubectl --kubeconfig "$dir/persons/$person.kubeconfig" --context "$person@$CLUSTER" auth whoami -o jsonpath='{.status.userInfo.username}' 2>/dev/null || true)"
  [[ "$got" == "$person" ]] || fail "the API server authenticated $person's token as '${got:-nobody}': the static token file is not in effect on cluster $CLUSTER (kind configuration: $dir/kind-config.yaml). A cluster created without it needs: make demo-down demo"
  echo "  $person's token authenticates as $got"
done

# ---- 2. Swarmada quickstart at the go.mod pin ---------------------------------
stage "Swarmada quickstart (go.mod pin)"
sw_mod=github.com/swarmada/swarmada
if [[ "$DRY" == 1 ]]; then
  sw_src="<module directory of $sw_mod at the go.mod pin>"
  sw_ver="<go.mod pin>"
else
  (cd "$repo" && GOWORK=off go mod download "$sw_mod")
  sw_src="$(cd "$repo" && GOWORK=off go list -m -f '{{.Dir}}' "$sw_mod")"
  sw_ver="$(cd "$repo" && GOWORK=off go list -m -f '{{.Version}}' "$sw_mod")"
  [[ -z "$(cd "$repo" && GOWORK=off go list -m -f '{{if .Replace}}replaced{{end}}' "$sw_mod")" ]] \
    || fail "go.mod replaces $sw_mod; make demo runs the quickstart at the pinned commit only"
  [[ -n "$sw_src" && -d "$sw_src" ]] || fail "cannot find the $sw_mod module in the module cache"
fi
sw="$dir/swarmada"
echo "  Swarmada $sw_ver -> $sw (a writable copy: the module cache is read-only and the quickstart builds in its tree)"
if [[ "$DRY" == 1 ]]; then
  # shellcheck disable=SC2016 # $1 and $2 are for the inner sh, not expanded here
  x sh -c 'tar -C "$1" -cf - . | tar -C "$2" -xf - && chmod -R u+w "$2"' copy-module "$sw_src" "$sw"
elif [[ "$(cat "$sw/.demo-version" 2>/dev/null)" != "$sw_ver" ]]; then
  # tar, not cp -R: the module cache is mode 0555 and tar sets directory modes last.
  chmod -R u+w "$sw" 2>/dev/null || true
  rm -rf "$sw"; mkdir -p "$sw"
  tar -C "$sw_src" -cf - . | tar -C "$sw" -xf -
  chmod -R u+w "$sw"
  echo "$sw_ver" >"$sw/.demo-version"
fi
# CI=true switches the quickstart's pacing off. It leaves the simulated adapter
# and a port-forward running in the background (pidfile /tmp/swq-$CLUSTER.pids);
# make demo-down stops them (run.sh --clean).
if [[ "$DRY" == 1 ]]; then
  x env QUICKSTART_CLUSTER="$CLUSTER" CI=true bash "$sw/examples/warehouse-quickstart/run.sh" --scenario "$scenario" --pace off
else
  (cd "$sw" && QUICKSTART_CLUSTER="$CLUSTER" CI=true bash examples/warehouse-quickstart/run.sh --scenario "$scenario" --pace off) \
    2>&1 | tee "$dir/logs/quickstart.log"
fi

# ---- 3. Swarmada's admission webhooks ------------------------------------------
stage "Swarmada admission webhooks"
x k apply -k "$sw/config/controlstream-tls" --server-side --force-conflicts
x k -n system rollout status deploy/swarmada-controller-manager --timeout=300s

stage "preflight: webhooks present and answering"
if [[ "$DRY" == 1 ]]; then
  x env KIND_CLUSTER="$CLUSTER" KUBECONFIG="$KUBECONFIG" "$repo/scripts/demo-preflight.sh"
else
  # The CA injector and the webhook server need a moment after the rollout.
  ok=0
  for _ in $(seq 36); do
    if KIND_CLUSTER="$CLUSTER" "$repo/scripts/demo-preflight.sh" >"$dir/logs/preflight.log" 2>&1; then ok=1; break; fi
    sleep 5
  done
  cat "$dir/logs/preflight.log"
  ((ok)) || fail "scripts/demo-preflight.sh still refuses after 3 minutes (output above)"
fi

# D59: the quickstart stopped its sim adapter on exit; keep all three robots live.
stage "Swarmada sim fleet: three live adapters"
x env DEMO_DIR="$dir" SWARMADA_NAMESPACE="$swarm_ns" "$repo/hack/demo-sim-fleet.sh" start

# ---- 4. image, chart -------------------------------------------------------------
stage "image $img"
x make -C "$repo" image IMG="$img"
x kind load docker-image "$img" --name "$CLUSTER"

stage "Helm chart"
if [[ "$profile" == cloud ]]; then
  if [[ "$DRY" == 1 ]]; then
    echo "  [dry-run] kubectl create secret generic fleet-ops-copilot-claude --from-file=ANTHROPIC_API_KEY=<key file, line break removed> -n $ns (applied)"
  else
    k create namespace "$ns" --dry-run=client -o yaml | k apply -f - >/dev/null
    # The key goes through a pipe, never a command line; a trailing line break would end up in the header.
    k -n "$ns" create secret generic fleet-ops-copilot-claude --from-file=ANTHROPIC_API_KEY=<(tr -d '\n' <"$key_file") \
      --dry-run=client -o yaml | k apply -f - >/dev/null
    echo "  Secret fleet-ops-copilot-claude created from $key_file"
  fi
fi
tracing_args=()
if [[ "${DEMO_TRACING:-0}" == 1 ]]; then tracing_args=(--set tracing.backend.enabled=true); fi
# CRDs first: Helm does not upgrade them, and a re-run should.
x k apply --server-side --force-conflicts -f "$repo/charts/fleet-ops-copilot/crds/"
x h upgrade --install fleetops "$repo/charts/fleet-ops-copilot" --namespace "$ns" --create-namespace \
  --set "swarmadaNamespaces={$swarm_ns}" --set "llm.profile=$profile" ${tracing_args[@]+"${tracing_args[@]}"}

# ---- 4b. air-gapped profile: llama-server, model files, egress policy -------------
if [[ "$profile" == airgap ]]; then
  stage "llama-server, egress policy"
  x make -C "$repo" load-llama-image KIND_CLUSTER="$CLUSTER"
  x make -C "$repo" load-llama-model KIND_CLUSTER="$CLUSTER" MODEL_DIR="$model_cache" MODEL_SUMS="$model_sums"
  x make -C "$repo" deploy-airgap KIND_CLUSTER="$CLUSTER"
fi

# ---- 5. people -------------------------------------------------------------------
stage "people: RoleBindings"
x "$repo/hack/kind-people.sh" bind "$ctx" "$swarm_ns"

# ---- 6. health -------------------------------------------------------------------
stage "health checks"
x k -n "$ns" rollout status deploy/fleetops-mcp deploy/fleetops-approvals --timeout=180s
if [[ "$DRY" == 1 ]]; then
  echo "  [dry-run] fleetops-mcp /healthz through the Service proxy; fleetops-agent rollout;"
  echo "  [dry-run] air-gapped: llama-server rollout and its NetworkPolicies present; tracing backend rollouts if DEMO_TRACING=1"
else
  k get --raw "/api/v1/namespaces/$ns/services/fleetops-mcp:mcp/proxy/healthz" >/dev/null \
    || fail "fleetops-mcp /healthz did not answer through the Service proxy"
  echo "  fleetops-mcp /healthz ok"
  k -n "$ns" rollout status deploy/fleetops-agent --timeout=180s
  if [[ "$profile" == airgap ]]; then
    k -n "$ns" rollout status deploy/llama-server --timeout=60s
    k -n "$ns" get networkpolicy fleetops-airgap-egress fleetops-airgap-egress-apiserver fleetops-airgap-egress-model \
      fleetops-airgap-egress-agent-mcp llama-server-no-egress llama-server-ingress -o name >/dev/null \
      || fail "the air-gapped NetworkPolicies are not all in $ns"
    echo "  air-gapped NetworkPolicies present in $ns (config/airgap/networkpolicy.yaml)"
  fi
  if [[ "${DEMO_TRACING:-0}" == 1 ]]; then
    k -n "$ns" rollout status deploy/fleetops-otel-collector deploy/fleetops-jaeger --timeout=180s
  fi
fi

# ---- done ------------------------------------------------------------------------
stage_done
total=$((SECONDS - started))
{
  echo "demo: $(date -u +%Y-%m-%dT%H:%M:%SZ) cluster $CLUSTER profile $profile total $(fmt $total) (${total}s) dry-run=$DRY"
  printf '%s' "$timings"
} >"$dir/last-run.txt"
echo
printf '%s' "$timings"
echo
if [[ "$DRY" == 1 ]]; then
  echo "demo: dry run finished in $(fmt $total). Nothing was created on a cluster."
else
  echo "demo: stack running on $ctx in $(fmt $total) (${total}s)."
fi
# What to run next, in order: the KUBECONFIG export, the verified way to ask,
# the web chat, then make demo-down. Command lines carry no comment, so each
# pastes into zsh as it is.
cat <<EOT

      export KUBECONFIG=$KUBECONFIG
  Run the line above first in every new terminal you use for this demo.
EOT
if [[ "$profile" == airgap ]]; then
  cat <<EOT

  Model: Qwen2.5-7B-Instruct (Q4_K_M) on llama-server in this cluster; fleetops-system's egress is denied
  by NetworkPolicy and no API key is used. On the v6 record this model passed 26/150 and Claude Sonnet 5.5
  120/150 (evals/results/comparison-v6.md); its answers are slower (p50 10.23 s, p95 124.78 s there) and more
  often wrong, and a wrong answer still cannot change the fleet: every change waits for a person's approval.
  For the cloud model instead: make demo-down, then make demo DEMO_MODEL=cloud DEMO_ANTHROPIC_KEY_FILE=<key file>
EOT
else
  cat <<EOT

  Model: Claude Sonnet 5.5 (claude-sonnet-5-5) through the Claude API, key in Secret fleet-ops-copilot-claude.
  Every question is a paid API call (v6 record: 1.5449 USD per 100 requests, evals/results/comparison-v6.md).
EOT
fi
cat <<EOT

  1. Ask as sam, verified: a request sam files can be approved by dana.
     In a second terminal, run these two lines; the second stays in the foreground:
      export KUBECONFIG=$KUBECONFIG
      kubectl --context $ctx -n $ns port-forward svc/fleetops-agent 8090:8090
     Then here, in $repo (bin/fleetops comes from make build):
      KUBECONFIG=$dir/persons/sam.kubeconfig bin/fleetops chat --context sam@$CLUSTER "Show me all robots"

  2. Or ask in the web chat, through the same port-forward: open the address below and type sam in "Your name".
     It has no login, so a request filed there is recorded for unverified:sam and can only be rejected.
      http://127.0.0.1:8090/

  People (test only, static tokens): $dir/persons/{sam,dana,eve}.kubeconfig, context <person>@$CLUSTER.
  Approval paths against this stack: make test-live.
  Sim fleet (3 adapters, background): PIDs in $dir/sim-fleet/pids, logs in $dir/logs/sim-adapter-*.log.
EOT
if [[ "$profile" == airgap ]]; then
  echo "  Egress check (a probe Job that must reach nothing outside the cluster): make verify-egress KIND_CLUSTER=$CLUSTER"
fi
if [[ "${DEMO_TRACING:-0}" == 1 ]]; then
  echo "  Traces: in another terminal, after the export line, run this and open http://127.0.0.1:16686/"
  echo "      kubectl --context $ctx -n $ns port-forward svc/fleetops-jaeger 16686:16686"
fi
cat <<EOT

  Remove the cluster, its kubeconfig, the people's tokens and their kept chat conversations (stop the port-forwards with Ctrl-C first):
      make demo-down
EOT
