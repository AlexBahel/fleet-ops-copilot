# `make foc-live` on the foc-up stack, 2026-11-05

One run of `test/live` (`make foc-live`, phase `webhooks`) against the `make foc-up` cluster of
`docs/evidence/foc-up-2026-11-05/` (its second run), on the Mac mini. Requests are filed with the
requester's own bearer token (sam's, from the test-only static token file, D60) and decided by
dana with her own token. Times are UTC. The full log is not committed (it holds local paths); the
result lines below are copied from it unchanged.

| Item | Value |
|---|---|
| Date | 2026-11-05, about 03:57–03:58 (test objects stamped 1793851023 = 03:57:03; a decision at 03:58:04) |
| Copilot | `738f848` |
| Swarmada | v0.4, `558da47` (tag `rfc-0001-v0.4`) resolved through the `go.mod` pin; quickstart `healthy-fleet` run by `make foc-up` from a writable copy of the pinned module; admission webhooks served (`config/controlstream-tls`, D32, D59) |
| Cluster | kind `foc-up`, kind v0.33.0, single node `kindest/node:v1.37.0` pinned by digest (`hack/foc-up.sh`); host and Docker VM as in `docs/evidence/foc-up-2026-11-05/host-and-docker.txt` |
| Command | `make foc-live` (`hack/live-approval-path.sh`, `PHASE=webhooks`), 1:23.60 total, exit 0 |

## Fix before this run

An earlier `make foc-live` at `d08c345` failed on a test bug: the per-person MCP session was closed
when the first subtest ended and then reused ("client is closing"). `738f848` ("test/live: drop a
closed MCP session from the per-person cache") changes only `test/live/live_test.go` (4 lines
added, 1 removed); no product code changed between the two runs.

## Results

`TestLiveApprovalPaths`: 10/10 subtests PASS. `TestLiveSwarmadaAuditActor`: PASS (in its own
`go test` invocation, after the first one skipped it for want of the audit copy).
`TestLiveDraftGateAbsent`: SKIP, by design; it needs phase `no-webhooks`, which this run did not use.

```text
--- SKIP: TestLiveDraftGateAbsent (0.00s)
--- PASS: TestLiveApprovalPaths (72.89s)
    --- PASS: TestLiveApprovalPaths/swarmada-webhooks-served (0.01s)
    --- PASS: TestLiveApprovalPaths/draft-created-by-executor (11.56s)
    --- PASS: TestLiveApprovalPaths/activate-refused-to-serviceaccounts (2.07s)
    --- PASS: TestLiveApprovalPaths/activation-by-person-observed (10.29s)
    --- PASS: TestLiveApprovalPaths/zone-maintenance-by-executor (6.11s)
    --- PASS: TestLiveApprovalPaths/zone-stop-by-executor (6.10s)
    --- PASS: TestLiveApprovalPaths/guardrails-api-server-refuses-serviceaccounts (0.05s)
    --- PASS: TestLiveApprovalPaths/requester-is-authenticated-not-asserted (10.31s)
    --- PASS: TestLiveApprovalPaths/executor-writes-only-after-named-approval (26.26s)
    --- PASS: TestLiveApprovalPaths/audit-record (0.12s)
--- SKIP: TestLiveSwarmadaAuditActor (0.00s)
PASS
ok  	github.com/AlexBahel/fleet-ops-copilot/test/live	73.428s
--- PASS: TestLiveSwarmadaAuditActor (0.03s)
PASS
ok  	github.com/AlexBahel/fleet-ops-copilot/test/live	0.511s
```

What four of the tests checked, from their log lines and assertions (`test/live/live_test.go`):

| Subtest | Observed |
|---|---|
| `requester-is-authenticated-not-asserted` | A request sent with sam's token and the asserted name "dana" has `requestedFor` sam (asserted by the subtest). A request sent with no token and the asserted name "dana" has `requestedFor` `unverified:dana` (asserted); dana's approval of it was refused ("the requester \"unverified:dana\" was not authenticated"), and the request stayed Pending |
| `executor-writes-only-after-named-approval` | A request nobody decided stayed Pending for 15 s and its FleetTask did not exist. dana's approval of her own request was refused ("\"dana\" asked for this change and cannot also approve it") |
| `audit-record` | `foc-approvals audit file: 15 entries, chain intact` |
| `TestLiveSwarmadaAuditActor` | Swarmada's safety audit log records the approved zone stop as `ESTOP_TRIGGERED` with actor `system:serviceaccount:foc-system:foc-approvals` (D33). Its `outcome` is `Error`, with `stopped:0 unconfirmed:3`: the three simulated robots did not confirm the stop. The test checks the actor, not the outcome; whether the simulated adapter stays connected after the webhook overlay is listed in `docs/KNOWN-LIMITS.md` |
