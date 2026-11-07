# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Runtime image for fleetops-mcp, fleetops-approvals and fleetops-agent. The binaries are
# built on the host (make image) because, until the Swarmada module is pinned
# in go.mod, it is resolved through a local workspace that a container build
# cannot see (docs/DESIGN.md D2). The :debug-nonroot base adds a busybox shell
# so the audit file can be read with `kubectl exec`.
FROM gcr.io/distroless/static:debug-nonroot
ARG TARGETARCH
# --chmod: the mode in the image must not depend on the umask of the machine
# that built the binaries (R60); test/image runs each one as user 65532.
COPY --chmod=0755 bin/linux-${TARGETARCH}/fleetops-mcp bin/linux-${TARGETARCH}/fleetops-approvals bin/linux-${TARGETARCH}/fleetops-agent /usr/local/bin/
# --chmod also sets the mode of the parent directories COPY creates
# (/etc/fleetops, /etc/fleetops/policies), so it is 0755 here: 0644 left them
# untraversable for user 65532, and the policy and catalogue mounted under
# /etc/fleetops unreadable (seen on the demo stack, 2026-11-07).
COPY --chmod=0755 policies/default.yaml /etc/fleetops/policies/default.yaml
USER 65532:65532
