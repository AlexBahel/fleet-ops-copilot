#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Check that a file holds a Claude API key alone, without showing it
# (docs/DESIGN.md D103). make demo DEMO_MODEL=cloud runs it on
# DEMO_ANTHROPIC_KEY_FILE before it starts anything; run it yourself right after
# writing the file:
#
#   hack/check-key-file.sh ~/fleetops-key.txt
#
# A key file passes when it is one line (one trailing line break is allowed),
# starts with sk-ant-, has only letters, digits, - and _, and is at least 40
# characters long. This checks the shape only: whether the API accepts the key
# is known at the first question. On a refusal it says what the file seems to
# hold (empty, several lines, text with spaces, a NAME=value line, a web
# address, another kind of token) and never prints the content.
set -euo pipefail

f="${1:?usage: hack/check-key-file.sh <key file>}"
hint="Copy the key from the Claude Console again, write it to the file again, then run hack/check-key-file.sh $f again."
refuse() {
  echo "$f: not a Claude API key: $*." >&2
  echo "$hint" >&2
  exit 1
}

[[ -e "$f" ]] || refuse "there is no such file"
[[ -f "$f" ]] || refuse "it is not a regular file"
[[ -r "$f" ]] || refuse "it cannot be read"

export LC_ALL=C
# The x keeps trailing line breaks, which $(...) would drop.
raw="$(cat "$f"; printf x)"
raw="${raw%x}"
body="${raw%$'\n'}"

if [[ -z "${body//[[:space:]]/}" ]]; then
  refuse "it is empty: nothing was pasted into it"
fi
if [[ "$body" == *$'\n'* ]]; then
  nl="${body//[!$'\n']/}"
  n=$((${#nl} + 1))
  what="it looks like copied text, not a key"
  while IFS= read -r line; do
    if [[ "$line" == sk-ant-* ]]; then what="one line starts with sk-ant-, so the key seems to be there among other text"; fi
  done <<<"$body"
  refuse "it has $n lines; a key file holds only the key, on one line; $what"
fi
if [[ "$body" == *$'\r'* ]]; then
  refuse "it contains a carriage return (a Windows line ending); the file must hold the key alone"
fi
assign='^(export[[:space:]]+)?[A-Za-z_][A-Za-z0-9_]*='
if [[ "$body" =~ $assign ]]; then
  refuse "it looks like a shell line (NAME=value), not the key alone"
fi
if [[ "$body" == *[[:space:]]* ]]; then
  words="$(printf '%s' "$body" | wc -w | tr -d ' ')"
  what="it looks like copied text, not a key"
  if [[ "$body" == *sk-ant-* ]]; then what="a key seems to be inside other text"; fi
  refuse "it is one line of ${#body} characters with spaces in it ($words words); $what"
fi
if [[ "$body" == http://* || "$body" == https://* ]]; then
  refuse "it looks like a web address, not a key"
fi
if [[ "$body" != sk-ant-* ]]; then
  refuse "it does not start with sk-ant- (one line of ${#body} characters); it looks like another kind of text or token"
fi
if [[ "$body" == *[!A-Za-z0-9_-]* ]]; then
  refuse "it has characters a key does not have (a key has only letters, digits, - and _)"
fi
if ((${#body} < 40)); then
  refuse "it is ${#body} characters long, too short for a key"
fi
echo "$f: looks like a Claude API key (one line, starts with sk-ant-, no spaces); its content is not shown."
