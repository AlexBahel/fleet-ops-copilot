#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Runs the eval scenarios whose fixture was captured from a quickstart run
# (fixture: live/<name>) against that quickstart state on a live kind cluster,
# through the deployed foc-mcp (foc-eval -live-context). For each quickstart
# SCENARIO it re-runs `make quickstart SCENARIO=<s>` on the same cluster (the
# copilot in foc-system is kept), then runs the matching scenarios.
# zone-estop is the estop-drill state plus `swarmctl estop trigger warehouse-a`;
# after it, the stop is cleared as cluster admin with swarmctl.
#
# Dispatch scenarios are skipped: the quickstart's overlay serves no admission
# webhooks, so the copilot refuses every draft there (DENY_DRAFT_GATE_ABSENT,
# docs/DESIGN.md D22); test/live covers the draft path on an install with webhooks.
#
# Required: SWARMADA_DIR, CLUSTER (foc-*), KUBECONFIG, SWARMCTL, OUT (results dir).
# Optional: BACKENDS (default "scripted"), RUNS (default 3), CLAUDE_MODEL, plus the
# -price-* values as PRICE_INPUT PRICE_OUTPUT PRICE_CACHE_WRITE PRICE_CACHE_READ PRICING_SOURCE PRICING_READ_ON.
#
# Refuses to start unless CLUSTER matches ^foc-[a-z0-9-]+$ and KUBECONFIG names a
# dedicated file: the Swarmada quickstart runs `kubectl config use-context`, which
# changes the current context of the file KUBECONFIG names, so KUBECONFIG must be
# set and no entry in it may resolve to ~/.kube/config.
# Test: test/scripts/livescenarios_test.go.
set -euo pipefail

refuse() { echo "live-scenarios.sh: refused: $*" >&2; exit 1; }
[[ "${CLUSTER:-}" =~ ^foc-[a-z0-9-]+$ ]] || refuse "CLUSTER must match ^foc-[a-z0-9-]+\$ (got '${CLUSTER:-}')"
[[ -n "${KUBECONFIG:-}" ]] || refuse "KUBECONFIG must be set to a dedicated kubeconfig file, not ~/.kube/config"
default_kubeconfig="${HOME:?}/.kube/config"
default_physical="$(cd "$HOME/.kube" 2>/dev/null && pwd -P)/config" || default_physical="$default_kubeconfig"
IFS=: read -r -a kubeconfig_entries <<<"$KUBECONFIG"
entries=0
for k in "${kubeconfig_entries[@]}"; do
  [[ -n "$k" ]] || continue
  entries=$((entries + 1))
  k="${k/#\~/$HOME}"
  [[ "$k" == /* ]] || k="$PWD/$k"
  physical="$(cd "$(dirname "$k")" 2>/dev/null && pwd -P)/$(basename "$k")" || physical="$k"
  # -ef: same device and inode, so symlinks and hard links to the file are caught;
  # the path comparisons catch the file when it does not exist yet.
  if [[ "$k" -ef "$default_kubeconfig" || "$k" == "$default_kubeconfig" \
    || "$physical" == "$default_kubeconfig" || "$physical" == "$default_physical" ]]; then
    refuse "KUBECONFIG entry '$k' resolves to ~/.kube/config; use a dedicated file (e.g. kind get kubeconfig --name \"$CLUSTER\" > file)"
  fi
done
((entries > 0)) || refuse "KUBECONFIG names no file"

: "${SWARMADA_DIR:?}" "${SWARMCTL:?}" "${OUT:?}"
ctx="kind-$CLUSTER"
repo="$(cd "$(dirname "$0")/.." && pwd)"
BACKENDS="${BACKENDS:-scripted}" RUNS="${RUNS:-3}"
FIXTURES="${FIXTURES:-battery-edge battery-handoff hardware-fault comms-flaky estop-drill zone-estop healthy-fleet}"

kubectl --context "$ctx" -n foc-system port-forward svc/foc-mcp 18080:8080 >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 30); do curl -sf http://127.0.0.1:18080/healthz >/dev/null && break; sleep 1; done

scenarios_for() { # fixture -> comma-separated scenario ids, dispatch excluded; empty if none
  # grep exits 1 on no match; under pipefail that would stop the whole script at a
  # fixture no scenario uses (live/comms-flaky, live/estop-drill).
  { grep -l "^fixture: live/$1\$" "$repo"/evals/scenarios/*.yaml || true; } | xargs -r -n1 basename | sed 's/\.yaml$//' \
    | { grep -v '^dispatch-' || true; } | paste -sd, -
}

for f in $FIXTURES; do
  ids="$(scenarios_for "$f")"
  [[ -n "$ids" ]] || continue
  qs="$f"; [[ "$f" == zone-estop ]] && qs=estop-drill
  echo "==> $f: make quickstart SCENARIO=$qs"
  (cd "$SWARMADA_DIR" && QUICKSTART_CLUSTER="$CLUSTER" CI=true make quickstart SCENARIO="$qs")
  if [[ "$f" == zone-estop ]]; then
    "$SWARMCTL" --context "$ctx" -n warehouse-a estop trigger warehouse-a --reason "live eval: zone stop drill (fictional site)" --yes
    sleep 10
  fi
  # Restart the port-forward: the quickstart run may have replaced pods it depends on.
  kill $pf 2>/dev/null || true
  kubectl --context "$ctx" -n foc-system port-forward svc/foc-mcp 18080:8080 >/dev/null 2>&1 &
  pf=$!
  for _ in $(seq 30); do curl -sf http://127.0.0.1:18080/healthz >/dev/null && break; sleep 1; done
  for b in $BACKENDS; do
    args=(-live-context "$ctx" -live-mcp-url http://127.0.0.1:18080/mcp -only "$ids" -runs "$RUNS")
    name="$b"
    if [[ "$b" == claude ]]; then
      name="claude-${CLAUDE_MODEL:-claude-sonnet-5-5}"
      args+=(-backend claude -model "${CLAUDE_MODEL:-claude-sonnet-5-5}")
      [[ -n "${PRICING_SOURCE:-}" ]] && args+=(-price-input "$PRICE_INPUT" -price-output "$PRICE_OUTPUT" \
        -price-cache-write "$PRICE_CACHE_WRITE" -price-cache-read "$PRICE_CACHE_READ" \
        -pricing-source "$PRICING_SOURCE" -pricing-read-on "$PRICING_READ_ON")
    fi
    echo "==> $f: $name ($ids)"
    (cd "$repo" && go run ./evals/cmd/foc-eval "${args[@]}" -out "$OUT/$f/$name/results.json") || true
  done
  if [[ "$f" == zone-estop ]]; then
    echo "==> zone-estop: clear the stop as cluster admin"
    "$SWARMCTL" --context "$ctx" -n warehouse-a estop-clear warehouse-a --yes || true
  fi
done
