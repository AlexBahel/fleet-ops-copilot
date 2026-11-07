#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# make foc-up: from an empty machine state to a running stack on a kind
# cluster, in one command, and the elapsed time at the end.
#
#   1. a kind cluster foc-up whose API server has the static token file for
#      the fictional people sam, dana and eve (R43, test only);
#   2. Swarmada's warehouse quickstart at the commit go.mod pins;
#   3. Swarmada's admission webhooks turned on (config/controlstream-tls, the
#      overlay its `make deploy` applies; docs/RUNBOOK.md section 6);
#   4. scripts/foc-up-preflight.sh (webhooks present, with endpoints, answering);
#   4b. hack/foc-sim-fleet.sh start: the simulated fleet kept live (one sim
#      adapter per robot, in the background; RobotClass simbot-v1 as a
#      labelled demo workaround), every Robot live (R73);
#   5. the image built on the host and loaded into the cluster;
#   6. the Helm chart installed into foc-system;
#   7. the people's RoleBindings and token kubeconfigs;
#   8. health checks.
#
#   CLUSTER=foc-up KUBECONFIG=<dedicated file> hack/foc-up.sh        (make foc-up sets both)
#
# Cluster and kubeconfig rules are hack/lib/foc-guard.sh's: CLUSTER matches
# ^foc-[a-z0-9-]+$, KUBECONFIG is a dedicated file and never ~/.kube/config
# (the Swarmada quickstart runs `kubectl config use-context`, kind writes its
# context into KUBECONFIG). Every kubectl and helm call here names the
# kubeconfig and the context kind-$CLUSTER.
#
# Environment (all optional):
#   FOC_UP_DIR               working directory (default ~/.local/state/fleet-ops-copilot/foc-up, outside the repository: it holds tokens): people/ (tokens), persons/ (kubeconfigs),
#                            swarmada/ (writable copy of the pinned module), logs/, last-run.txt
#   FOC_UP_SCENARIO          Swarmada quickstart scenario (default healthy-fleet)
#   FOC_UP_NODE_IMAGE        kind node image (default: kind v0.33.0's own default, pinned by digest)
#   FOC_UP_IMAGE             copilot image (default fleet-ops-copilot:dev)
#   FOC_UP_TRACING=1         install the chart's optional Collector + Jaeger and export traces to it
#   FOC_UP_ANTHROPIC_KEY_FILE  file holding the Claude API key: becomes Secret fleet-ops-copilot-claude (not read otherwise;
#                            without it foc-agent does not start, and this script says so; nothing calls the API)
#   FOC_UP_DRY_RUN=1         print every command that would change anything, run none of them. Reads nothing
#                            from a cluster. Still creates FOC_UP_DIR with the tokens and the rendered kind
#                            configuration, and reports missing tools instead of stopping.
#
# Not run on a cluster when this was written (docs/KNOWN-LIMITS.md).
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/foc-guard.sh
source "$repo/hack/lib/foc-guard.sh"
foc_guard foc-up.sh
export CLUSTER KUBECONFIG

DRY="${FOC_UP_DRY_RUN:-0}"
dir="${FOC_UP_DIR:-$HOME/.local/state/fleet-ops-copilot/foc-up}"
scenario="${FOC_UP_SCENARIO:-healthy-fleet}"
# The default node image of kind v0.33.0 (pkg/apis/config/defaults/image.go).
node_image="${FOC_UP_NODE_IMAGE:-kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5}"
img="${FOC_UP_IMAGE:-fleet-ops-copilot:dev}"
ctx="kind-$CLUSTER"
ns=foc-system
swarm_ns="${SWARMADA_NAMESPACE:-warehouse-a}"
foc_outside_repo foc-up.sh "$dir" "$repo"
mkdir -p "$dir/logs"
chmod 700 "$dir"

started=$SECONDS
stage_name="" stage_start=$SECONDS timings=""
fmt() { printf '%dm%02ds' $(($1 / 60)) $(($1 % 60)); }
stage() {
  stage_done
  stage_name="$1" stage_start=$SECONDS
  echo
  echo "==> foc-up [$(fmt $((SECONDS - started)))] $1"
}
stage_done() {
  [[ -n "$stage_name" ]] || return 0
  timings+="  $(printf '%-34s' "$stage_name") $(fmt $((SECONDS - stage_start)))"$'\n'
  stage_name=""
}
fail() { echo "foc-up: $*" >&2; exit 1; }
# shellcheck disable=SC2154 # rc is assigned inside the single-quoted trap body
trap 'rc=$?; [[ $rc -eq 0 ]] || echo "foc-up: FAILED${stage_name:+ in stage \"$stage_name\"} after $(fmt $((SECONDS - started))) (exit $rc). Logs: $dir/logs. Start over with: make foc-down foc-up" >&2' EXIT

