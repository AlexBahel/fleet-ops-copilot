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
set -euo pipefail
: "${SWARMADA_DIR:?}" "${CLUSTER:?}" "${KUBECONFIG:?}" "${SWARMCTL:?}" "${OUT:?}"
[[ "$CLUSTER" == foc-* ]] || { echo "CLUSTER must start with foc-" >&2; exit 1; }
ctx="kind-$CLUSTER"
repo="$(cd "$(dirname "$0")/.." && pwd)"
BACKENDS="${BACKENDS:-scripted}" RUNS="${RUNS:-3}"
FIXTURES="${FIXTURES:-battery-edge battery-handoff hardware-fault comms-flaky estop-drill zone-estop healthy-fleet}"

kubectl --context "$ctx" -n foc-system port-forward svc/foc-mcp 18080:8080 >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
for _ in $(seq 30); do curl -sf http://127.0.0.1:18080/healthz >/dev/null && break; sleep 1; done

scenarios_for() { # fixture -> comma-separated scenario ids, dispatch excluded
  grep -l "^fixture: live/$1\$" "$repo"/evals/scenarios/*.yaml | xargs -n1 basename | sed 's/\.yaml$//' \
    | grep -v '^dispatch-' | paste -sd, -
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
