#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# make demo-down: removes what hack/demo-up.sh made: the Swarmada quickstart's
# background processes (its run.sh --clean, which also deletes the kind
# cluster), the kind cluster, the dedicated kubeconfig, and the people's tokens
# and kubeconfigs (the tokens belong to the cluster that is gone), and the
# conversations fleetops chat kept for the demo's contexts (D75). Keeps the
# writable copy of the Swarmada module and the logs.
#
#   CLUSTER=fleetops-demo KUBECONFIG=<dedicated file> hack/demo-down.sh
#
# Same guard as demo-up.sh: CLUSTER is fleetops-*, KUBECONFIG is never ~/.kube/config.
set -euo pipefail
repo="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/guard.sh
source "$repo/hack/lib/guard.sh"
guard_cluster demo-down.sh
export CLUSTER KUBECONFIG
dir="${DEMO_DIR:-$HOME/.local/state/fleet-ops-copilot/demo}"
guard_outside_repo demo-down.sh "$dir" "$repo"

# D59: the sim fleet's adapters and port-forward first.
DEMO_DIR="$dir" "$repo/hack/demo-sim-fleet.sh" stop
if [[ -f "$dir/swarmada/examples/warehouse-quickstart/run.sh" ]]; then
  (cd "$dir/swarmada" && QUICKSTART_CLUSTER="$CLUSTER" bash examples/warehouse-quickstart/run.sh --clean)
fi
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
fi
rm -rf "$dir/people" "$dir/persons" "$dir/kind-config.yaml" "$dir/last-run.txt" "$dir/live" "$dir/sim-fleet" "$dir/profile"
rm -f "$KUBECONFIG"
# fleetops chat keeps a conversation id per context in its state directory
# (cmd/fleetops/chat.go); the next demo's fleetops-agent never issued them.
# Only the demo's own contexts: other contexts and files there stay.
conv="${XDG_STATE_HOME:-$HOME/.local/state}/fleetops/conversations"
for c in "sam@$CLUSTER" "dana@$CLUSTER" "eve@$CLUSTER" "kind-$CLUSTER"; do
  rm -rf "${conv:?}/ctx-$c"
done
echo "demo-down: $CLUSTER removed"
