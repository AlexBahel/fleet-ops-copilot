#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Preflight for `make demo`, run after Swarmada is up and before the chart is
# installed. Exits 0 only if every check passes; otherwise prints what failed
# and exits 1. Read-only apart from one server-side dry run, which the API
# server does not persist.
#
#   KIND_CLUSTER=fleetops-<name> KUBECONFIG=<dedicated file> scripts/demo-preflight.sh
#
# Cluster: only kind cluster KIND_CLUSTER (fleetops-*), only through context
# kind-$KIND_CLUSTER, only from a KUBECONFIG file that holds fleetops-* contexts and
# nothing else. Every kubectl call names --kubeconfig and --context; the
# current context of any kubeconfig is never read or changed. Names of
# contexts that are not fleetops-* are counted, never printed.
#
# Swarmada's admission webhooks: the Swarmada quickstart serves none
# (docs/DESIGN.md D32), and without the FleetTask webhook a draft is not an
# authorization boundary (D22). This script requires
#   1. a fail-closed validating webhook covering UPDATE of fleettasks.swarmada.io
#      (the same test the copilot's draft gate applies, internal/fleet DraftGate),
#      a validating webhook for fleetzones and zonemaintenances, and the
#      mutating webhook for fleetzones (estop actor stamp, D9/D33);
#   2. for every webhook that covers swarmada.io: a CA bundle, and at least one
#      ready endpoint behind its Service;
#   3. an answer from the FleetTask webhook: a server-side dry run of a held
#      draft must be admitted or denied by a swarmada.io webhook. "failed
#      calling webhook" or any other error is a refusal.
# docs/RUNBOOK.md, "Enable Swarmada's admission webhooks", shows how to turn
# them on.
#
# Optional: SWARMADA_NAMESPACE (default warehouse-a) for the dry run.
set -euo pipefail

fail() { echo "preflight: REFUSED: $*" >&2; exit 1; }
ok() { echo "preflight: ok: $*"; }

for bin in kubectl kind jq; do
  command -v "$bin" >/dev/null || fail "$bin not found on PATH"
done

