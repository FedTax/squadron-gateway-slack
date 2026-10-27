// Squadron Slack gateway. Starts missions from /squadron and groups posts,
// questions and notifications by mission thread (a chain of related runs)
// into Slack conversations. Choice questions get buttons or a select plus
// Other; free-text questions get an Answer modal.
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
//	run_url         = "https://…/{mission_id}"         // links the mission name to its run
//
// The checkpoint stores the latest processed question event time and
// conversation records for catch-up and message routing after a restart.
package main

import "github.com/mlund01/squadron-gateway-sdk"

func main() {
	gateway.Serve(newSlackGateway())
}
