# Fleet Ops Copilot — Guided task requests

How work is asked for: the site catalogue, the dialogue rules, "something else", `check_task`,
the preview and its confirmation, the re-check at execution, conversation state, failure
handling and `fleetops activate`. The summary is in [`DESIGN.md`](../DESIGN.md).

## 3.10 Guided task requests (D70–D81)

A person asks for work in plain language. The copilot asks at most two short questions, shows a
preview with the checks it ran, and files a request only after the person confirms that preview.
The request then follows the approval path of [§3.3](approvals.md#33-approval-flow) unchanged.

### 3.10.1 Site catalogue (`catalogue/<site>.yaml`, D70)

One YAML file per site, mounted into `fleetops-agent` from a ConfigMap like the policy files. It has a
`version`, and `fleetops-agent` records the file's SHA-256 in every preview and every `ApprovalRequest`;
evaluation results record both (it is part of the eval surface, D81).

Each kind of work has a class, from what Swarmada v0.4 can express for it:

- **(a)** expressible, with its result reportable from a field (move, deliver, charge, patrol,
  zone maintenance, zone stop): for example `phase: Succeeded` of a Navigate action is arrival;
- **(b)** expressible, but the result asked for has no field (inspect, survey): Swarmada records
  arrival and completion, not what the robot found or the map it made;
- **(c)** needs a Swarmada change, proposed for a later Swarmada version (C1–C7 in
  `docs/BACKLOG.md`).

Only class (a) and (b) kinds can be previewed and filed. The catalogue holds:

- **`kinds`**: one entry per kind of work, each with `class` (`a` or `b`),
  `resultReported` (false for class (b): Swarmada records arrival and completion, not what the robot
  found), `requiredCapabilities`, the Swarmada mapping (`type`, optional `customType`,
  payload keys, members and `dependsOn`, or a `loop` for repeats), `required` and `optional`
  fields, `defaults`, the result fields a person can read afterwards, and the failure-handling
  options (always `ReportOnly` and `PrepareFollowUp`).
- **`asks`**: one decision tree per ask (inspect, move, deliver, charge, patrol, survey,
  zone maintenance, zone stop, activate). A node is a question with an id, its text, the fields it
  fills, and numbered choices. A choice either selects a kind, sets field values, or is the last
  choice, "Something else — describe it". Choices that come from the cluster (waypoints, robots,
  zones, drafts) are declared as a source and ordered by a declared rule ([§3.10.2](#3102-dialogue-rules-d71)).
- **`locations`**: how a location is written into a payload at this site. For the fictional
  warehouse: a `FleetZone` waypoint name resolved from `spec.waypoints[]` to
  `{"waypoint": "<name>", "x": <x>, "y": <y>}`, which the quickstart's adapter reads.
- **`basicSteps`**: the kinds a "something else" request may be composed from (`move`, `patrol`,
  `charge`), each a single member.
- **`notYetPossible`**: the class (c) items, each needing a Swarmada change proposed for a later version (C1–C7), with the sentence
  the copilot says when a request needs one. Nothing in this list can be previewed or filed.

Kinds in the file for the fictional warehouse: `inspect-damage`, `inspect-location`,
`inspect-both` (class b), `move`, `deliver`, `charge`, `patrol` (class a), `survey` (class b,
`Custom` `explore-floor`), `zone-maintenance`, `zone-stop` (class a, the existing request
types), and `activate` (the existing activation request). A DraftTask kind also takes `name`, the
draft's name when the person gives one; otherwise the engine generates it. Changing `resultReported` to true when
Swarmada can report a result (C1) changes one line of the preview and nothing in the dialogue
(a test flips it).

The loader validates the file when `fleetops-agent` starts and refuses to start on any error: unknown
keys, a kind with no Swarmada mapping or a mapping to a type outside `api/v1`, a class other than
`a` or `b`, `resultReported: true` on a class (b) kind, a tree deeper than two questions before the
preview, a choice list that does not end with "Something else — describe it" (the inspect
question included), a question with more than four choices besides that one, a
reference to an undefined kind or field, or a default for a field the kind does not have.

### 3.10.2 Dialogue rules (D71)

The tree is walked by `internal/dialogue` in `fleetops-agent`, a pure function from (catalogue,
dialogue state, fields the model extracted, fleet snapshot) to the next step: a question, a
preview, or a statement that the request cannot be made. The model's part is to read the person's
words: it calls a tool that `fleetops-agent` handles itself, `propose_task(ask, fields, answer,
somethingElse)`, with what it extracted, and it may call read tools first. When `propose_task`
returns a question or a preview, `fleetops-agent` ends the turn with the text the engine rendered. The
model does not write the question, the choices or the preview. The person sees the text of every
model response of the message, in order, before the engine's text, and the trace records each
response (D84). Every question and preview the engine writes states what fleetops-agent read itself
in a "Checked:" block, never anything the model wrote (D92): a question's block, from the engine's
fleet snapshot, stands before the question, whose text and choices stay last.

1. **Extract** every field the message states (kind, location, robot, times, zone, mode, start),
   by the catalogue's field names; a call naming another field is refused and the model is asked
   which allowed field it meant (D85), as is a catalogue field the kind does not use (`dock` on a
   move), with the kind's fields named (D94); a field with an empty value is ignored, and after the
   same field is refused twice in one message the person is asked which field holds its value
   (D98). Closed values are those the site policy allows; a value it
   forbids (priority `Critical`) is refused to the person before any question (D93).
2. **Infer** from live state instead of asking: one zone means no zone question; a named robot
   becomes `robotSelector` for charge and `preferredRobot` elsewhere; a dock comes from the robot's
   `spec.charging.dockName` or the zone's `ChargingDock`.
3. **Ask the most discriminating question first**: the tree's order, which puts the question that
   selects the kind before questions about its fields. A question whose answer is already known is
   skipped.
4. **Numbered choices, likeliest first**, then "Something else — describe it". Likeliest means: an
   option named or partly named in the person's words first, then by the source's rule (robots by
   lowest battery for charge, by eligibility for other kinds; drafts newest first; waypoints in
   catalogue order), at most four.
5. **Combine small details** into one question where the catalogue says so (deliver asks "from
   where to where" as one question with pairs; zone maintenance asks mode and start together).
6. **Defaults are shown in the preview, not asked**: a question whose fields all have a value or a
   default is skipped (a patrol runs one round, maintenance starts on approval, unless the person
   says otherwise). Defaults: priority `Normal`, any eligible robot,
   failure handling report only, Swarmada's own retry (`onFailure: Requeue`, shown as "retries up
   to 3 times, then alerts"; "alert on first failure" is available through "Change something"),
   start when activated, a generated draft name.
7. **At most two questions before the preview.** The engine counts every question it shows in the
   dialogue state, a question asked again after an unclear answer and the open question included
   (D87), and, once two have been shown, returns the preview with defaults for what is still open. A required
   field with no default is then shown in the preview as missing; that preview cannot be
   confirmed, and its choices are to supply the item or cancel.
8. **A value without its field's form is not kept** (D88). `start` must be empty (on approval) or
   an RFC 3339 time, `times` a whole number of 1 or more, `mode`, `priority` and `onFailure` one
   of their values, `name` a DNS-1123 name. A start of `now`, `immediately` or `asap` (any case)
   is not refused: the window starts on approval, with the mode given or its default (D91). Any
   other value without
   its form (`start: on approval`) is removed
   from the dialogue state before anything is checked, and the question that fills the field is
   asked again, opening with the value and the valid forms; it counts toward the cap. Past the cap,
   or for a field no question fills, the default is used and the preview's line says the value
   could not be used. A later call with a valid value is used.
9. **Inspect's first question is exactly** "What should the robot inspect? 1 Damage · 2 Location ·
   3 Both · 4 Something else — describe it". The second, when needed, is where (waypoints).

Failure handling is always offered, inside the preview's confirmation choices ([§3.10.5](#3105-preview-and-confirmation-d74)): report
only, or prepare a follow-up request if the task fails. A follow-up is never filed automatically
(D78).

### 3.10.3 "Something else" (D72)

- One open question: "Describe what the robot should do or look for, where, and what result you
  need." It is not asked when the person's reply to the choice already describes it ("4 check the
  extinguisher at receiving-dock-1"). It counts toward the two-question cap.
- The person's text is kept verbatim by `fleetops-agent` (from the message, not from the model) and
  attached to the request as `spec.preview.instruction` and to each member's payload as
  `instruction`.
- The model classifies it through `propose_task(somethingElse: …)` as one of: a **known kind**
  (normal preview); **composed** from `basicSteps` (members with `dependsOn`; the preview and the
  `ApprovalRequest` say "composed from basic steps for human review", and the requests panel shows
  it); or **not possible** (it needs a `notYetPossible` item or a capability no robot has): the
  copilot says why and files nothing. A model that answers the description with a plain ask of the
  catalogue (`propose_task(ask: charge, …)`) instead of `somethingElse` has read it as that ask:
  the request becomes that ask, its kind is logged as a known reading, and its own tree continues
  within the same cap (D87).
- **A finding is an inspection, never "not possible" (D72).** A need for what a robot sees
  or records (a reported result, a photo, damage, what is at a place) is filed as the class (b)
  inspect kind for what it looks for (`lookFor`: damage, location-state, or both), with the "not
  reported" label and, for the finding named, the catalogue's sentence ("Not reported: photos or
  other evidence attached to the result. …"). The catalogue marks those gaps `finding: true` (C1,
  C2); `propose_task` does not offer them as reasons for `notPossible`, and when a model names one
  anyway the engine maps the request itself: the model's `lookFor`, else damage when the person's
  words name damage, else the state of the location. "Not possible" stays for work no robot can do
  at all (C3, an external analysis service; C6). Scenarios guided-15 to guided-17 replay
  readings in which a model called such a need not possible; without the mapping they fail.
- At most one follow-up question for one missing required item, and only within the cap; past the
  cap the item is shown as missing in the preview (rule 7).
- Every "something else" answer is written to `fleetops-agent`'s audit file as a `catalogue_review`
  entry: requester, the redacted text, the interpretation, the kind or steps, the reason when not
  possible. No token, no conversation id, nothing else from the request.

### 3.10.4 `check_task` (D73)

A read-only tool in `fleetops-mcp`. Input: the draft the preview would file. Output: a verdict
(`likely feasible` or `blocked`; never "guaranteed", and a test fails if that word is returned),
blockers and warnings (each with a stable code, the member, the reason and, for eligibility, the
Swarmada filter number), alternatives, and the list of checks not made. Checks, from reads only:

- **eligible robot now**, per member: the candidate filters of RFC §9.4.2 that reads can evaluate
  (1–8, 11, 12, the resume window, a robot being deleted; 9 except contract range and
  `SafetyStream`; 10 except reported stops), and the adapter catalog's type and `customType`. A
  member with no robot passing every filter is a blocker, except that robots failing only filter 1
  (busy now) make it a warning ("eligible once sim-robot-002 is idle"). Two named blockers:
  `CHARGE_BELOW_BATTERY_FLOOR`, a Charge for a robot whose battery is
  below `Robot.spec.constraints.minBatteryPctForAction` (filter 4 has no Charge exemption), with
  the alternatives "charge it by hand or with the robot's own app" and "a person lowers the floor";
  `NO_CARRYING_ROBOT`, a deliver whose carrying capability (`transport.payload` at this site) no
  robot declares, with the alternative "move instead (no load)";
- **zone state**: exists, estop on it or an ancestor, an `Active` or `Scheduled` maintenance
  covering it, restrictions denying entry or a required capability, a namespace stop;
- **location and dependencies exist**: the waypoint is in the zone's chain, and a location that no
  FleetZone declares (it resolves to no zone) is `WAYPOINT_NOT_FOUND` (D86); every `dependsOn`
  names a member and the graph is acyclic (Swarmada checks this only after activation, C7); the
  draft name is free;
- **timing**: start, deadline and maintenance start in the future; a loop has a bound;
- **policy**: the policy engine's decision on the request (a refusal is a blocker).

`SwarmadaConfig` (namespace stop, `maxPendingActionsPerZone`) is read (D80): its schema has no
secret-typed field (Secret references name public-key material only). It is read only in code,
never through a model-facing tool; the reader projects the namespace-stop markers and
`maxPendingActionsPerZone` and drops the rest of the object at once; the object is never logged or
traced (D80). The ServiceAccount can still read the whole object (KNOWN-LIMITS). If the read is refused, the output lists "namespace-wide stop and Pending
cap not checked". The per-instance `validate_action` of the adapter cannot be observed by any
client before assignment and is always listed as not checked (a Swarmada change is proposed for a later version: C5 in `docs/BACKLOG.md`).

A test reads RFC §9.4.2 from the pinned module and fails when its numbered filter list, or the
filter constants in `internal/controller/dispatch_filters.go`, differ from the list `check_task`
was written against, so a re-pin shows what to revisit.

### 3.10.5 Preview and confirmation (D74)

The preview shows: the kind and its catalogue version; every field with "(default)" where it was
not given; for a class (b) kind, "Swarmada records arrival and completion, not what the robot
found; a person reviews the result on the robot's side"; for a composed request, that it is
composed for human review; the person's own words when they were kept; the generated draft name;
the `check_task` verdict, blockers, warnings and alternatives; the "Checked:" block, from the facts
`check_task` read (each zone, each robot considered with the fields its filters used and why it is
eligible now or not, at most five robot lines; the FleetTask an activation names; D92); and the sentence "Checked against
current fleet state; Swarmada's own acceptance is checked at execution." It ends with:
"1 Confirm · 2 Confirm, and prepare a follow-up request if it fails · 3 Change something —
describe it · 4 Cancel". A blocked preview offers only 3 and 4.

The preview id is the SHA-256 of the canonical request parameters with the catalogue SHA-256.
`fleetops-agent` stores the preview in the conversation. When the next message from the same person in
the same conversation is "1", "2", "confirm" or "confirm with follow-up" (matched by `fleetops-agent`,
not the model), `fleetops-agent` calls the request tool with exactly the stored parameters and the
preview id, and sets `fleetops.alexbahel.github.io/confirmedPreview` in the call's `_meta`. fleetops-mcp
refuses a request without that confirmation (`DENY_NOT_CONFIRMED`) or whose recomputed id differs
(`DENY_PREVIEW_MISMATCH`). The model is not offered the `request_*` tools. This applies to every
request type, zone stops included (a zone stop was never an emergency path, [§1.2](../DESIGN.md#what-it-is)).

When the filing fails as a call rather than with a policy decision (the API server refuses the
`ApprovalRequest`, fleetops-mcp cannot be reached), `fleetops-agent` replies with a fixed sentence
that names the request and the error and says that nothing was filed (dialogue code
`FILING_ERROR`, D86); the conversation's preview is closed.

Another MCP client (the evaluation harness, a local stdio client) sets the confirmation itself;
like the session id, it is asserted by the person's own client (KNOWN-LIMITS).

### 3.10.6 Re-check at execution (D76)

Immediately before creating an approved draft, `fleetops-approvals` re-runs `check_task` on fresh state,
then makes two kinds of dry run with `dryRun: All`: the FleetTask exactly as it will be created
(Swarmada's flow, binding and producer-contract checks, the CRD's own rules, and the copilot's
admission policy), and each member as a standalone FleetAction (Swarmada's catalog check, External
references and the per-zone Pending cap, which a FleetTask dry run does not reach). Any blocker or
refused dry run moves the request to `Failed` with `DENY_CHECK_FAILED` or `DENY_SWARMADA_REFUSED`
and Swarmada's message, and writes nothing. The results go to `status.executionChecks`, the Event
and the audit line, and `fleetops requests` and the web UI show the reason to the requester. Creating
FleetActions is otherwise refused to fleetops-approvals by `fleetops-approvals-dry-run-only`.
fleetops-mcp holds no create permission of any kind (D25 unchanged).

### 3.10.7 Conversation state (D75)

- `fleetops-agent` keeps conversations in memory: id → owner, created, last used, bounded history (the
  last 16 messages and at most 16 KiB of person and assistant text; tool results are not kept),
  and the dialogue state (ask, kind, fields, questions asked, the last question, the preview shown).
- The id is 128 random bits generated by `fleetops-agent` and returned only to the client that started
  the conversation; `fleetops-agent` stores only its SHA-256.
- The owner is the requester as `fleetops-mcp` verified it (`whoami`, the same TokenReview as every tool
  call). A message continues a conversation only when both the id and the owner match; otherwise
  it starts a new one, and the answer says so. A conversation of an unverified requester is
  `unverified:<name>`; it works, and anything it files can never be approved (D56).
- TTL 30 minutes from the last message; at most 5 conversations per owner and 1000 in total,
  oldest dropped first. A restart loses them. The chart refuses `agent.replicas` > 1.
- Nothing secret is stored: no token, no tool result, text passed through the redaction of [§3.8](components.md#38-observability-and-secrets).
- `fleetops chat` keeps the id per kubeconfig context in a file under the user's state directory
  (mode 600) and continues it until `fleetops chat --new`; the web page keeps it in the page's memory.

### 3.10.8 Failure handling and follow-up (D78)

Both options leave Swarmada's bounded retry in place: every member is created with
`onFailure: Requeue` (the controller's defaults, 3 retries 30 s apart, then `Alert` behaviour),
shown in the preview as "retries up to 3 times, then alerts", unless the person chose "alert on
first failure" (`onFailure: Alert`) through "Change something"; `failurePolicy: FailFast`. After the activation is observed, `fleetops-approvals` keeps watching the
task until it is terminal and records `status.outcome` (task phase, the failed member and its
`failureReason`) and an Event. With `PrepareFollowUp` it also records `status.followUp`: the same
kind and fields, as a proposal. `fleetops chat --follow-up <request>` opens a conversation that starts
at that proposal's preview; it is filed only after a confirmation, like any other request.

### 3.10.9 `fleetops activate` (D79)

`fleetops activate <request>` reads the `ActivateTask` request with the caller's kubeconfig and requires
state `Approved`. Any person Swarmada allows to activate may run it: the CLI refuses a
ServiceAccount caller (SelfSubjectReview) up front, and Swarmada's own `activate` check decides for
everyone else. It runs the same `check_task` evaluation with the caller's read permissions (a read
the caller lacks is listed as not checked) and refuses on a blocker. It then records the activation
in the copilot's own API: an `ApprovalDecision` of `type: Activation` (`requestRef`, `decidedBy`,
`decidedByGroups`, no `verdict`), under the same `fleetops-decisions` admission binding as a verdict,
so it names only the authenticated caller and is never changed. Then it sends the plain merge patch
`{"spec":{"draft":false}}`; nothing is added to the FleetTask. Swarmada's FleetTask webhook
checks the caller's `activate` verb. An Activation record decides nothing: the decision loop skips
it, and the CRD refuses a record with a verdict or a verdict decision without one. When the task
leaves Draft, `fleetops-approvals` links the most recent Activation record made no earlier than the
approval (`status.activationRef`, `status.activatedBy`) and names any earlier ones in its
`activation_observed` audit line and the request message (a record whose patch Swarmada refused
stays, since fleetops activate records before it patches). An activation made without `fleetops activate`
(a plain `kubectl patch`, which still works) is recorded as "activated outside fleetops activate;
activator not recorded" (`status.activatedBy: unknown`). `fleetops activate` needs `create` on `approvaldecisions`,
which the `fleetops:approver` role grants (a narrower `fleetops:activator` role is open, P3 in
`docs/BACKLOG.md`); without it, it stops before the patch. It prints the API server and
namespace as `fleetops approve` does, and replaces the `kubectl patch` instruction.
