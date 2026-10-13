# squadron-gateway-slack

A Squadron gateway for Slack. Starting a mission posts a message, and
everything the run sends after that goes in that message's thread:
`builtins.human.ask` questions (answered with buttons, a multi-select
dropdown, or an Answer modal), `builtins.gateway.post` posts, and
lifecycle notifications. What a post says is whatever the mission sends.

## Setup walkthrough

You'll create a Slack app, give it two tokens (one bot, one app-level
for Socket Mode), invite it to a channel, then point your squadron
config at it. About 5 minutes end-to-end.

### 1. Create the Slack app

1. Go to <https://api.slack.com/apps> → **Create New App** → **From
   scratch**.
2. Name it `squadron` (or whatever you like) and pick your workspace.

### 2. Enable Socket Mode and grab the app-level token

Socket Mode is how the gateway receives events without exposing a
public URL.

1. In the app's left sidebar: **Socket Mode** → toggle **Enable Socket
   Mode**.
2. Slack will prompt for an app-level token. Name it `squadron-socket`
   and add the scope `connections:write`. Click **Generate**.
3. Copy the token (starts with `xapp-`) and stash it in squadron:

   ```bash
   squadron vars set slack_app_token <xapp-…>
   ```

### 3. Configure bot scopes and install

1. Sidebar: **OAuth & Permissions** → scroll to **Scopes** → **Bot
   Token Scopes** → **Add an OAuth Scope** for each of:

   - `chat:write` — post the question messages
   - `commands` — receive the `/squadron` command
   - `channels:read` — only needed if you'll use `channel_name` instead of `channel_id`
   - `groups:read` — same, for private channels
   - `files:write` — **only needed if agents will attach files** via the `builtins.gateway.post` tool's `attachments` (squadron reads the referenced memory/scratchpad file and the gateway uploads it). Skip it if you don't use attachments.

2. Scroll up → **Install to Workspace** → **Allow**.
3. After install, the page shows a **Bot User OAuth Token** (starts
   with `xoxb-`). Copy it and stash:

   ```bash
   squadron vars set slack_bot_token <xoxb-…>
   ```

### 4. Create the `/squadron` command

1. Sidebar: **Slash Commands** → **Create New Command**.
2. Command: `/squadron`. With Socket Mode on, no Request URL is needed.
3. Click **Save**. If Slack asks you to reinstall the app, reinstall it.
4. To run `/squadron` in a DM with the app: sidebar **App Home** →
   **Messages Tab** → turn it on and tick **Allow users to send Slash
   commands and messages from the messages tab** (manifest:
   `messages_tab_enabled: true`, `messages_tab_read_only_enabled: false`).

### 5. Invite the bot to your channels

In Slack, in each channel you'll use:

```
/invite @squadron
```

Then grab the ID of the channel that runs not started from Slack should
post to: click the channel name → **About** → bottom
of the panel shows `Channel ID: C0123456789`. Stash it:

```bash
squadron vars set slack_channel_id C0123456789
```

### 6. Wire it into your squadron config

```hcl
variable "slack_bot_token" {
  secret = true
}

variable "slack_app_token" {
  secret = true
}

variable "slack_channel_id" {}

gateway "slack" {
  source  = "github.com/mlund01/squadron-gateway-slack"
  version = "v0.0.1"

  settings = {
    bot_token       = vars.slack_bot_token
    app_token       = vars.slack_app_token
    channel_id      = vars.slack_channel_id
    checkpoint_path = "${path.cwd}/.squadron/slack-gateway.json"
    mcp_url         = "http://localhost:8090/mcp"
    mcp_token       = vars.mcp_host_secret
  }
}

mcp_host {
  enabled = true
  port    = 8090
  secret  = vars.mcp_host_secret
}
```

`mcp_url` points at squadron's own [MCP host](https://github.com/mlund01/squadron).
The gateway uses MCP to list missions and their inputs and to start them
(`list_missions`, `get_mission_config`, `run_mission`). Without it,
questions and posts still work, but missions can't be started from Slack.

Restart squadron. The log should show `gateway "slack" started` and
something like `slack gateway ready (channel=C0123…, bot=U0123…)`.
The next `builtins.human.ask` call from a run not started in Slack lands in
that channel.

## Settings reference