# x runs a command that changes something; with FOC_UP_DRY_RUN it only prints it.
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
# R59: the quickstart's simulated adapter runs on this host with the first
# python3 (else python) on PATH, as its run.sh picks it, and imports grpc and
# google.protobuf. Check that interpreter now, before anything is created.
py="$(command -v python3 || command -v python || true)"
if [[ -n "$py" ]] && ! "$py" -c 'import grpc, google.protobuf' >/dev/null 2>&1; then
  pymsg="the Swarmada quickstart's simulated adapter runs with $py, which cannot import grpc and google.protobuf. Put a Python with grpcio and protobuf first on PATH, for example: python3 -m venv ~/foc-venv && ~/foc-venv/bin/pip install grpcio protobuf && export PATH=\"\$HOME/foc-venv/bin:\$PATH\"; then run make foc-up again"
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
  gowork="$(go env GOWORK)"
  [[ -z "$gowork" || "$gowork" == off ]] || fail "go env GOWORK is '$gowork': a go.work file would replace the Swarmada commit go.mod pins; unset GOWORK or remove the file"
fi
echo "  cluster $CLUSTER (context $ctx), kubeconfig $KUBECONFIG, scenario $scenario"

# ---- 1. people tokens, kind cluster ------------------------------------------
stage "people tokens (R43, test only)"
"$repo/hack/kind-people.sh" gen "$dir/people"

stage "kind cluster $CLUSTER"
sed "s|@PEOPLE_DIR@|$dir/people|" "$repo/hack/kind/foc-cluster.yaml.tmpl" >"$dir/kind-config.yaml"
echo "  kind configuration: $dir/kind-config.yaml"
exists=0
if [[ "$DRY" != 1 ]] && kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then exists=1; fi
if ((exists)); then
  echo "  kind cluster $CLUSTER exists; reusing it (make foc-down first for a clean start)"
  [[ -f "$KUBECONFIG" ]] || kind export kubeconfig --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
else
  x kind create cluster --name "$CLUSTER" --image "$node_image" --config "$dir/kind-config.yaml" --kubeconfig "$KUBECONFIG" --wait 180s
fi
[[ "$DRY" == 1 ]] || chmod 600 "$KUBECONFIG"

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
  [[ "$got" == "$person" ]] || fail "the API server authenticated $person's token as '${got:-nobody}': the static token file is not in effect on cluster $CLUSTER (kind configuration: $dir/kind-config.yaml). A cluster created without it needs: make foc-down foc-up"
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
    || fail "go.mod replaces $sw_mod; foc-up runs the quickstart at the pinned commit only"
  [[ -n "$sw_src" && -d "$sw_src" ]] || fail "cannot find the $sw_mod module in the module cache"
fi
sw="$dir/swarmada"
echo "  Swarmada $sw_ver -> $sw (a writable copy: the module cache is read-only and the quickstart builds in its tree)"
if [[ "$DRY" == 1 ]]; then
  # shellcheck disable=SC2016 # $1 and $2 are for the inner sh, not expanded here
  x sh -c 'tar -C "$1" -cf - . | tar -C "$2" -xf - && chmod -R u+w "$2"' copy-module "$sw_src" "$sw"
elif [[ "$(cat "$sw/.foc-up-version" 2>/dev/null)" != "$sw_ver" ]]; then
  # tar, not cp -R: the module cache is mode 0555 and tar sets directory modes last.
  chmod -R u+w "$sw" 2>/dev/null || true
  rm -rf "$sw"; mkdir -p "$sw"
  tar -C "$sw_src" -cf - . | tar -C "$sw" -xf -
  chmod -R u+w "$sw"
  echo "$sw_ver" >"$sw/.foc-up-version"
fi
# CI=true switches the quickstart's pacing off. It leaves the simulated adapter
# and a port-forward running in the background (pidfile /tmp/swq-$CLUSTER.pids);
# make foc-down stops them (run.sh --clean).
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
  x env KIND_CLUSTER="$CLUSTER" KUBECONFIG="$KUBECONFIG" "$repo/scripts/foc-up-preflight.sh"
else
  # The CA injector and the webhook server need a moment after the rollout.
  ok=0
  for _ in $(seq 36); do
    if KIND_CLUSTER="$CLUSTER" "$repo/scripts/foc-up-preflight.sh" >"$dir/logs/preflight.log" 2>&1; then ok=1; break; fi
    sleep 5
  done
  cat "$dir/logs/preflight.log"
  ((ok)) || fail "scripts/foc-up-preflight.sh still refuses after 3 minutes (output above)"
fi

# R73: the quickstart stopped its sim adapter on exit; keep all three robots live.
stage "Swarmada sim fleet: three live adapters"
x env FOC_UP_DIR="$dir" SWARMADA_NAMESPACE="$swarm_ns" "$repo/hack/foc-sim-fleet.sh" start

