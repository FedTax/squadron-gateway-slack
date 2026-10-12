package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// squadronMCP talks to squadron's MCP host. The gateway SDK only lets a
// gateway list and resolve questions, so starting a mission and reading a
// run's inputs go through MCP instead.
type squadronMCP struct {
	url   string
	token string
}

// startMission calls run_mission and returns the new mission id.
func (s *squadronMCP) startMission(ctx context.Context, name string, inputs map[string]any) (string, error) {
	text, err := s.callTool(ctx, "run_mission", map[string]any{"name": name, "inputs": inputs})
	if err != nil {
		return "", err
	}
	var out struct {
		MissionID string `json:"missionId"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return "", fmt.Errorf("run_mission result: %w", err)
	}
	if out.MissionID == "" {
		return "", fmt.Errorf("run_mission returned no mission id")
	}
	return out.MissionID, nil
}

type runDetails struct {
	MissionName string         `json:"missionName"`
	Inputs      map[string]any `json:"inputs"`
}

// getRunDetails calls get_run_details for the run's mission name and inputs.
func (s *squadronMCP) getRunDetails(ctx context.Context, missionID string) (runDetails, error) {
	text, err := s.callTool(ctx, "get_run_details", map[string]any{"run_id": missionID})
	if err != nil {
		return runDetails{}, err
	}
	var out runDetails
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return runDetails{}, fmt.Errorf("get_run_details result: %w", err)
	}
	return out, nil
}

// callTool opens a short-lived session per call: these calls are rare (one
// per started mission or unknown run), so holding a session open would only
// add reconnect handling.
func (s *squadronMCP) callTool(ctx context.Context, tool string, args map[string]any) (string, error) {
	var opts []transport.StreamableHTTPCOption
	if s.token != "" {
		opts = append(opts, transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + s.token}))
	}
	c, err := client.NewStreamableHttpClient(s.url, opts...)
	if err != nil {
		return "", fmt.Errorf("mcp client: %w", err)
	}
	defer c.Close()
	if err := c.Start(ctx); err != nil {
		return "", fmt.Errorf("mcp start: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "squadron-gateway-slack"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return "", fmt.Errorf("mcp initialize: %w", err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	text := resultText(res)
	if res.IsError {
		return "", fmt.Errorf("%s: %s", tool, text)
	}
	return text, nil
}

func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := mcp.AsTextContent(c); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}
