// Squadron Slack gateway. Starting a mission from /squadron posts a
// message, and the run's questions, gateway.post messages and
// notifications go in that message's thread. Choice questions get
// buttons or a select; free-text questions get an Answer modal.
//
// Required HCL settings:
//
//	gateway "slack" {
//	  source  = "github.com/mlund01/squadron-gateway-slack"
//	  version = "vX.Y.Z"
//	  settings = {
//	    bot_token  = vars.slack_bot_token   // xoxb-…
//	    app_token  = vars.slack_app_token   // xapp-… (Socket Mode)
//	    channel_id = "C0123456789"
//	  }
//	}
//
// Optional settings:
//
//	checkpoint_path = "/abs/path/to/checkpoint.json"   // default: ./.squadron-slack-gateway.json
//
// The checkpoint file holds the latest event timestamp the gateway has
// processed, so a restart asks squadron for everything since then and
// transient disconnects don't drop events, plus each request's thread so
// later posts still find it.
package main

import "github.com/mlund01/squadron-gateway-sdk"

func main() {
	gateway.Serve(newSlackGateway())
}
