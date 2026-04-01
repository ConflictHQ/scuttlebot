// Package mcp implements a Model Context Protocol (MCP) server for scuttlebot.
//
// The server exposes scuttlebot tools to any MCP-compatible AI agent
// (Claude, Gemini, Codex, etc.). Transport: HTTP POST /mcp, JSON-RPC 2.0.
// Auth: Bearer token in the Authorization header (same tokens as REST API).
//
// Tools:
//   - get_status      — daemon health and agent count
//   - list_channels   — available IRC channels
//   - register_agent  — register an agent, return credentials
//   - send_message    — send a typed message to a channel
//   - get_history     — recent messages from a channel
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/conflicthq/scuttlebot/internal/registry"
)

// Sender can send a typed message to an IRC channel.
// Implement this with pkg/client.Client when the daemon has a relay connection.
type Sender interface {
	Send(ctx context.Context, channel, msgType string, payload any) error
}

// HistoryQuerier returns recent messages from a channel.
// Implement this with the scribe Store when wired into the daemon.
type HistoryQuerier interface {
	Query(channel string, limit int) ([]HistoryEntry, error)
}

// HistoryEntry is a single message from channel history.
type HistoryEntry struct {
	Nick        string `json:"nick"`
	MessageType string `json:"type,omitempty"`
	MessageID   string `json:"id,omitempty"`
	Raw         string `json:"raw"`
}

// ChannelLister lists IRC channels.
type ChannelLister interface {
	ListChannels() ([]ChannelInfo, error)
}

// ChannelInfo describes a single IRC channel.
type ChannelInfo struct {
	Name  string `json:"name"`
	Topic string `json:"topic,omitempty"`
	Count int    `json:"count"`
}

// Server is the MCP server.
type Server struct {
	registry *registry.Registry
	channels ChannelLister
	sender   Sender         // optional — send_message returns error if nil
	history  HistoryQuerier // optional — get_history returns error if nil
	tokens   map[string]struct{}
	log      *slog.Logger
}

// New creates an MCP Server.
func New(reg *registry.Registry, channels ChannelLister, tokens []string, log *slog.Logger) *Server {
	t := make(map[string]struct{}, len(tokens))
	for _, tok := range tokens {
		t[tok] = struct{}{}
	}
	return &Server{
		registry: reg,
		channels: channels,
		tokens:   t,
		log:      log,
	}
}

// WithSender attaches an IRC relay client for send_message.
func (s *Server) WithSender(sender Sender) *Server {
	s.sender = sender
	return s
}

// WithHistory attaches a history store for get_history.
func (s *Server) WithHistory(h HistoryQuerier) *Server {
	s.history = h
	return s
}

// Handler returns the HTTP handler for the MCP endpoint. Mount at /mcp.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	return s.authMiddleware(mux)
}

// --- Auth ---

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if _, ok := s.tokens[token]; !ok {
			writeRPCError(w, nil, -32001, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(v, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

// --- JSON-RPC 2.0 types ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	if req.JSONRPC != "2.0" {
		writeRPCError(w, req.ID, -32600, "invalid request")
		return
	}

	var result any
	var rpcErr *rpcError

	switch req.Method {
	case "initialize":
		result = s.handleInitialize()
	case "tools/list":
		result = s.handleToolsList()
	case "tools/call":
		result, rpcErr = s.handleToolCall(r.Context(), req.Params)
	case "ping":
		result = map[string]string{}
	default:
		rpcErr = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// --- MCP method handlers ---

func (s *Server) handleInitialize() any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "scuttlebot", "version": "0.1"},
	}
}

func (s *Server) handleToolsList() any {
	return map[string]any{"tools": toolDefs()}
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) handleToolCall(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p toolCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}

	var text string
	var err error

	switch p.Name {
	case "get_status":
		text, err = s.toolGetStatus()
	case "list_channels":
		text, err = s.toolListChannels()
	case "register_agent":
		text, err = s.toolRegisterAgent(p.Arguments)
	case "send_message":
		text, err = s.toolSendMessage(ctx, p.Arguments)
	case "get_history":
		text, err = s.toolGetHistory(p.Arguments)
	default:
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}

	if err != nil {
		// Tool errors are returned as content with isError flag, not RPC errors.
		return toolResult(err.Error(), true), nil
	}
	return toolResult(text, false), nil
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"isError": isError,
	}
}

// --- Tool implementations ---

func (s *Server) toolGetStatus() (string, error) {
	agents := s.registry.List()
	active := 0
	for _, a := range agents {
		if !a.Revoked {
			active++
		}
	}
	return fmt.Sprintf("status: ok\nagents: %d active, %d total", active, len(agents)), nil
}

