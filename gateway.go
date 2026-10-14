package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

type slackGateway struct {
	api gatewaysdk.SquadronAPI

	mu        sync.Mutex
	client    *slack.Client
	socket    *socketmode.Client
	channelID string
	botUserID string

	// tool_call_id → posted message, so we can edit the message when the
	// request resolves (regardless of which surface resolved it).
	messages map[string]postedMessage

	// mission id → request thread ts, thread key → request thread ts, and
	// request thread ts → the request started there. Persisted in the
	// checkpoint; see threads.go.
	missionThreads map[string]string
	keyThreads     map[string]string
	requests       map[string]request

	// run_url: the run page link template, with {mission_id}.
	runURL string

	// nil when mcp_url isn't set: missions can't be started from Slack and
	// runs the gateway didn't start can't be traced to their thread.
	mcp *squadronMCP

	checkpointPath string
	latest         time.Time

	// cancels the socket-mode run loop on Shutdown.
	cancel context.CancelFunc
	done   chan struct{}
}

func newSlackGateway() *slackGateway {
	return &slackGateway{
		messages:       map[string]postedMessage{},
		missionThreads: map[string]string{},
		keyThreads:     map[string]string{},
		requests:       map[string]request{},
	}
}

func (g *slackGateway) Configure(ctx context.Context, settings map[string]string, api gatewaysdk.SquadronAPI) error {
	g.api = api

	botToken := strings.TrimSpace(settings["bot_token"])
	appToken := strings.TrimSpace(settings["app_token"])
	channelID := strings.TrimSpace(settings["channel_id"])
	channelName := strings.TrimPrefix(strings.TrimSpace(settings["channel_name"]), "#")
	if botToken == "" {
		return fmt.Errorf("missing setting: bot_token")
	}
	if !strings.HasPrefix(botToken, "xoxb-") {
		return fmt.Errorf("bot_token must start with xoxb-; got prefix %q", tokenPrefix(botToken))
	}
	if appToken == "" {
		return fmt.Errorf("missing setting: app_token")
	}
	if !strings.HasPrefix(appToken, "xapp-") {
		return fmt.Errorf("app_token must start with xapp-; got prefix %q", tokenPrefix(appToken))
	}
	if channelID == "" && channelName == "" {
		return fmt.Errorf("missing setting: one of channel_id or channel_name is required")
	}
	if channelID != "" && channelName != "" {
		return fmt.Errorf("conflicting settings: set channel_id or channel_name, not both")
	}
	g.checkpointPath = strings.TrimSpace(settings["checkpoint_path"])
	if g.checkpointPath == "" {
		g.checkpointPath = ".squadron-slack-gateway.json"
	}
	if mcpURL := strings.TrimSpace(settings["mcp_url"]); mcpURL != "" {
		g.mcp = &squadronMCP{url: mcpURL, token: strings.TrimSpace(settings["mcp_token"])}
	}
	g.runURL = strings.TrimSpace(settings["run_url"])
	g.loadCheckpoint()

	api2 := slack.New(botToken, slack.OptionAppLevelToken(appToken))

	// Verify auth and stash the bot's own user id so we can ignore
	// our own thread replies in the message handler.
	authTest, err := api2.AuthTestContext(ctx)
	if err != nil {
		return fmt.Errorf("slack: auth test: %w", err)
	}

	if channelID == "" {
		resolved, err := resolveChannelByName(ctx, api2, channelName)
		if err != nil {
			return err
		}
		channelID = resolved
		log.Printf("resolved channel_name=%q to channel_id=%s", channelName, channelID)
	}

	socket := socketmode.New(api2)

	g.mu.Lock()
	g.client = api2
	g.socket = socket
	g.channelID = channelID
	g.botUserID = authTest.UserID
	g.mu.Unlock()

	runCtx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.done = make(chan struct{})

	go g.runSocketEvents(runCtx)
	go func() {
		defer close(g.done)
		if err := socket.RunContext(runCtx); err != nil && runCtx.Err() == nil {
			log.Printf("socket-mode run loop exited: %v", err)
		}
	}()

	if err := g.catchUp(ctx); err != nil {
		// Best-effort: a missed catch-up doesn't block live events.
		log.Printf("catch-up failed: %v", err)
	}

	log.Printf("slack gateway ready (channel=%s, bot=%s)", channelID, authTest.UserID)
	return nil
}

func (g *slackGateway) OnHumanInputRequested(ctx context.Context, rec gatewaysdk.HumanInputRecord) error {
	return g.postQuestion(ctx, rec)
}

func (g *slackGateway) OnHumanInputResolved(ctx context.Context, rec gatewaysdk.HumanInputRecord) error {
	g.advanceCheckpoint(rec.ResolvedAt)
	return g.markResolved(ctx, rec)
}

func (g *slackGateway) OnNotification(ctx context.Context, rec gatewaysdk.NotificationRecord) error {
	return g.postNotification(ctx, rec)
}

func (g *slackGateway) PostMessage(ctx context.Context, req gatewaysdk.PostMessageRequest) error {
	return g.postMessage(ctx, req)
}

func (g *slackGateway) MessageToolSpec(ctx context.Context) (gatewaysdk.MessageToolSpec, error) {
	return gatewaysdk.MessageToolSpec{
		Description:  slackPostDescription,
		ParamsSchema: slackPostSchema,
	}, nil
}

func (g *slackGateway) Shutdown(ctx context.Context) error {
	if g.cancel != nil {
		g.cancel()
	}
	if g.done != nil {
		select {
		case <-g.done:
		case <-ctx.Done():
		}
	}
	return nil
}

// catchUp replays everything since the local checkpoint through the
// live-event handlers. Per-row failures are logged so one bad row
// doesn't block the rest.
func (g *slackGateway) catchUp(ctx context.Context) error {
	g.mu.Lock()
	since := g.latest
	g.mu.Unlock()
	rows, _, err := g.api.ListHumanInputs(ctx, gatewaysdk.HumanInputFilter{
		Since:       since,
		OldestFirst: true,
		Limit:       200,
	})
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.State == gatewaysdk.HumanInputStateResolved {
			if err := g.markResolved(ctx, r); err != nil {
				log.Printf("catch-up resolved %s: %v", r.ToolCallID, err)
			}
			g.advanceCheckpoint(r.ResolvedAt)
			continue
		}
		if err := g.postQuestion(ctx, r); err != nil {
			log.Printf("catch-up post %s: %v", r.ToolCallID, err)
		}
		g.advanceCheckpoint(r.RequestedAt)
	}
	return nil
}

// tokenPrefix returns the leading slug of a token for error messages
// without leaking the secret. Slack tokens are xoxb-XXXX-… style; we
// slice up to (but not including) the first `-` after the kind tag.
func tokenPrefix(s string) string {
	if len(s) >= 5 {
		return s[:5]
	}
	return s
}
