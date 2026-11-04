#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Runs test/live against a kind cluster that runs Swarmada v0.4 and the
# copilot (make deploy-kind). Creates a fictional approver person, binds the
# approver role and swarmada:fleet-manager to them in warehouse-a,
# port-forwards foc-mcp, and runs `make test-live`.
#
#   CLUSTER=foc-x KUBECONFIG=<file with context kind-foc-x> PHASE=webhooks|no-webhooks \
#   WORK=<dir for the person's kubeconfig> [EVIDENCE_DIR=<dir>] hack/live-approval-path.sh
set -euo pipefail
: "${CLUSTER:?}" "${KUBECONFIG:?}" "${PHASE:?}" "${WORK:?}"
[[ "$CLUSTER" == foc-* ]] || { echo "CLUSTER must start with foc-" >&2; exit 1; }
ctx="kind-$CLUSTER"
repo="$(cd "$(dirname "$0")/.." && pwd)"
approver="dana@example-distribution.test"   # fictional person
mkdir -p "$WORK"
pk="$WORK/approver.kubeconfig"
[[ -f "$pk" ]] || "$repo/hack/kind-person.sh" "$ctx" "$approver" "example-distribution:fleet-engineers" "$pk" >/dev/null
for role in fleetops:approver swarmada:fleet-manager; do
  kubectl --context "$ctx" -n warehouse-a create rolebinding "person-dana-${role//:/-}" \
    --clusterrole="$role" --user="$approver" --dry-run=client -o yaml | kubectl --context "$ctx" apply -f - >/dev/null
done
kubectl --context "$ctx" -n foc-system port-forward svc/foc-mcp 18080:8080 >"$WORK/port-forward.log" 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 30); do curl -sf http://127.0.0.1:18080/healthz >/dev/null && break; sleep 1; done
export KUBECONFIG="$KUBECONFIG:$pk"
export FOC_LIVE_ADMIN_CONTEXT="$ctx" FOC_LIVE_APPROVER_CONTEXT="$approver@$CLUSTER" \
  FOC_LIVE_MCP_URL=http://127.0.0.1:18080/mcp FOC_LIVE_FOC="$repo/bin/foc" FOC_LIVE_PHASE="$PHASE" \
  FOC_LIVE_EVIDENCE_DIR="${EVIDENCE_DIR:-}"
cd "$repo" && make test-live
