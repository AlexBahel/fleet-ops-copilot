#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Runs test/live against a kind cluster that runs Swarmada v0.4 and the
# copilot (make foc-up, or make deploy-kind). The people are the fictional sam,
# dana and eve with bearer tokens from the static token file hack/kind-people.sh
# made (R43, test only): requests are filed with sam's or dana's own token, so
# the requester is verified (D57), and dana approves with her token's
# kubeconfig. The script binds the people's roles in warehouse-a
# (hack/kind-people.sh bind), port-forwards fleetops-mcp, and runs `make test-live`.
# With PHASE=webhooks it then copies Swarmada's safety audit log out of the
# manager pod (an ephemeral busybox container that mounts the manager's
# audit-log volume read-only, as the manager's user) and runs
# TestLiveSwarmadaAuditActor against the copy.
#
#   CLUSTER=foc-x KUBECONFIG=<file with context kind-foc-x> PHASE=webhooks|no-webhooks \
#   WORK=<dir for the person's kubeconfig> TOKENS_FILE=<people/tokens.csv> \
#   [EVIDENCE_DIR=<dir>] hack/live-approval-path.sh
#
# Refuses before doing anything unless CLUSTER matches ^foc-[a-z0-9-]+$ and KUBECONFIG
# names a dedicated file, never ~/.kube/config (hack/lib/foc-guard.sh; test:
# test/scripts/guard_test.go).
set -euo pipefail
# shellcheck source=lib/foc-guard.sh
source "$(dirname "$0")/lib/foc-guard.sh"
foc_guard live-approval-path.sh
: "${PHASE:?}" "${WORK:?}" "${TOKENS_FILE:?TOKENS_FILE must name the token file made by hack/kind-people.sh gen}"
ctx="kind-$CLUSTER"
repo="$(cd "$(dirname "$0")/.." && pwd)"
approver=dana   # fictional person, authenticates with a bearer token
mkdir -p "$WORK"
pk="$WORK/$approver.kubeconfig"
[[ -f "$pk" ]] || "$repo/hack/kind-people.sh" kubeconfig "$ctx" "$approver" "$TOKENS_FILE" "$pk" >/dev/null
"$repo/hack/kind-people.sh" bind "$ctx" warehouse-a >/dev/null
kubectl --context "$ctx" -n fleetops-system port-forward svc/fleetops-mcp 18080:8080 >"$WORK/port-forward.log" 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 30); do curl -sf http://127.0.0.1:18080/healthz >/dev/null && break; sleep 1; done
export KUBECONFIG="$KUBECONFIG:$pk"
export FOC_LIVE_ADMIN_CONTEXT="$ctx" FOC_LIVE_APPROVER_CONTEXT="$approver@$CLUSTER" FOC_LIVE_TOKENS_FILE="$TOKENS_FILE" \
  FOC_LIVE_MCP_URL=http://127.0.0.1:18080/mcp FOC_LIVE_FOC="$repo/bin/fleetops" FOC_LIVE_PHASE="$PHASE" \
  FOC_LIVE_EVIDENCE_DIR="${EVIDENCE_DIR:-}"
cd "$repo"
rc=0
make test-live || rc=$?
if [[ "$PHASE" == webhooks ]]; then
  pod="$(kubectl --context "$ctx" -n system get pods -o name | grep swarmada-controller-manager | head -1)"
  probe="fleetopsaudit-$(date +%s)"
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
