# squadron-gateway-slack

A Squadron gateway that runs missions out of one Slack channel. Each
request gets a single top-level message — the one the gateway posts
when someone starts a mission with `/squadron`, or one it posts the first time a run
needs to say something — and everything else about it goes in that
message's thread: `builtins.human.ask` questions (answered with buttons, a
multi-select dropdown, or an Answer modal), `builtins.gateway.post`
posts, and lifecycle notifications. Whoever started the request is
tagged on each of them.

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

### 5. Invite the bot to your channel

In Slack, in the channel where questions should land:

```
/invite @squadron
```

Then grab the channel ID: click the channel name → **About** → bottom
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
The gateway SDK only lets a gateway list and answer questions, so the
gateway uses MCP to start missions (`run_mission`) and to find the thread
of a run it didn't start (`get_run_details`). Without it, questions and
posts still work, but missions can't be started from Slack.

Restart squadron. The log should show `gateway "slack" started` and
something like `slack gateway ready (channel=C0123…, bot=U0123…)`.
The next `builtins.human.ask` call lands in your channel.

## Settings reference

| Setting           | Required | Notes                                                                 |
| ----------------- | -------- | --------------------------------------------------------------------- |
| `bot_token`       | yes      | Bot User OAuth Token (`xoxb-…`). Use a `secret` variable.             |
| `app_token`       | yes      | App-Level Token (`xapp-…`) with `connections:write`. Use a `secret` variable. |
| `channel_id`      | one of   | Slack channel ID (e.g. `C0123456789`).                                |
| `channel_name`    | one of   | Channel name (e.g. `general` or `#general`). Resolved at startup; needs `channels:read` / `groups:read`. |
| `checkpoint_path` | optional | Where the gateway persists the last event timestamp it processed and each mission's request thread. Defaults to `./.squadron-slack-gateway.json`. |
| `mcp_url`         | optional | Squadron MCP host endpoint, e.g. `http://localhost:8090/mcp`. Needed to start missions from Slack. |
| `mcp_token`       | optional | The MCP host's `secret`, sent as a Bearer token. Use a `secret` variable. |
| `threads`         | optional | JSON object of mission name → `{"key": …, "title": …}` templates; see [Keeping a request in one thread](#keeping-a-request-in-one-thread). |

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
top-level message in the configured channel, tagging you. Everything
the run says afterwards goes in that message's thread. If the run can't
start, only you see the error.

## Keeping a request in one thread

Each request gets one top-level message and everything else goes in its
thread. Questions and notifications reach the gateway with their mission
id. A run the gateway started is already mapped to its thread. Any other
run (chained, resumed, or started elsewhere) is placed by its **thread
key**. Squadron records no link between a mission and the one it routes
to, so the key is how a later run finds the request it belongs to.

Keys and titles are set per mission in the `threads` setting, as
[text/template](https://pkg.go.dev/text/template) strings over the
run's inputs:

```hcl
threads = <<-EOT
  {
    "rate_triage":   {"key": "{{.issue}}", "title": "{{.issue}}: rate ticket"},
    "rate_fix":      {"key": "{{.issue}}", "title": "{{.issue}}: rate ticket"},
    "rate_finalize": {"key": "{{.issue}}", "title": "{{.issue}}: rate ticket"}
  }
EOT
```

- Runs whose keys render the same share a thread, so prefix a key when
  unrelated missions could render the same value.
- `title` is the top-level message when the gateway has to start the
  thread itself (the run's first question arrived before any thread
  existed). Without one, the gateway posts `<mission>: <question summary>`.
- A key that references an input the run doesn't have isn't used, and
  the run gets a thread of its own.
- For `builtins.gateway.post`, set `thread` to the request's key (e.g.
  the ticket key). The post replies in that thread, or becomes the
  request's top-level message when it has none yet.
- Starting a request for a key that already has a thread moves the key
  to the new thread.

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
