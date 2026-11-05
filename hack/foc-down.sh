#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# make foc-down: removes what hack/foc-up.sh made: the Swarmada quickstart's
# background processes (its run.sh --clean, which also deletes the kind
# cluster), the kind cluster, the dedicated kubeconfig, and the people's tokens
# and kubeconfigs (the tokens belong to the cluster that is gone). Keeps the
# writable copy of the Swarmada module and the logs.
#
#   CLUSTER=foc-up KUBECONFIG=<dedicated file> hack/foc-down.sh
#
# Same guard as foc-up.sh: CLUSTER is foc-*, KUBECONFIG is never ~/.kube/config.
set -euo pipefail
repo="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/foc-guard.sh
source "$repo/hack/lib/foc-guard.sh"
foc_guard foc-down.sh
export CLUSTER KUBECONFIG
dir="${FOC_UP_DIR:-$HOME/.local/state/fleet-ops-copilot/foc-up}"
foc_outside_repo foc-down.sh "$dir" "$repo"

if [[ -f "$dir/swarmada/examples/warehouse-quickstart/run.sh" ]]; then
  (cd "$dir/swarmada" && QUICKSTART_CLUSTER="$CLUSTER" bash examples/warehouse-quickstart/run.sh --clean)
fi
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
fi
rm -rf "$dir/people" "$dir/persons" "$dir/kind-config.yaml" "$dir/last-run.txt" "$dir/live"
rm -f "$KUBECONFIG"
echo "foc-down: $CLUSTER removed"
