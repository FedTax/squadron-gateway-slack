# Technical reference

## `builtins.gateway.post` payload

| Field     | Type    | Meaning |
| --------- | ------- | ------- |
| `text`    | string  | Required. The message, in Slack mrkdwn. |
| `blocks`  | array   | Optional Block Kit blocks. `text` is still sent with them. |
| `thread`  | string  | Optional key naming the request, e.g. a ticket key. Runs that post under the same key share one thread. |
| `root`    | boolean | Replace the request's top-level message with this post instead of replying. Posts it if the request has none yet. |
| `channel` | string  | Optional channel name or ID. The post goes there top-level, and `thread` and `root` are ignored. |

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
4. **A post with no linked run or key** becomes a new top-level message in the
   configured channel. Its run and key are linked to it.
5. **A question from an unlinked run** (`HumanInputRecord.MissionID`) posts a
   top-level message of `*<mission>*: <question summary>` in the configured
   channel, links the run to it, and posts the question as its first reply.
6. **A notification** posts in its run's thread, and is dropped if the run has
   none. A notification with its own `channel` posts top-level there.

A run must therefore post under its key before it asks anything, or its
question opens a second top-level message (case 5).

A question's `short_summary` is shown as its heading only when the question
itself is top-level. `PostMessageRequest.TaskID` is received but not used.

## Saved state

The checkpoint file (`checkpoint_path`) holds:

- `latest`: the newest event timestamp processed. On restart the gateway asks
  Squadron for events after it, so nothing missed while disconnected is lost.
- mission ID → request thread, and key → request thread.
- per request thread: the requester, the mission line, and its channel when
  that isn't the configured one (another channel, or a DM).

A missing or unreadable file starts empty.

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
