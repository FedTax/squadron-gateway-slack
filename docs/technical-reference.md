# Technical reference

## `builtins.gateway.post` payload

| Field     | Type    | Meaning |
| --------- | ------- | ------- |
| `text`    | string  | Required. The message, in Slack mrkdwn. |
| `blocks`  | array   | Optional Block Kit blocks. `text` is still sent with them. |
| `thread`  | string  | Optional key naming the request, e.g. a ticket key. Runs that post under the same key share one thread. |
| `root`    | boolean | Replace the request's top-level message with this post instead of replying. Posts it if the request has none yet. |
| `channel` | string  | Optional channel name or ID to open the request's thread in. Used only when neither the run nor `thread` is linked to a thread yet; otherwise the existing thread's channel wins. A name needs `channels:read`/`groups:read`, and one that can't be found falls back to the configured channel. |

Files a mission attaches to the post are uploaded into the same thread.

On a `root` post the gateway appends its own lines after `text`: the mission
line and `Requester: <@user>`, for requests started from `/squadron`. With
`blocks`, they're added as a final `context` block.

## How a run finds its thread

A request thread is identified by its top-level message's timestamp. The gateway
links runs and keys to it:

1. **Started from `/squadron`.** MCP `run_mission` returns the run's mission ID.
   The gateway posts the top-level message and links the mission ID to it.
2. **A post from a linked run** replies in that run's thread. If the post has a
   `thread` key that isn't linked yet, the key is linked to the thread too.
3. **A post from an unlinked run with a key that is linked** links the run to
   that key's thread. Squadron sends the posting run's mission ID with each
   post (`PostMessageRequest.MissionID`), so this is how a routed, resumed or
   externally started run joins an existing request.
4. **A post with no linked run or key** becomes a new top-level message in
   its `channel`, or the configured channel without one. Its run and key
   are linked to it.
5. **A question from an unlinked run** (`HumanInputRecord.MissionID`) posts a
   top-level message of `*<mission>*: <question summary>` in the configured
   channel, links the run to it, and posts the question as its first reply.
6. **A notification** posts in its run's thread, and is dropped if the run has
   none. A notification with its own `channel` posts top-level there.

A run must therefore post under its key before it asks anything, or its
question opens a second top-level message (case 5).

A question's `short_summary` is shown as its heading only when the question
itself is top-level. `PostMessageRequest.TaskID` is received but not used.

## State

The gateway keeps five pieces of state. All except `messages` are saved in the
checkpoint file (`checkpoint_path`) whenever they change, and loaded when the
gateway starts. A missing or unreadable file starts everything empty. Nothing
removes saved entries; there's one per request, run and key.

| State | Maps | Written when | Read when | Why it's needed |
| ----- | ---- | ------------ | --------- | --------------- |
| `latest` | the newest event time handled | a question is posted or resolved, and only ever forward | on start, catch-up asks Squadron for questions after it | so a restart posts questions and answers missed while disconnected, and doesn't post earlier ones again |
| `missionThreads` | mission ID → request thread | `/squadron` starts a run; a run's post finds or opens its thread; an unlinked run's question opens a thread | a run asks a question, posts, or sends a notification | questions and notifications carry only the mission ID, so this is the only way to find the run's thread |
| `keyThreads` | key → request thread | a post with a `thread` key opens or joins a thread, if that key isn't linked yet | a post from a run that isn't linked yet | a routed, resumed or externally started run has a new mission ID; its key is the only link back to the request |
| `requests` | request thread → channel, requester, mission line | `/squadron` starts a run (all three); a post opens a thread in its `channel` (channel only) | anything is posted in the thread, and on every `root` rewrite | the thread may live in any channel or a DM, and a mission rewriting the top-level message doesn't know the gateway's own lines, so they're put back each time |
| `messages` | tool call ID → posted question (channel, ts) | a question is posted | the same question arrives again; it's answered | stops a replayed question posting twice, and lets the answer edit the question in place. In memory only, so a question posted before a restart isn't edited when it's answered |

Threads with no `requests` entry are in the configured channel. Threads with no
requester or mission line (not started from `/squadron`) get no gateway lines.

## Slack IDs

- Question buttons and selects use `sq::<tool_call_id>::<choice>`, with
  `__select__` for a multi-select and `__answer__` for the Answer button.
  IDs are cut to 255 bytes.
- The start form's callback ID is `sq-start`. Each input's block ID is
  `sq-in::<type>::<name>`, and its private metadata holds the picked mission
  and the channel `/squadron` was run in.
- The Answer modal's callback ID is `sq-answer`.

## Local development

Point your config at a locally built binary:

```hcl
gateway "slack" {
  version  = "local"   # skips the GitHub download; omit source
  settings = { ... }
}
```

```bash
go build -o gateway .
mkdir -p ~/.squadron/gateways/darwin-arm64/slack/local
mv gateway ~/.squadron/gateways/darwin-arm64/slack/local/gateway
```

Use your platform's `GOOS-GOARCH` in the path.

## Preview messages without Slack credentials

Run the real message handlers against local fake Slack and MCP servers:

```bash
SQUADRON_SLACK_PREVIEW="$PWD/slack-calls.json" \
  go test -run '^TestMockSlackPreview$' -count=1 -v
```

The output records outgoing API payloads for externally started and slash-command
started cases, including root edits, threaded replies, questions, failures, and
checkpoint reload. No Slack app or model is contacted. The JSON `checks` report
whether the cases kept one thread. A passing walkthrough means the capture
completed; false checks remain visible as behavior to investigate. The recorder
supplies mission events and answers directly to the gateway. Use a running
Squadron instance and a Slack test app to exercise mission execution and Socket
Mode delivery. Keep the generated JSON outside version control.
