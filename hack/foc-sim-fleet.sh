#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# hack/foc-sim-fleet.sh start|stop — keeps the Swarmada quickstart's simulated
# fleet live on the foc-up stack (R73, BACKLOG-v4 P4).
#
# The quickstart stops its sim adapter when it exits, so every Robot goes
# Offline and nothing can be filed (the filing check, R65). start runs one
# adapters/simulation/sim_adapter.py per robot, as the quickstart's own
# all-live path does (one FleetAdapter identity per process: Swarmada keys a
# live stream by adapter name, config/overlays/demo-ab-live), in the
# background, with one port-forward to the ControlStream:
#
#   1. RobotClass simbot-v1, labelled as a demo workaround: the quickstart's
#      Robots name it but the quickstart does not ship it, and Swarmada
#      refuses every Robot spec update until it exists (docs/KNOWN-LIMITS.md);
#   2. the client certificates of sim-fleet-adapter-2 and -3 (the pinned
#      tree's demo-ab-live overlay) and FleetAdapters with sim-fleet-adapter's
#      spec;
#   3. the port-forward and three adapters; logs in $FOC_UP_DIR/logs, PIDs in
#      $FOC_UP_DIR/sim-fleet/pids; waits until every FleetAdapter is Connected;
#   4. points sim-robot-002/003 at their adapters (Swarmada's Robot webhook
#      admits only a Connected one) and waits until every Robot is live
#      (Idle, or InProgress on work Swarmada gave it).
#
# stop kills only the processes in the pidfile whose command line is still
# the one recorded (a reused PID is left alone), adapters first.
#
#   CLUSTER=foc-up KUBECONFIG=<dedicated file> FOC_UP_DIR=<dir> hack/foc-sim-fleet.sh start
#
# Environment (optional): SWARMADA_NAMESPACE (default warehouse-a),
# FOC_SIM_CS_PORT (local ControlStream port, default 19443),
# FOC_SIM_READY_TIMEOUT (seconds, default 180).
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/foc-guard.sh
source "$repo/hack/lib/foc-guard.sh"
foc_guard foc-sim-fleet.sh
export CLUSTER KUBECONFIG

cmd="${1:-}"
dir="${FOC_UP_DIR:-$HOME/.local/state/fleet-ops-copilot/foc-up}"
foc_outside_repo foc-sim-fleet.sh "$dir" "$repo"
ctx="kind-$CLUSTER"
ns="${SWARMADA_NAMESPACE:-warehouse-a}"
mgr=system
port="${FOC_SIM_CS_PORT:-19443}"
timeout="${FOC_SIM_READY_TIMEOUT:-180}"
sw="$dir/swarmada"
run="$dir/sim-fleet"
logs="$dir/logs"
pidfile="$run/pids"
robots=(sim-robot-001 sim-robot-002 sim-robot-003)
adapters=(sim-fleet-adapter sim-fleet-adapter-2 sim-fleet-adapter-3)
label="fleetops.alexbahel.github.io/demo-workaround"

k() { kubectl --kubeconfig "$KUBECONFIG" --context "$ctx" "$@"; }
say() { echo "  sim fleet: $*"; }
fail() { echo "foc-sim-fleet.sh: $*" >&2; exit 1; }

# ours PID PATTERN: the process is alive and still runs what we started.
ours() { local c; c="$(ps -p "$1" -o command= 2>/dev/null || true)"; [[ -n "$c" && "$c" == *"$2"* ]]; }

