#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# hack/demo-sim-fleet.sh start|stop — keeps the Swarmada quickstart's simulated
# fleet live on the demo stack (D59).
#
# The quickstart stops its sim adapter when it exits, so every Robot goes
# Offline and nothing can be filed (the filing check, D73). start runs one
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
#   3. the port-forward, then for each robot: the Robot is
#      pointed at its adapter before the adapter that stays is started, so
#      its first register is authorized (Swarmada refuses a register from an
#      adapter the Robot does not name, and the sim adapter does not retry a
#      refused one). Swarmada's Robot webhook admits only a Connected adapter,
#      so for a Robot that names another one a short-lived connect process of
#      its adapter runs first, the Robot is pointed, and that process is
#      stopped. Logs in $DEMO_DIR/logs, PIDs in $DEMO_DIR/sim-fleet/pids;
#   4. waits until every robot is registered and ready for work: its Robot
#      names its adapter, the adapter is Connected with a heartbeat from the
#      process that stays, the adapter's log holds no refused register, and
#      the Robot is live (Idle, or InProgress on work Swarmada gave it). A
#      robot that is not, within the timeout, fails start with its name.
#
# stop kills only the processes in the pidfile whose command line is still
# the one recorded (a reused PID is left alone), adapters first.
#
#   CLUSTER=fleetops-demo KUBECONFIG=<dedicated file> DEMO_DIR=<dir> hack/demo-sim-fleet.sh start
#
# Environment (optional): SWARMADA_NAMESPACE (default warehouse-a),
# FLEETOPS_SIM_CS_PORT (local ControlStream port, default 19443),
# FLEETOPS_SIM_READY_TIMEOUT (seconds, default 180), FLEETOPS_SIM_REGISTER_GRACE
# (seconds an adapter runs before its log is read for a refused register,
# default 5).
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/guard.sh
source "$repo/hack/lib/guard.sh"
guard_cluster demo-sim-fleet.sh
export CLUSTER KUBECONFIG

cmd="${1:-}"
dir="${DEMO_DIR:-$HOME/.local/state/fleet-ops-copilot/demo}"
guard_outside_repo demo-sim-fleet.sh "$dir" "$repo"
ctx="kind-$CLUSTER"
ns="${SWARMADA_NAMESPACE:-warehouse-a}"
mgr=system
port="${FLEETOPS_SIM_CS_PORT:-19443}"
timeout="${FLEETOPS_SIM_READY_TIMEOUT:-180}"
grace="${FLEETOPS_SIM_REGISTER_GRACE:-5}"
sw="$dir/swarmada"
run="$dir/sim-fleet"
logs="$dir/logs"
pidfile="$run/pids"
robots=(sim-robot-001 sim-robot-002 sim-robot-003)
adapters=(sim-fleet-adapter sim-fleet-adapter-2 sim-fleet-adapter-3)
label="fleetops.alexbahel.github.io/demo-workaround"
py="" proc=""
started=() # per robot: when the adapter that stays was started (epoch seconds)

k() { kubectl --kubeconfig "$KUBECONFIG" --context "$ctx" "$@"; }
say() { echo "  sim fleet: $*"; }
fail() { echo "demo-sim-fleet.sh: $*" >&2; exit 1; }

# ours PID PATTERN: the process is alive and still runs what we started.
ours() { local c; c="$(ps -p "$1" -o command= 2>/dev/null || true)"; [[ -n "$c" && "$c" == *"$2"* ]]; }

# end PID PATTERN: TERM, then KILL after 5 s.
end() {
  kill "$1" 2>/dev/null || true
  for _ in $(seq 50); do ours "$1" "$2" || break; sleep 0.1; done
  if ours "$1" "$2"; then kill -9 "$1" 2>/dev/null || true; fi
}

