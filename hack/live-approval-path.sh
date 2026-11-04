#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Runs test/live against a kind cluster that runs Swarmada v0.4 and the
# copilot (make deploy-kind). Creates a fictional approver person, binds the
# approver role and swarmada:fleet-manager to them in warehouse-a,
# port-forwards foc-mcp, and runs `make test-live`. With PHASE=webhooks it then
# copies Swarmada's safety audit log out of the manager pod (an ephemeral
# busybox container that mounts the manager's audit-log volume read-only, as
# the manager's user) and runs TestLiveSwarmadaAuditActor against the copy.
#
#   CLUSTER=foc-x KUBECONFIG=<file with context kind-foc-x> PHASE=webhooks|no-webhooks \
#   WORK=<dir for the person's kubeconfig> [EVIDENCE_DIR=<dir>] hack/live-approval-path.sh
#
# Refuses before doing anything unless CLUSTER matches ^foc-[a-z0-9-]+$ and KUBECONFIG
# names a dedicated file, never ~/.kube/config (hack/lib/foc-guard.sh; test:
# test/scripts/guard_test.go).
set -euo pipefail
# shellcheck source=lib/foc-guard.sh
source "$(dirname "$0")/lib/foc-guard.sh"
foc_guard live-approval-path.sh
: "${PHASE:?}" "${WORK:?}"
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
cd "$repo"
rc=0
make test-live || rc=$?
if [[ "$PHASE" == webhooks ]]; then
  pod="$(kubectl --context "$ctx" -n system get pods -o name | grep swarmada-controller-manager | head -1)"
  probe="focaudit-$(date +%s)"
  custom="$WORK/$probe.json"
  echo '{"securityContext":{"runAsUser":65532,"runAsNonRoot":true,"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}},"volumeMounts":[{"name":"audit-log","mountPath":"/audit","readOnly":true}]}' >"$custom"
  kubectl --context "$ctx" -n system debug "$pod" --image=busybox:1.36 --custom="$custom" -c "$probe" -- sleep 600 >/dev/null
  for _ in $(seq 60); do
    kubectl --context "$ctx" -n system exec "$pod" -c "$probe" -- true >/dev/null 2>&1 && break; sleep 2
  done
  audit="${EVIDENCE_DIR:-$WORK}/swarmada-safety-audit.jsonl"
  kubectl --context "$ctx" -n system exec "$pod" -c "$probe" -- cat /audit/audit.jsonl >"$audit"
  FOC_LIVE_SWARMADA_AUDIT_FILE="$audit" go test -tags live -count=1 -v -run TestLiveSwarmadaAuditActor ./test/live/... || rc=$?
fi
exit $rc
