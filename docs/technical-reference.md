# Technical reference

## `builtins.gateway.post` payload

| Field     | Type    | Meaning |
| --------- | ------- | ------- |
| `text`    | string  | Required. The message, in Slack mrkdwn. |
| `blocks`  | array   | Optional Block Kit blocks. `text` is still sent with them. |
| `root`    | boolean | Replace the request's top-level message with this post instead of replying. Posts it if the request has none yet. |
| `channel` | string  | Optional channel name or ID to open the request's thread in. Used only when the mission thread has no Slack conversation yet; otherwise the existing thread's channel wins. A name needs `channels:read`/`groups:read`, and one that can't be found falls back to the default channel. |

Files a mission attaches to the post are uploaded into the same thread.

On a `root` post the gateway appends its own lines after `text`: the mission
line and `Requester: <@user>`, for requests started from `/squadron`. With
`blocks`, they're added as a final `context` block.

Every post returns the ID of the message it created or replaced, as
`<channel>/<ts>`: the new top-level message, the reply, or the rewritten
top-level message. Slack only guarantees a `ts` is unique within one
conversation, so the ID carries the channel too.

## `builtins.gateway.edit` payload

`id` is an ID a post returned. `text` and `blocks` are as for a post and
replace the whole message, through `chat.update` on that channel and `ts`.
`attachments` are as for a post: the files are uploaded into the mission thread's
Slack conversation, or under the edited message if there is none. They're added; files
posted earlier stay.

- **The request's top-level message:** a `/squadron` start's mission and
  requester lines are added back, as on a `root` post.
- **Refused:** a question's message, because the gateway rewrites it itself
  when it's answered; a payload with `channel` or `root`, because an
  edit can't move a message; and an ID that isn't `<channel>/<ts>`.
- **Not kept:** the gateway doesn't store post IDs. A later run that needs to
  edit a message has to be given its ID, e.g. from a memory file the mission
  writes it to.

## How related missions share a conversation

Squadron's `ThreadID` identifies a **mission thread: a chain of related mission
runs**. Each run retains its own `MissionID` for run URLs, attribution and question
ownership. The mission thread ID is independent of Slack's channel/timestamp
reference.

Squadron generates a UUID when a mission starts without a supplied thread ID.
Automatic cross-mission routing inherits it, and resuming retains it. To start a
separate follow-up in the same conversation, pass the existing ID as
`thread_id` to MCP `run_mission`. Its response includes `missionId` and `threadId`.
The agent does not need to post or call another tool before asking a question.

Posts, edits, questions and notifications carry the mission thread ID outside
the agent-authored payload. The gateway uses it to find one Slack conversation:

1. A `/squadron` start reserves a mission thread ID and supplies it to
   `run_mission`. Events for that thread wait until the gateway posts and records
   the starter message in the selected channel or DM.
2. An existing conversation receives replies, even if a later mission supplies
   a different channel override.
3. For other starts, the first post or notification becomes the root, in its
   channel override or the configured default channel. A first question creates
   a summary root in the default channel and posts the question beneath it.
4. `root: true` replaces the complete root content. It does not return the
   previous content to the agent. Missions can keep shared content in an
   explicitly configured memory and use it to compose later replacements.

Root lookup, creation and recording are serialized per mission thread, so
concurrent first messages share a root while unrelated threads proceed
independently. A failed Slack request returns an error. If Slack accepts a root
but its response is lost, the gateway cannot record its ID; a later attempt may
create another root. The lock does not provide exactly-once delivery across
network failures.

## State

The checkpoint saves `latest` and `conversations` when they change. A missing or
unreadable file starts them empty. Entries are retained until the checkpoint is
removed.

| State | Purpose |
| ----- | ------- |
| `latest` | Newest processed question event time, used for catch-up. Only moves forward. |
| `conversations` | Mission thread ID → Slack root reference and optional starter mission/requester lines. Keeps related runs in the same conversation after gateway restarts and preserves attribution on root replacements. |
| `messages` | Tool call ID → posted question's channel and timestamp. Prevents duplicate questions within the process and locates them for answer edits. In memory only; questions posted before restart cannot be edited from this map. |

Only conversations started through `/squadron` have gateway-owned mission and
requester lines. Root replacements preserve these lines; the mission ID in the
run URL still identifies the original run.

## Slack IDs

- Question buttons and selects use `sq::<tool_call_id>::<choice>`, with
  `__select__` for a multi-select and `__answer__` for the Answer button.
  IDs are cut to 255 bytes.
- The start form's callback ID is `sq-start`. Each input's block ID is
  `sq-in::<type>::<name>`, and its private metadata holds the picked mission
  and the channel `/squadron` was run in.
- The Answer modal's callback ID is `sq-answer`. A choice question's Other
  button opens it with the same `__answer__` action ID. On a multi-select
  question the button's value is `multi`, and the typed answer is sent as a
  one-item JSON array, the shape the agent expects from a multi-select.

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
started cases, including root edits, threaded replies, edits by ID (and a
refused edit of a question), questions, failures, and
checkpoint reload. No Slack app or model is contacted. The JSON `checks` report
whether the cases kept one thread. A passing walkthrough means the capture
completed; false checks remain visible as behavior to investigate. The recorder
supplies mission events and answers directly to the gateway. Use a running
Squadron instance and a Slack test app to exercise mission execution and Socket
Mode delivery. Keep the generated JSON outside version control.