stop() {
  [[ -f "$pidfile" ]] || { say "not running (no $pidfile)"; return 0; }
  local -a lines=()
  local line pid pat i n=0
  while IFS= read -r line; do [[ -n "$line" ]] && lines+=("$line"); done <"$pidfile"
  # Reverse start order: the adapters before the port-forward they use.
  for ((i = ${#lines[@]} - 1; i >= 0; i--)); do
    pid="${lines[$i]%% *}" pat="${lines[$i]#* }"
    if ours "$pid" "$pat"; then
      end "$pid" "$pat"
      n=$((n + 1))
    elif kill -0 "$pid" 2>/dev/null; then
      say "PID $pid no longer runs '$pat'; left alone"
    fi
  done
  rm -f "$pidfile"
  say "stopped $n process(es)"
}

# record PID PATTERN; unrecord PID drops its line.
record() { echo "$1 $2" >>"$pidfile"; }
unrecord() {
  local keep
  keep="$(grep -v "^$1 " "$pidfile" || true)"
  if [[ -n "$keep" ]]; then printf '%s\n' "$keep" >"$pidfile"; else : >"$pidfile"; fi
}

robot_class() {
  k apply -f - >/dev/null <<EOF
apiVersion: swarmada.io/v1
kind: RobotClass
metadata:
  name: simbot-v1
  namespace: $ns
  labels: {$label: "true"}
  annotations:
    $label: "Swarmada's warehouse quickstart names RobotClass simbot-v1 on its Robots but does not ship it; make demo creates it so that a Robot spec can be updated (docs/KNOWN-LIMITS.md)"
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

# robot_field ROBOT JSONPATH, adapter_field ADAPTER JSONPATH
robot_field() { k -n "$ns" get robot "$1" -o jsonpath="$2" 2>/dev/null || true; }
adapter_field() { k -n "$ns" get fleetadapter "$1" -o jsonpath="$2" 2>/dev/null || true; }

# adapter_proc INDEX LOG: one sim_adapter.py for robot INDEX in the background,
# recorded in the pidfile; its PID in $proc.
adapter_proc() {
  local i="$1"
  (
    cd "$sw"
    exec nohup env PYTHONUNBUFFERED=1 PYTHONPATH=proto "$py" adapters/simulation/sim_adapter.py \
      --endpoint "127.0.0.1:$port" --namespace "$ns" --robot-id "${robots[$i]}" --zone warehouse-a \
      --vendor simulation --scenario healthy-fleet \
      --tls-ca "$run/tls-$((i + 1))/ca.crt" --tls-cert "$run/tls-$((i + 1))/tls.crt" --tls-key "$run/tls-$((i + 1))/tls.key" \
      --tls-server-name "swarmada-controlstream.$mgr.svc"
  ) >"$2" 2>&1 &
  proc=$!
  disown "$proc" 2>/dev/null || true # no "Terminated" line when a connect process is stopped
  record "$proc" "sim_adapter.py"
}

# point INDEX: the Robot names its adapter before the adapter that stays
# starts. Swarmada's Robot webhook admits only a Connected adapter, so a
# connect process of the adapter runs while the Robot is pointed, then stops.
point() {
  local i="$1" r="${robots[$1]}" a="${adapters[$1]}" waited=0
  [[ "$(robot_field "$r" '{.spec.adapter.name}')" == "$a" ]] && return 0
  adapter_proc "$i" "$logs/sim-adapter-$r-connect.log"
  until [[ "$(adapter_field "$a" '{.status.phase}')" == Connected ]]; do
    ((waited >= timeout)) && fail "$r: FleetAdapter $a not Connected after ${timeout}s, so $r cannot be pointed at it (log: $logs/sim-adapter-$r-connect.log)"
    sleep 1; waited=$((waited + 1))
  done
  k -n "$ns" annotate robot "$r" "swarmada.io/robot-id=$r" --overwrite >/dev/null
  k -n "$ns" patch robot "$r" --type=json \
    -p "[{\"op\":\"replace\",\"path\":\"/spec/adapter/name\",\"value\":\"$a\"}]" >/dev/null
  end "$proc" "sim_adapter.py"
  unrecord "$proc"
  say "$r points at $a"
}

launch() {
  local i pid pf_log="$logs/sim-fleet-port-forward.log"
  py="$(command -v python3 || command -v python)"
  nohup kubectl --kubeconfig "$KUBECONFIG" --context "$ctx" -n "$mgr" port-forward service/swarmada-controlstream "$port:9445" >"$pf_log" 2>&1 &
  pid=$!
  record "$pid" "port-forward service/swarmada-controlstream"
  for _ in $(seq 60); do grep -q "Forwarding from" "$pf_log" 2>/dev/null && break; sleep 0.5; done
  grep -q "Forwarding from" "$pf_log" 2>/dev/null || fail "the ControlStream port-forward did not start ($pf_log)"
  for i in "${!robots[@]}"; do
    point "$i"
    # Two seconds apart, so no heartbeat from a connect process that just
    # stopped counts as one from the adapter that stays (one-second stamps).
    sleep 2
    started[i]="$(date -u +%s)"
    adapter_proc "$i" "$logs/sim-adapter-${robots[$i]}.log"
  done
  say "port-forward 127.0.0.1:$port and ${#robots[@]} adapters started; PIDs in $pidfile, logs in $logs"
}

# epoch RFC3339: seconds since 1970 (GNU or BSD date).
epoch() { date -u -d "$1" +%s 2>/dev/null || date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$1" +%s 2>/dev/null || echo 0; }

# not_ready INDEX: empty when the robot is registered and ready for work,
# otherwise why not.
not_ready() {
  local i="$1" r="${robots[$1]}" a="${adapters[$1]}" log="$logs/sim-adapter-${robots[$1]}.log" hb phase line
  [[ "$(robot_field "$r" '{.spec.adapter.name}')" == "$a" ]] || { echo "Robot does not name $a"; return; }
  [[ "$(adapter_field "$a" '{.status.phase}')" == Connected ]] || { echo "FleetAdapter $a not Connected"; return; }
  hb="$(adapter_field "$a" '{.status.lastHeartbeat}')"
  (($(epoch "$hb") + 1 >= started[i])) || { echo "no heartbeat on $a since its adapter started"; return; }
  line="$(grep -m1 "register refused\|NOT_ADMITTED" "$log" 2>/dev/null || true)"
  [[ -z "$line" ]] || { echo "register refused: ${line#*: }"; return; }
  (($(date -u +%s) - started[i] >= grace)) || { echo "adapter started less than ${grace}s ago"; return; }
  phase="$(robot_field "$r" '{.status.phase}')"
  case "$phase" in Idle | InProgress) ;; *) echo "Robot phase ${phase:-<none>}" ;; esac
}

# ready: every robot registered and ready for work, or fail naming the robot.
ready() {
  local i why waited=0 pending
  while :; do
    pending=""
    for i in "${!robots[@]}"; do
      why="$(not_ready "$i")"
      if [[ -n "$why" ]]; then
        pending+="; ${robots[$i]}: $why"
        # A refused register is final: the sim adapter does not retry it.
        [[ "$why" == "register refused"* ]] && fail "${robots[$i]} is not registered: Swarmada refused its adapter's register (${why#register refused: }), and the sim adapter does not retry it (log: $logs/sim-adapter-${robots[$i]}.log)"
      fi
    done
    [[ -z "$pending" ]] && break
    ((waited >= timeout)) && fail "not registered and ready for work after ${timeout}s${pending} (logs: $logs/sim-adapter-*.log)"
    sleep 1; waited=$((waited + 1))
  done
  say "every robot registered and ready for work: its Robot names its adapter, the adapter is Connected, no register refused, the Robot Idle or InProgress"
}

case "$cmd" in
start)
  mkdir -p "$run" "$logs"; chmod 700 "$run"
  [[ -f "$sw/adapters/simulation/sim_adapter.py" ]] || fail "no quickstart tree at $sw (run make demo)"
  stop >/dev/null
  # A start that fails leaves nothing running.
  # shellcheck disable=SC2154 # rc is assigned inside the single-quoted trap body
  trap 'rc=$?; if [[ $rc -ne 0 ]]; then stop >/dev/null; fi' EXIT
  robot_class
  adapters
  launch
  ready
  ;;
stop) stop ;;
*) echo "usage: hack/demo-sim-fleet.sh start|stop" >&2; exit 2 ;;
esac