stop() {
  [[ -f "$pidfile" ]] || { say "not running (no $pidfile)"; return 0; }
  local -a lines=()
  local line pid pat i n=0
  while IFS= read -r line; do [[ -n "$line" ]] && lines+=("$line"); done <"$pidfile"
  # Reverse start order: the adapters before the port-forward they use.
  for ((i = ${#lines[@]} - 1; i >= 0; i--)); do
    pid="${lines[$i]%% *}" pat="${lines[$i]#* }"
    if ours "$pid" "$pat"; then
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 50); do ours "$pid" "$pat" || break; sleep 0.1; done
      if ours "$pid" "$pat"; then kill -9 "$pid" 2>/dev/null || true; fi
      n=$((n + 1))
    elif kill -0 "$pid" 2>/dev/null; then
      say "PID $pid no longer runs '$pat'; left alone"
    fi
  done
  rm -f "$pidfile"
  say "stopped $n process(es)"
}

# record PID PATTERN
record() { echo "$1 $2" >>"$pidfile"; }

robot_class() {
  k apply -f - >/dev/null <<EOF
apiVersion: swarmada.io/v1
kind: RobotClass
metadata:
  name: simbot-v1
  namespace: $ns
  labels: {$label: "true"}
  annotations:
    $label: "Swarmada's warehouse quickstart names RobotClass simbot-v1 on its Robots but does not ship it; foc-up creates it so that a Robot spec can be updated (docs/KNOWN-LIMITS.md)"
spec:
  manufacturer: Simulated
  model: SimBot-v1
  baseAdapter: {name: sim-fleet-adapter, version: "0.1.0"}
  hardware:
    - {name: camera_front, type: Camera, model: sim-cam-v1}
EOF
  say "RobotClass simbot-v1 (labelled $label)"
}

# client_tls ADAPTER INDEX: the adapter's client keypair, from cert-manager.
client_tls() {
  local a="$1" d="$run/tls-$(($2 + 1))" secret="$1-client-tls" waited=0
  until k -n "$mgr" get secret "$secret" >/dev/null 2>&1; do
    ((waited >= 120)) && fail "cert-manager never issued $secret"
    sleep 2; waited=$((waited + 2))
  done
  rm -rf "$d"; mkdir -p "$d"; chmod 700 "$d"
  for f in tls.crt tls.key ca.crt; do
    k -n "$mgr" get secret "$secret" -o jsonpath="{.data.${f//./\\.}}" | base64 -d >"$d/$f"
    [[ -s "$d/$f" ]] || fail "$secret has no $f"
  done
  chmod 600 "$d"/*
}

adapters() {
  local i
  k apply -f "$sw/config/overlays/demo-ab-live/adapter-client-cert-2.yaml" -f "$sw/config/overlays/demo-ab-live/adapter-client-cert-3.yaml" >/dev/null
  for i in "${!adapters[@]}"; do client_tls "${adapters[$i]}" "$i"; done
  say "client certificates of ${adapters[*]} in $run"
  for i in 1 2; do
    k -n "$ns" get fleetadapter sim-fleet-adapter -o json |
      jq --arg n "${adapters[$i]}" --arg l "$label" \
        '{apiVersion, kind, metadata: {name: $n, namespace: .metadata.namespace, labels: {($l): "true"}}, spec}' |
      k apply -f - >/dev/null
  done
  say "FleetAdapters ${adapters[1]}, ${adapters[2]} (sim-fleet-adapter's spec)"
}

# robots points sim-robot-002/003 at their own adapters. Swarmada's Robot
# webhook admits only a Connected adapter, so this runs after its process
# has registered.
robots() {
  local i
  for i in 1 2; do
    k -n "$ns" annotate robot "${robots[$i]}" "swarmada.io/robot-id=${robots[$i]}" --overwrite >/dev/null
    k -n "$ns" patch robot "${robots[$i]}" --type=json \
      -p "[{\"op\":\"replace\",\"path\":\"/spec/adapter/name\",\"value\":\"${adapters[$i]}\"}]" >/dev/null
  done
  say "${robots[1]}, ${robots[2]} point at ${adapters[1]}, ${adapters[2]}"
}

launch() {
  local py i pid pf_log="$logs/sim-fleet-port-forward.log"
  py="$(command -v python3 || command -v python)"
  nohup kubectl --kubeconfig "$KUBECONFIG" --context "$ctx" -n "$mgr" port-forward service/swarmada-controlstream "$port:9445" >"$pf_log" 2>&1 &
  pid=$!
  record "$pid" "port-forward service/swarmada-controlstream"
  for _ in $(seq 60); do grep -q "Forwarding from" "$pf_log" 2>/dev/null && break; sleep 0.5; done
  grep -q "Forwarding from" "$pf_log" 2>/dev/null || fail "the ControlStream port-forward did not start ($pf_log)"
  for i in "${!robots[@]}"; do
    (
      cd "$sw"
      exec nohup env PYTHONUNBUFFERED=1 PYTHONPATH=proto "$py" adapters/simulation/sim_adapter.py \
        --endpoint "127.0.0.1:$port" --namespace "$ns" --robot-id "${robots[$i]}" --zone warehouse-a \
        --vendor simulation --scenario healthy-fleet \
        --tls-ca "$run/tls-$((i + 1))/ca.crt" --tls-cert "$run/tls-$((i + 1))/tls.crt" --tls-key "$run/tls-$((i + 1))/tls.key" \
        --tls-server-name "swarmada-controlstream.$mgr.svc"
    ) >"$logs/sim-adapter-${robots[$i]}.log" 2>&1 &
    record "$!" "sim_adapter.py"
  done
  say "port-forward 127.0.0.1:$port and ${#robots[@]} adapters started; PIDs in $pidfile, logs in $logs"
}

# ready [adapters]: every FleetAdapter Connected and, unless "adapters", every
# Robot live: Idle, or InProgress on work Swarmada gave it (the quickstart's
# pending FleetActions go to a robot as soon as it is live).
ready() {
  local r a waited=0 pending
  while :; do
    pending=""
    if [[ "${1:-}" != adapters ]]; then
      for r in "${robots[@]}"; do
        case "$(k -n "$ns" get robot "$r" -o jsonpath='{.status.phase}' 2>/dev/null)" in
          Idle | InProgress) ;;
          *) pending+=" Robot/$r" ;;
        esac
      done
    fi
    for a in "${adapters[@]}"; do
      [[ "$(k -n "$ns" get fleetadapter "$a" -o jsonpath='{.status.phase}' 2>/dev/null)" == Connected ]] || pending+=" FleetAdapter/$a"
    done
    [[ -z "$pending" ]] && break
    ((waited >= timeout)) && fail "not live after ${timeout}s:$pending (logs: $logs/sim-adapter-*.log)"
    sleep 3; waited=$((waited + 3))
  done
  if [[ "${1:-}" == adapters ]]; then say "every FleetAdapter Connected"; else say "every Robot live (Idle or InProgress), every FleetAdapter Connected"; fi
}

case "$cmd" in
start)
  mkdir -p "$run" "$logs"; chmod 700 "$run"
  [[ -f "$sw/adapters/simulation/sim_adapter.py" ]] || fail "no quickstart tree at $sw (run make foc-up)"
  stop >/dev/null
  # A start that fails leaves nothing running.
  # shellcheck disable=SC2154 # rc is assigned inside the single-quoted trap body
  trap 'rc=$?; if [[ $rc -ne 0 ]]; then stop >/dev/null; fi' EXIT
  robot_class
  adapters
  launch
  ready adapters
  robots
  ready
  ;;
stop) stop ;;
*) echo "usage: hack/foc-sim-fleet.sh start|stop" >&2; exit 2 ;;
esac
