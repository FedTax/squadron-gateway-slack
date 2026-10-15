# squadron-gateway-slack

A Squadron gateway for Slack. Run `/squadron` in any channel the bot is in, or
in a DM with the app, to start a mission. Starting a mission posts a message,
and everything the run sends after that goes in that message's thread. What a
post says is up to the mission.

## Setup

1. Create a Slack app from this manifest:

   ```json
   {
     "display_information": { "name": "squadron" },
     "features": {
       "app_home": {
         "home_tab_enabled": false,
         "messages_tab_enabled": true,
         "messages_tab_read_only_enabled": false
       },
       "bot_user": { "display_name": "squadron", "always_online": true },
       "slash_commands": [
         { "command": "/squadron", "description": "Start a Squadron mission", "should_escape": false }
       ]
     },
     "oauth_config": {
       "scopes": { "bot": ["chat:write", "commands", "files:write"] }
     },
     "settings": {
       "interactivity": { "is_enabled": true },
       "socket_mode_enabled": true,
       "token_rotation_enabled": false
     }
   }
   ```

   - The Messages tab settings let you run `/squadron` in a DM with the app.
   - `files:write` is only needed if missions attach files to their posts.
   - Add `channels:read` and `groups:read` only if you set `channel_name`
     instead of `channel_id`, or missions name a channel by name (in a post or
     a `notification` block).

2. Create an app-level token with the `connections:write` scope and store it:

   ```bash
   squadron vars set slack_app_token <xapp-…>
   ```

3. Install the app to your workspace and store its bot token:

   ```bash
   squadron vars set slack_bot_token <xoxb-…>
   ```

4. Invite the bot to each channel you'll use: `/invite @squadron`.

5. Pick a default channel and store its ID. A run posts there when it wasn't
   started from Slack and its mission doesn't name a channel:

   ```bash
   squadron vars set slack_channel_id C0123456789
   ```

6. Add the gateway and Squadron's MCP host to your config:

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

   Without `mcp_url`, questions and posts still work, but missions can't be
   started from Slack.

7. Restart Squadron. The gateway logs
   `slack gateway ready (channel=C0123…, bot=U0123…)` once it's connected.

## Settings

| Setting           | Required | Notes |
| ----------------- | -------- | ----- |
| `bot_token`       | yes      | Bot token (`xoxb-…`). Use a `secret` variable. |
| `app_token`       | yes      | App-level token (`xapp-…`) with `connections:write`. Use a `secret` variable. |
| `channel_id`      | one of   | Default channel ID (e.g. `C0123456789`), for runs not started from Slack whose mission doesn't name a channel. |
| `channel_name`    | one of   | The default channel by name (e.g. `general`). Looked up at startup, so renaming the channel breaks it. |
| `checkpoint_path` | optional | Where the gateway saves its state across restarts. Defaults to `./.squadron-slack-gateway.json`. |
| `mcp_url`         | optional | Squadron MCP host endpoint, e.g. `http://localhost:8090/mcp`. Needed to start missions from Slack. |
| `mcp_token`       | optional | The MCP host's `secret`. Use a `secret` variable. |
| `run_url`         | optional | Link to a run's page, with `{mission_id}` where the run id goes. The mission name on the top-level message links there. |

## Starting a mission

1. Run `/squadron` in a channel the bot is in, or in your DM with the app to
   keep the request private.
2. Pick a mission. The form shows one field per input, with its description and
   default. Required inputs must be filled in. Enter non-string inputs as JSON,
   e.g. `["CO", "PA"]`.
3. Click **Start**. If Squadron rejects an input, the error shows on that field
   and the form stays open. Any other start error is sent only to you.

**Limitation:** protected inputs aren't on the form and can't be set from Slack.
A mission started from Slack only works if its config gives every protected
input its `value`.

## A request from start to finish

1. **It starts.**
   - From Slack: you run `/squadron` (see [Starting a mission](#starting-a-mission)).
     The top-level message is posted right away, in the channel or DM you ran
     it in.
   - From Squadron (the command center, a schedule, a route): nothing is posted
     until the run first sends something. That first message becomes the
     top-level message. It goes in the channel the mission names, or in the
     default channel if it names none. A question can't name a channel, so a
     run whose first message is a question opens in the default channel.
2. **The top-level message.** For a `/squadron` start, the gateway puts two
   lines on it: the mission name, linked to the run if `run_url` is set
   (otherwise the name and run id), and `Requester: @you`. For a start in
   Squadron, it's the run's first message. As the work moves on, the mission
   can replace the message's content. The gateway keeps its lines on it.
3. **While it runs**, the run's posts and questions are replies in that
   message's thread. The mission can edit any of its own posts later, e.g. to
   tick off a step, but not a question. Answer questions there (see
   [Answering questions](#answering-questions)).
4. **When it ends**, whatever the run posts last, and any notification from the
   mission's `notification { gateway { … } }` block (`mission_completed`,
   `mission_failed`), arrive in the same thread.
5. **Follow-up runs.** Missions that work on the same request can share a
   `thread` key (e.g. a ticket key) on their posts. A later run that posts
   under that key replies in the request's existing thread instead of opening
   a new top-level message. A run that may ask a question before it posts
   calls `builtins.gateway.link` with the key first; see the
   [technical reference](docs/technical-reference.md).

## Answering questions

- A question with choices has a button per choice, or a dropdown if it allows
  several. Its **Other…** button opens a text box for an answer that isn't
  listed.
- A free-text question has an **Answer** button that opens a text box.
- Replies typed in the thread are never read as answers. That's deliberate:
  the thread can hold discussion and a record of decisions without any of it
  being taken as an answer, and a reply couldn't say which of several open
  questions it answers. Answers go only through the buttons, the dropdown or
  the text box.
- Once a question is answered, here or anywhere else (e.g. the command center),
  its message is edited: the question is struck through, followed by the answer
  and who gave it.

## See also

- [Technical reference](docs/technical-reference.md): the `gateway.post`
  payload, how runs find their thread, saved state, and local development.
- [`squadron-gateway-sdk`](https://github.com/mlund01/squadron-gateway-sdk):
  the Go SDK for building a gateway for another system.
- [`squadron-gateway-discord`](https://github.com/mlund01/squadron-gateway-discord):
  the same protocol for Discord.