| Setting           | Required | Notes                                                                 |
| ----------------- | -------- | --------------------------------------------------------------------- |
| `bot_token`       | yes      | Bot User OAuth Token (`xoxb-…`). Use a `secret` variable.             |
| `app_token`       | yes      | App-Level Token (`xapp-…`) with `connections:write`. Use a `secret` variable. |
| `channel_id`      | one of   | Slack channel ID (e.g. `C0123456789`) for runs not started from Slack. |
| `channel_name`    | one of   | Channel name (e.g. `general` or `#general`). Resolved at startup; needs `channels:read` / `groups:read`. |
| `checkpoint_path` | optional | Where the gateway persists the last event timestamp it processed and each mission's request thread. Defaults to `./.squadron-slack-gateway.json`. |
| `mcp_url`         | optional | Squadron MCP host endpoint, e.g. `http://localhost:8090/mcp`. Needed to start missions from Slack. |
| `mcp_token`       | optional | The MCP host's `secret`, sent as a Bearer token. Use a `secret` variable. |
| `run_url`         | optional | Link to a run's page, with `{mission_id}` where the run id goes. The mission name on a request's top-level message links there. |

Set exactly one of `channel_id` or `channel_name`. `channel_id` is the
most stable handle — renaming a channel won't break the gateway.
`channel_name` is friendlier but pays a startup paginated REST round-
trip and breaks if the channel is renamed.

## Starting a mission

Run `/squadron` in any channel the bot is in. A form opens: pick a
mission, and it shows one field per input, using the names, types,
descriptions and defaults from the mission's declaration (squadron's MCP
`get_mission_config`). Required inputs must be filled in, and protected
inputs aren't shown. Values for non-string inputs are entered as JSON,
e.g. `["CO", "PA"]`.

On **Start**, the gateway runs the mission and posts the request's
top-level message in the channel you ran it in, naming the mission and
you as the requester. Everything the run says afterwards goes in that message's
thread. If squadron rejects an input, the error shows on that field and
the form stays open; any other start error is sent to you alone.

Run `/squadron` in a DM instead to keep the request private: its
top-level message, questions, updates and notifications all go to your
DM with the app.

## Threads

- A run started from `/squadron` replies under the message the gateway
  posts when it starts the run: the mission (linked to the run when
  `run_url` is set, otherwise with its run id) and `Requester: @…`. Slack has users follow threads whose parent mentions
  them, so replies don't tag the requester.
- `builtins.gateway.post` with `thread` set to a key (e.g. a ticket key)
  replies in that key's thread. A run that posts under a key joins that
  thread, so its later questions and notifications go there too. Squadron
  records no link between a mission and one it routes to, so this is how
  a chained or resumed run finds its thread.
- With `root: true`, the post replaces the thread's top-level message, or
  posts it when there is none yet. The gateway adds its mission and
  `Requester: @…` lines after the post's text on every rewrite.
- A run that asks a question before it has a thread gets a top-level
  message of `*<mission>*: <question summary>`. The question itself is a
  reply in that thread.
- A question in a thread shows without its summary heading. Once
  answered, it's struck through, followed by the answer and who gave it.
- A mission's `notification` block (`mission_completed`,
  `mission_failed` or `all`) posts in the run's thread, with the error
  when the run failed. A run with no thread posts nothing. A notification
  with its own `channel` posts top-level in that channel.

## What the operator sees

For a question with choices:

```
*Should I proceed with prod or staging?*
The schemas are identical except for the past 24 hours of writes.

[ prod ] [ staging ] [ both ]

`databricks_explore › discover`
```

Click a button to answer. For multi-select questions (`multi_select:
true` on the agent's tool call) the channel shows a multi-select
dropdown picker instead of buttons. For free-text questions, click
**Answer** and type the answer in the modal. Thread replies aren't read
as answers: a request thread can hold several open questions, and a
reply can't say which one it answers.

When a question is resolved (here, in Slack, in Command Center, or
anywhere else), the message is edited to strike through the question
and append the answer.

## Local development

If you want to hack on the gateway itself, point your squadron config
at a locally-built binary:

```hcl
gateway "slack" {
  version = "local"   # skips the GitHub download
  # source omitted intentionally
  settings = { ... }
}
```

Then build and install:

```bash
go build -o gateway .
mkdir -p ~/.squadron/gateways/darwin-arm64/slack/local
mv gateway ~/.squadron/gateways/darwin-arm64/slack/local/gateway
```

Adjust the platform path to match `runtime.GOOS-runtime.GOARCH`.

## See also

- [`squadron-gateway-discord`](https://github.com/mlund01/squadron-gateway-discord)
  — same protocol, different surface; useful as a reference.
- [`squadron-gateway-sdk`](https://github.com/mlund01/squadron-gateway-sdk)
  — the Go SDK if you want to build a gateway for some other system.

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
completed; false checks remain visible as behavior to investigate. The recorder supplies mission events and answers directly to the gateway. Use a
running Squadron instance and a Slack test app to exercise mission execution and
Socket Mode delivery. Keep the generated JSON outside version control.