# ---- cluster and kubeconfig --------------------------------------------------
: "${KIND_CLUSTER:=}"
[[ "$KIND_CLUSTER" =~ ^fleetops-[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] \
  || fail "KIND_CLUSTER must be set to a kind cluster name starting with fleetops- (got '${KIND_CLUSTER}')"
ctx="kind-$KIND_CLUSTER"

: "${KUBECONFIG:=}"
[[ -n "$KUBECONFIG" ]] || fail "KUBECONFIG must name a dedicated kubeconfig file for $ctx (for example from: kind get kubeconfig --name $KIND_CLUSTER)"
[[ "$KUBECONFIG" != *:* ]] || fail "KUBECONFIG must be one file, not a list"
[[ -f "$KUBECONFIG" ]] || fail "KUBECONFIG=$KUBECONFIG is not a file"
kc="$(cd "$(dirname "$KUBECONFIG")" && pwd -P)/$(basename "$KUBECONFIG")"
[[ ! "$kc" -ef "$HOME/.kube/config" ]] || fail "KUBECONFIG is the default ~/.kube/config; use a dedicated file"
[[ -z "$(find "$kc" -perm -004 2>/dev/null)" ]] || fail "KUBECONFIG=$kc is readable by every user; chmod 600 it"
ok "dedicated kubeconfig $kc"

k() { kubectl --kubeconfig "$kc" --context "$ctx" "$@"; }

contexts="$(kubectl --kubeconfig "$kc" config view -o jsonpath='{range .contexts[*]}{.name}{"\n"}{end}')"
others="$(printf '%s\n' "$contexts" | grep -cv '^kind-fleetops-' || true)"
[[ -z "$contexts" ]] && others=0
[[ "$others" -eq 0 ]] || fail "KUBECONFIG holds $others context(s) not named kind-fleetops-*; use a file with fleetops-* contexts only"
printf '%s\n' "$contexts" | grep -qx "$ctx" || fail "KUBECONFIG has no context $ctx"
ok "every context in the file is kind-fleetops-*; $ctx present"

server="$(kubectl --kubeconfig "$kc" config view --minify --context "$ctx" -o jsonpath='{.clusters[0].cluster.server}')"
[[ "$server" =~ ^https://(127\.0\.0\.1|localhost|\[::1\]):[0-9]+$ ]] \
  || fail "context $ctx points at $server, not a local kind API server"
kind_server="$(kind get kubeconfig --name "$KIND_CLUSTER" 2>/dev/null | awk '$1 == "server:" {print $2; exit}')" \
  || true
[[ -n "$kind_server" ]] || fail "kind has no cluster named $KIND_CLUSTER"
[[ "$kind_server" == "$server" ]] || fail "context $ctx points at $server, but kind cluster $KIND_CLUSTER serves $kind_server"
k get --raw /readyz >/dev/null 2>&1 || fail "API server of $KIND_CLUSTER at $server is not ready"
ok "$ctx is kind cluster $KIND_CLUSTER at $server, ready"

# ---- Swarmada ------------------------------------------------------------------
for crd in fleettasks fleetzones zonemaintenances robots; do
  k get crd "$crd.swarmada.io" >/dev/null 2>&1 || fail "CRD $crd.swarmada.io not installed: Swarmada is not on $KIND_CLUSTER"
done
ok "Swarmada CRDs installed"

vwc="$(k get validatingwebhookconfigurations -o json)"
mwc="$(k get mutatingwebhookconfigurations -o json)"

# covers KIND WEBHOOKS_JSON RESOURCE OPERATION [fail-closed-only]
# Prints configuration/webhook names that cover RESOURCE.swarmada.io for OPERATION.
covers() {
  jq -r --arg res "$2" --arg op "$3" --arg fc "${4:-}" '
    .items[] | .metadata.name as $cfg | (.webhooks // [])[]
    | select($fc == "" or (.failurePolicy // "Fail") == "Fail")
    | select(any(.rules[]?;
        ((.apiGroups // []) | index("swarmada.io") or index("*")) and
        ((.resources // []) | index($res) or index("*")) and
        ((.operations // []) | index($op) or index("*"))))
    | "\($cfg)/\(.name)"' <<<"$1"
}
need() { # need DESCRIPTION LIST
  [[ -n "$2" ]] || fail "no $1. Swarmada's admission webhooks are not installed; see docs/RUNBOOK.md, \"Enable Swarmada's admission webhooks\""
  ok "$1: $(tr '\n' ' ' <<<"$2")"
}
need "fail-closed validating webhook for UPDATE fleettasks.swarmada.io (draft gate)" "$(covers "$vwc" fleettasks UPDATE fail-closed)"
need "validating webhook for UPDATE fleetzones.swarmada.io" "$(covers "$vwc" fleetzones UPDATE)"
need "validating webhook for CREATE zonemaintenances.swarmada.io" "$(covers "$vwc" zonemaintenances CREATE)"
need "mutating webhook for UPDATE fleetzones.swarmada.io (estop actor)" "$(covers "$mwc" fleetzones UPDATE)"

# Every swarmada.io webhook: CA bundle and a ready endpoint behind its Service.
targets="$(jq -s -r '
  [.[].items[] | .metadata.name as $cfg | (.webhooks // [])[]
   | select(any(.rules[]?; (.apiGroups // []) | index("swarmada.io")))
   | {w: "\($cfg)/\(.name)", ca: (.clientConfig.caBundle // ""),
      svc: (if .clientConfig.service then "\(.clientConfig.service.namespace)/\(.clientConfig.service.name)" else "" end),
      url: (.clientConfig.url // "")}]
  | .[] | [.w, (if .ca == "" then "no-ca" else "ca" end), .svc, .url] | @tsv' <<<"$vwc"$'\n'"$mwc")"
services=""
while IFS=$'\t' read -r w ca svc url; do
  [[ -n "$w" ]] || continue
  [[ "$ca" == ca ]] || fail "webhook $w has no caBundle: the API server cannot call it (serving certificate not injected)"
  if [[ -n "$svc" ]]; then services+="$svc"$'\n'; else ok "webhook $w calls URL $url (not checked for endpoints)"; fi
done <<<"$targets"
while read -r svc; do
  [[ -n "$svc" ]] || continue
  ns="${svc%%/*}" name="${svc#*/}"
  ready="$(k -n "$ns" get endpointslices -l "kubernetes.io/service-name=$name" -o json \
    | jq '[.items[].endpoints[]? | select(.conditions.ready != false)] | length')"
  [[ "$ready" -gt 0 ]] || fail "webhook Service $svc has no ready endpoint: Swarmada's webhook server is not running"
  ok "webhook Service $svc: $ready ready endpoint(s)"
done < <(sort -u <<<"$services")

# The FleetTask webhook answers: dry run of a held draft.
sns="${SWARMADA_NAMESPACE:-warehouse-a}"
k get namespace "$sns" >/dev/null 2>&1 || fail "namespace $sns does not exist (set SWARMADA_NAMESPACE)"
probe="fleetops-preflight-$(date +%s)"
set +e
out="$(k -n "$sns" create --dry-run=server -f - 2>&1 <<YAML
apiVersion: swarmada.io/v1
kind: FleetTask
metadata:
  name: $probe
  labels: {fleetops.alexbahel.github.io/created-by: fleetops-preflight}
spec:
  draft: true
  actions:
    - name: probe
      action: {type: Patrol, zone: $sns, requiredCapabilities: [navigation]}
YAML
)"
rc=$?
set -e
if [[ $rc -eq 0 ]]; then
  ok "FleetTask webhook answered: dry-run held draft admitted ($out)"
elif grep -q 'admission webhook "[^"]*swarmada.io" denied' <<<"$out"; then
  ok "FleetTask webhook answered: dry-run held draft denied by Swarmada ($(head -1 <<<"$out"))"
elif grep -qi 'failed calling webhook' <<<"$out"; then
  fail "the API server could not call Swarmada's webhook: $(head -1 <<<"$out")"
else
  fail "dry run of a held draft did not reach a Swarmada webhook: $(head -1 <<<"$out")"
fi

echo "preflight: PASSED for $ctx"
