# Technical reference

## `builtins.gateway.post` payload

| Field     | Type    | Meaning |
| --------- | ------- | ------- |
| `text`    | string  | Required. The message, in Slack mrkdwn. |
| `blocks`  | array   | Optional Block Kit blocks. `text` is still sent with them. |
| `thread`  | string  | Optional key naming the request, e.g. a ticket key. Runs that post under the same key share one thread. |
| `root`    | boolean | Replace the request's top-level message with this post instead of replying. Posts it if the request has none yet. |
| `channel` | string  | Optional channel name or ID to open the request's thread in. Used only when neither the run nor `thread` is linked to a thread yet; otherwise the existing thread's channel wins. A name needs `channels:read`/`groups:read`, and one that can't be found falls back to the default channel. |

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

- **The request's top-level message:** a `/squadron` start's mission and
  requester lines are added back, as on a `root` post.
- **Refused:** a question's message, because the gateway rewrites it itself
  when it's answered; a payload with `thread`, `channel` or `root`, because an
  edit can't move a message; and an ID that isn't `<channel>/<ts>`.
- **Not kept:** the gateway doesn't store post IDs. A later run that needs to
  edit a message has to be given its ID, e.g. from a memory file the mission
  writes it to.

## How a run finds its thread

A request thread's ID is its channel plus the `ts` of its top-level message,
written `<channel>/<ts>`. The `ts` is what `chat.postMessage` returns for the
posted message, and what the gateway passes as `thread_ts` on every reply.
Slack's docs only guarantee a `ts` is unique within one channel or conversation
([Retrieving messages](https://docs.slack.dev/messaging/retrieving-messages)),
so the channel is part of the ID. All the state below points at thread IDs,
and they're saved in the checkpoint, so they survive restarts.

Questions and notifications carry only the run's mission ID. Posts carry the
mission ID and, optionally, a `thread` key the mission chooses.

**What the gateway does** with each message:

1. **`/squadron` start.** MCP `run_mission` returns the run's mission ID. The
   gateway posts the top-level message and links the mission ID to it.
2. **A post, question or notification from a linked run** replies in that
   run's thread. A post's key that isn't linked yet is linked to the thread
   too.
3. **A post from an unlinked run, with a linked key,** links the run to the
   key's thread and replies there. Squadron sends the posting run's mission ID
   with each post (`PostMessageRequest.MissionID`). A routed, resumed or
   separately started run has a new mission ID, so this is how it joins an
   existing request.
4. **A post from an unlinked run, with no linked key,** posts a new top-level
   message in the post's `channel`, or the default channel. The run and key
   are linked to it.
5. **A question from an unlinked run** posts a new top-level message,
   `*<mission>*: <question summary>`, in the default channel. The run is
   linked to it, and the question is posted as the first reply in its thread.
6. **A notification from an unlinked run** posts a new top-level message in
   the `channel` set on the mission's `notification { gateway { … } }` block,
   or the default channel. The run is linked to it.
7. **`builtins.gateway.link` with a key** (`Link`) posts nothing. If a request
   is filed under the key, the run is linked to it, even if the run already
   had a thread. Otherwise, if the run has a thread, the key is filed under
   it. If neither has one, it returns an error and links nothing.

**What the mission must do:** for a run to join an existing request, it must
call `builtins.gateway.link` with that request's key, or post with the key as
its `thread`, before it asks or notifies anything. A question or notification
carries no key, so if one comes first the gateway can't tell which request the
run belongs to. It opens a new top-level message for the run (case 5 or 6). A
later link moves the run to the request, but that message stays where it was.

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
| `missionThreads` | mission ID → request thread ID | `/squadron` starts a run; a run's post finds or opens its thread; an unlinked run's question or notification opens a thread | a run asks a question, posts, or sends a notification | questions and notifications carry only the mission ID, so this is the only way to find the run's thread |
| `keyThreads` | key → request thread ID | a post with a `thread` key opens or joins a thread, if that key isn't linked yet | a post from a run that isn't linked yet | a routed, resumed or externally started run has a new mission ID; its key is the only link back to the request |
| `requests` | request thread ID → requester, mission line | `/squadron` starts a run | on every `root` rewrite | a mission rewriting the top-level message doesn't know the gateway's own lines, so they're put back each time |
| `messages` | tool call ID → posted question (channel, ts) | a question is posted | the same question arrives again; it's answered | stops a replayed question posting twice, and lets the answer edit the question in place. In memory only, so a question posted before a restart isn't edited when it's answered |

Only `/squadron` starts have a `requests` entry. A `root` rewrite replaces the
whole message with the mission's content, so the gateway adds the requester and
mission lines back each time. Without the entry they'd be lost on the first
rewrite. A thread opened any other way never had gateway lines, so its
rewrites get none.

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