# ---- 4. image, chart -------------------------------------------------------------
stage "image $img"
x make -C "$repo" image IMG="$img"
x kind load docker-image "$img" --name "$CLUSTER"

stage "Helm chart"
if [[ -n "${FOC_UP_ANTHROPIC_KEY_FILE:-}" ]]; then
  [[ "$DRY" == 1 || -s "$FOC_UP_ANTHROPIC_KEY_FILE" ]] || fail "FOC_UP_ANTHROPIC_KEY_FILE=$FOC_UP_ANTHROPIC_KEY_FILE is not a file"
  if [[ "$DRY" == 1 ]]; then
    echo "  [dry-run] kubectl create secret generic fleet-ops-copilot-claude --from-file=ANTHROPIC_API_KEY=<file> -n $ns (applied)"
  else
    k create namespace "$ns" --dry-run=client -o yaml | k apply -f - >/dev/null
    k -n "$ns" create secret generic fleet-ops-copilot-claude --from-file=ANTHROPIC_API_KEY="$FOC_UP_ANTHROPIC_KEY_FILE" \
      --dry-run=client -o yaml | k apply -f - >/dev/null
  fi
fi
tracing_args=()
if [[ "${FOC_UP_TRACING:-0}" == 1 ]]; then tracing_args=(--set tracing.backend.enabled=true); fi
# CRDs first: Helm does not upgrade them, and a re-run should.
x k apply --server-side --force-conflicts -f "$repo/charts/fleet-ops-copilot/crds/"
x h upgrade --install foc "$repo/charts/fleet-ops-copilot" --namespace "$ns" --create-namespace \
  --set "swarmadaNamespaces={$swarm_ns}" ${tracing_args[@]+"${tracing_args[@]}"}

# ---- 5. people -------------------------------------------------------------------
stage "people: RoleBindings"
x "$repo/hack/kind-people.sh" bind "$ctx" "$swarm_ns"

# ---- 6. health -------------------------------------------------------------------
stage "health checks"
x k -n "$ns" rollout status deploy/foc-mcp deploy/foc-approvals --timeout=180s
if [[ "$DRY" == 1 ]]; then
  echo "  [dry-run] foc-mcp /healthz through the Service proxy; foc-agent rollout if Secret fleet-ops-copilot-claude exists;"
  echo "  [dry-run] tracing backend rollouts if FOC_UP_TRACING=1"
else
  k get --raw "/api/v1/namespaces/$ns/services/foc-mcp:mcp/proxy/healthz" >/dev/null \
    || fail "foc-mcp /healthz did not answer through the Service proxy"
  echo "  foc-mcp /healthz ok"
  if k -n "$ns" get secret fleet-ops-copilot-claude >/dev/null 2>&1; then
    k -n "$ns" rollout status deploy/foc-agent --timeout=180s
  else
    echo "  foc-agent not waited for: Secret fleet-ops-copilot-claude (key ANTHROPIC_API_KEY) does not exist in $ns,"
    echo "  so the pod stays in CreateContainerConfigError. Create it (docs/RUNBOOK.md section 2) or rerun with FOC_UP_ANTHROPIC_KEY_FILE."
  fi
  if [[ "${FOC_UP_TRACING:-0}" == 1 ]]; then
    k -n "$ns" rollout status deploy/foc-otel-collector deploy/foc-jaeger --timeout=180s
  fi
fi

# ---- done ------------------------------------------------------------------------
stage_done
total=$((SECONDS - started))
{
  echo "foc-up: $(date -u +%Y-%m-%dT%H:%M:%SZ) cluster $CLUSTER total $(fmt $total) (${total}s) dry-run=$DRY"
  printf '%s' "$timings"
} >"$dir/last-run.txt"
echo
printf '%s' "$timings"
echo
if [[ "$DRY" == 1 ]]; then
  echo "foc-up: dry run finished in $(fmt $total). Nothing was created on a cluster."
else
  echo "foc-up: stack running on $ctx in $(fmt $total) (${total}s)."
fi
cat <<EOT

  export KUBECONFIG=$KUBECONFIG
  kubectl --context $ctx -n $ns get pods
  people (test only, static tokens): $dir/persons/{sam,dana,eve}.kubeconfig  (context <person>@$CLUSTER)
  file a verified request:  kubectl --context $ctx -n $ns port-forward svc/foc-agent 8090:8090
                            KUBECONFIG=$dir/persons/sam.kubeconfig bin/foc chat --context sam@$CLUSTER "..."
  verified-requester live run:  make foc-live
EOT
if [[ "${FOC_UP_TRACING:-0}" == 1 ]]; then
  echo "  traces:  kubectl --context $ctx -n $ns port-forward svc/foc-jaeger 16686:16686   (http://127.0.0.1:16686/)"
fi
echo "  sim fleet (3 adapters, background): PIDs $dir/sim-fleet/pids, logs $dir/logs/sim-adapter-*.log"
echo "  tear down:  make foc-down"
