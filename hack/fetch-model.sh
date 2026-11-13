#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Fetch the air-gapped profile's model files once into a cache on this machine,
# each checked against its sha256 before it is kept (docs/DESIGN.md D103).
# make demo runs it before it creates the cluster; the files then go into the
# kind node with make load-llama-model. Nothing is downloaded inside the cluster.
#
#   hack/fetch-model.sh <cache directory>
#
# The files are those of the v6 record runs: Qwen2.5-7B-Instruct, GGUF
# Q4_K_M, two shards, 4,683,073,632 bytes together (3,993,201,344 and
# 689,872,288), from the official Qwen repository on Hugging Face at a fixed
# revision. Their sha256 are in config/airgap/model-SHA256SUMS (the same as
# docs/evidence/airgap-2026-11-12/model-SHA256SUMS). A cached file whose sha256
# still matches is reused; one that does not is downloaded again.
#
# Environment (for tests and mirrors):
#   MODEL_SUMS      sha256sum-format file naming the files (default config/airgap/model-SHA256SUMS)
#   MODEL_BASE_URL  URL the file names are appended to (default: the Hugging Face revision below)
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
cache="${1:?usage: hack/fetch-model.sh <cache directory>}"
sums="${MODEL_SUMS:-$repo/config/airgap/model-SHA256SUMS}"
base="${MODEL_BASE_URL:-https://huggingface.co/Qwen/Qwen2.5-7B-Instruct-GGUF/resolve/bb5d59e06d9551d752d08b292a50eb208b07ab1f}"

fail() { echo "fetch-model: $*" >&2; exit 1; }
sha256() {
  if command -v shasum >/dev/null; then shasum -a 256 "$1" | cut -d' ' -f1; else sha256sum "$1" | cut -d' ' -f1; fi
}

[[ -f "$sums" ]] || fail "no sha256 list at $sums"
mkdir -p "$cache"

while read -r want name; do
  [[ -n "${want:-}" ]] || continue
  name="${name#\*}"
  f="$cache/$name"
  if [[ -f "$f" ]]; then
    if [[ "$(sha256 "$f")" == "$want" ]]; then
      echo "  $name: in the cache, sha256 OK"
      continue
    fi
    echo "  $name: in the cache but its sha256 does not match; downloading it again"
    rm -f "$f"
  fi
  echo "  $name: downloading from $base/$name"
  # A .part file left by an interrupted download is continued (-C -).
  curl -fL --retry 3 -C - -o "$f.part" "$base/$name" || fail "download of $name failed (the partial file $f.part is kept and continued next time)"
  got="$(sha256 "$f.part")"
  if [[ "$got" != "$want" ]]; then
    rm -f "$f.part"
    fail "$name: sha256 $got does not match $want from $sums; the download was deleted"
  fi
  mv "$f.part" "$f"
  echo "  $name: downloaded, sha256 OK"
done <"$sums"