func (s *Server) toolListChannels() (string, error) {
	if s.channels == nil {
		return "", fmt.Errorf("channel listing not available")
	}
	channels, err := s.channels.ListChannels()
	if err != nil {
		return "", fmt.Errorf("list channels: %w", err)
	}
	if len(channels) == 0 {
		return "no channels", nil
	}
	var sb strings.Builder
	for _, ch := range channels {
		if ch.Topic != "" {
			fmt.Fprintf(&sb, "%s (%d members) — %s\n", ch.Name, ch.Count, ch.Topic)
		} else {
			fmt.Fprintf(&sb, "%s (%d members)\n", ch.Name, ch.Count)
		}
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func (s *Server) toolRegisterAgent(args map[string]any) (string, error) {
	nick, _ := args["nick"].(string)
	if nick == "" {
		return "", fmt.Errorf("nick is required")
	}
	agentType := registry.AgentTypeWorker
	if t, ok := args["type"].(string); ok && t != "" {
		agentType = registry.AgentType(t)
	}
	var channels []string
	if ch, ok := args["channels"].([]any); ok {
		for _, c := range ch {
			if s, ok := c.(string); ok {
				channels = append(channels, s)
			}
		}
	}

	creds, _, err := s.registry.Register(nick, agentType, registry.EngagementConfig{Channels: channels})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Agent registered: %s\nnick: %s\npassword: %s",
		nick, creds.Nick, creds.Passphrase), nil
}

func (s *Server) toolSendMessage(ctx context.Context, args map[string]any) (string, error) {
	if s.sender == nil {
		return "", fmt.Errorf("send_message not available: no IRC relay connected")
	}
	channel, _ := args["channel"].(string)
	msgType, _ := args["type"].(string)
	payload := args["payload"]

	if channel == "" || msgType == "" {
		return "", fmt.Errorf("channel and type are required")
	}
	if err := s.sender.Send(ctx, channel, msgType, payload); err != nil {
		return "", err
	}
	return fmt.Sprintf("message sent to %s", channel), nil
}

func (s *Server) toolGetHistory(args map[string]any) (string, error) {
	if s.history == nil {
		return "", fmt.Errorf("get_history not available: no history store connected")
	}
	channel, _ := args["channel"].(string)
	if channel == "" {
		return "", fmt.Errorf("channel is required")
	}
	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	entries, err := s.history.Query(channel, limit)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return fmt.Sprintf("no history for %s", channel), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# history: %s (last %d)\n", channel, len(entries))
	for _, e := range entries {
		if e.MessageType != "" {
			fmt.Fprintf(&sb, "[%s] <%s> type=%s id=%s\n", channel, e.Nick, e.MessageType, e.MessageID)
		} else {
			fmt.Fprintf(&sb, "[%s] <%s> %s\n", channel, e.Nick, e.Raw)
		}
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// --- Tool schema definitions ---

func toolDefs() []map[string]any {
	return []map[string]any{
		{
			"name":        "get_status",
			"description": "Get scuttlebot daemon health and agent count.",
			"inputSchema": schema(nil),
		},
		{
			"name":        "list_channels",
			"description": "List available IRC channels with member count and topic.",
			"inputSchema": schema(nil),
		},
		{
			"name":        "register_agent",
			"description": "Register a new agent and receive IRC credentials.",
			"inputSchema": schema(map[string]any{
				"nick": prop("string", "The agent's IRC nick (unique identifier)."),
				"type": prop("string", "Agent type: operator, worker, orchestrator, or observer. Default: worker."),
				"channels": map[string]any{
					"type":        "array",
					"description": "Channels to join on connect.",
					"items":       map[string]any{"type": "string"},
				},
			}),
		},
		{
			"name":        "send_message",
			"description": "Send a typed message to an IRC channel.",
			"inputSchema": schema(map[string]any{
				"channel": prop("string", "Target channel (e.g. #fleet)."),
				"type":    prop("string", "Message type (e.g. task.create)."),
				"payload": map[string]any{
					"type":        "object",
					"description": "Message payload (any JSON object).",
				},
			}),
		},
		{
			"name":        "get_history",
			"description": "Get recent messages from an IRC channel.",
			"inputSchema": schema(map[string]any{
				"channel": prop("string", "Target channel (e.g. #fleet)."),
				"limit":   prop("number", "Number of messages to return. Default: 20."),
			}),
		},
	}
}

func schema(properties map[string]any) map[string]any {
	if len(properties) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{"type": "object", "properties": properties}
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
