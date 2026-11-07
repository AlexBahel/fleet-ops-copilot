#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Captures eval fixtures from live Swarmada quickstart runs.
#
# For each scenario: tear the quickstart cluster down (run.sh --clean), run
# `make quickstart SCENARIO=<s>` in a Swarmada checkout, then write
# evals/fixtures/live/<s>.yaml with evals/cmd/fleetops-capture. For the stop
# scenario, after the quickstart it also triggers a zone estop with
# `swarmctl estop trigger` and captures that state as zone-estop.yaml.
#
# Required environment:
#   SWARMADA_DIR  a Swarmada checkout at the pinned commit (read only; nothing in it is edited)
#   CLUSTER       kind cluster name; must match ^foc-[a-z0-9-]+$
#   KUBECONFIG    a kubeconfig file used only for this cluster (kind writes its context there,
#                 so no other kubeconfig's current-context changes)
#   SWARMCTL      path to a swarmctl binary built from SWARMADA_DIR
# Optional: SOURCE_NOTE (appended to each fixture's Source line), SCENARIOS (space-separated).
# Refuses before doing anything unless CLUSTER matches ^foc-[a-z0-9-]+$ and KUBECONFIG
# names a dedicated file, never ~/.kube/config (hack/lib/foc-guard.sh; test:
# test/scripts/guard_test.go).
set -euo pipefail

# shellcheck source=lib/foc-guard.sh
source "$(dirname "$0")/lib/foc-guard.sh"
foc_guard capture-live-fixtures.sh
: "${SWARMADA_DIR:?}" "${SWARMCTL:?}"
CTX="kind-$CLUSTER"
REPO="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$REPO/evals/fixtures/live"
SCENARIOS="${SCENARIOS:-healthy-fleet battery-edge battery-handoff hardware-fault comms-flaky estop-drill}"
mkdir -p "$OUT"
(cd "$REPO" && go build -o bin/ ./evals/cmd/fleetops-capture)

for s in $SCENARIOS; do
  echo "==> $s: clean"
  (cd "$SWARMADA_DIR" && QUICKSTART_CLUSTER="$CLUSTER" bash examples/warehouse-quickstart/run.sh --clean)
  echo "==> $s: quickstart"
  (cd "$SWARMADA_DIR" && QUICKSTART_CLUSTER="$CLUSTER" CI=true make quickstart SCENARIO="$s")
  "$REPO/bin/fleetops-capture" -context "$CTX" -scenario "$s" \
    -source "Swarmada v0.4 quickstart, make quickstart SCENARIO=$s, after it reached its end state and exited (the quickstart stops its sim adapter process on exit, so robot phase and connectivity drift afterwards)${SOURCE_NOTE:+; $SOURCE_NOTE}" \
    -out "$OUT/$s.yaml"
  if [[ "$s" == estop-drill ]]; then
    echo "==> zone-estop: swarmctl estop trigger"
    "$SWARMCTL" --context "$CTX" -n warehouse-a estop trigger warehouse-a \
      --reason "fixture capture: zone stop drill (fictional site)" --yes
    sleep 10
    "$REPO/bin/fleetops-capture" -context "$CTX" -scenario zone-estop \
      -source "Swarmada v0.4 quickstart SCENARIO=estop-drill (exited; its sim adapter process stopped), then swarmctl estop trigger warehouse-a, captured 10 s later${SOURCE_NOTE:+; $SOURCE_NOTE}" \
      -out "$OUT/zone-estop.yaml"
  fi
done
