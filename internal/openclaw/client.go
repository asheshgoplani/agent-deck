package openclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var clientLog = logging.ForComponent("openclaw")

const (
	// Default gateway URL (loopback only).
	DefaultGatewayURL = "ws://127.0.0.1:31337"

	// Timeouts.
	challengeTimeout = 5 * time.Second
	requestTimeout   = 10 * time.Second

	// Reconnection backoff.
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
	backoffFactor  = 2

	// Client identity — must use an allowed GatewayClientId from the protocol schema.
	clientID      = "gateway-client"
	clientMode    = "backend"
	clientVersion = "1.0.0"
)

// Client connects to the OpenClaw gateway via WebSocket JSON-RPC.
type Client struct {
	url      string
	password string

	conn      *websocket.Conn
	connMu    sync.Mutex
	hello     *HelloOk
	events    chan *GatewayEvent
	pending   map[string]chan *ResponseFrame
	pendMu    sync.Mutex
	closed    chan struct{}
	closeOnce sync.Once

	// Reconnection state
	autoReconnect bool
	backoff       time.Duration
}

// GatewayEvent wraps an event frame for consumers.
type GatewayEvent struct {
	Name    string          // Event name (e.g., "chat", "tick", "agent", "presence")
	Payload json.RawMessage // Raw payload for caller to unmarshal
	Seq     *int            // Optional sequence number
}

// NewClient creates a new OpenClaw gateway client.
func NewClient(url, password string) *Client {
	if url == "" {
		url = DefaultGatewayURL
	}
	return &Client{
		url:           url,
		password:      password,
		events:        make(chan *GatewayEvent, 256),
		pending:       make(map[string]chan *ResponseFrame),
		closed:        make(chan struct{}),
		autoReconnect: false,
		backoff:       initialBackoff,
	}
}

// Connect establishes the WebSocket connection and completes the challenge/response handshake.
func (c *Client) Connect(ctx context.Context) error {
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	// Step 1: Wait for connect.challenge event
	challenge, err := c.waitForChallenge(ctx)
	if err != nil {
		conn.Close()
		return fmt.Errorf("challenge: %w", err)
	}

	// Step 2: Send connect request with device identity
	helloOk, err := c.sendConnect(ctx, challenge.Nonce)
	if err != nil {
		conn.Close()
		return fmt.Errorf("connect: %w", err)
	}

	c.hello = helloOk
	c.backoff = initialBackoff // reset on successful connect

	// Persist device token if the gateway issued one
	if helloOk.Auth != nil && helloOk.Auth.DeviceToken != "" {
		if identity, err := LoadOrCreateIdentity(); err == nil {
			_ = SaveDeviceToken(identity.DeviceID, helloOk.Auth.Role, helloOk.Auth.DeviceToken, helloOk.Auth.Scopes)
		}
	}

	authRole := ""
	var authScopes []string
	if helloOk.Auth != nil {
		authRole = helloOk.Auth.Role
		authScopes = helloOk.Auth.Scopes
	}
	clientLog.Info("connected",
		slog.String("server_version", helloOk.Server.Version),
		slog.String("conn_id", helloOk.Server.ConnID),
		slog.Int("protocol", helloOk.Protocol),
		slog.String("auth_role", authRole),
		slog.Any("auth_scopes", authScopes),
	)

	// Start read loop
	go c.readLoop()

	return nil
}

// ConnectWithReconnect connects and automatically reconnects on disconnection.
func (c *Client) ConnectWithReconnect(ctx context.Context) error {
	c.autoReconnect = true
	return c.Connect(ctx)
}

// Request sends a JSON-RPC request and waits for the response.
func (c *Client) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := uuid.New().String()
	frame := RequestFrame{
		Type:   FrameTypeRequest,
		ID:     id,
		Method: method,
		Params: params,
	}

	respCh := make(chan *ResponseFrame, 1)
	c.pendMu.Lock()
	c.pending[id] = respCh
	c.pendMu.Unlock()

	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()

	if err := c.writeJSON(frame); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, fmt.Errorf("client closed")
	case resp := <-respCh:
		if !resp.OK {
			if resp.Error != nil {
				return nil, resp.Error
			}
			return nil, fmt.Errorf("request failed: %s", method)
		}
		return resp.Payload, nil
	}
}

// Events returns a channel of gateway events for the caller to consume.
func (c *Client) Events() <-chan *GatewayEvent {
	return c.events
}

// Hello returns the hello-ok response from the gateway, available after Connect.
func (c *Client) Hello() *HelloOk {
	return c.hello
}

// Close shuts down the client and WebSocket connection.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.autoReconnect = false
		close(c.closed)
	})
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// --- Convenience methods ---

// Health checks gateway health.
func (c *Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	_, err := c.Request(ctx, "health", nil)
	return err
}

// ListAgents returns all configured agents.
func (c *Client) ListAgents(ctx context.Context) (*AgentsListResult, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	payload, err := c.Request(ctx, "agents.list", struct{}{})
	if err != nil {
		return nil, err
	}
	var result AgentsListResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("unmarshal agents.list: %w", err)
	}
	return &result, nil
}

// ListSessions returns sessions for an agent.
func (c *Client) ListSessions(ctx context.Context, agentID string) ([]SessionEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	params := SessionsListParams{
		AgentID:              agentID,
		IncludeDerivedTitles: true,
	}
	payload, err := c.Request(ctx, "sessions.list", params)
	if err != nil {
		return nil, err
	}
	var entries []SessionEntry
	if err := json.Unmarshal(payload, &entries); err != nil {
		return nil, fmt.Errorf("unmarshal sessions.list: %w", err)
	}
	return entries, nil
}

// ChatSend sends a message to a session.
func (c *Client) ChatSend(ctx context.Context, sessionKey, message string) error {
	params := ChatSendParams{
		SessionKey:     sessionKey,
		Message:        message,
		IdempotencyKey: uuid.New().String(),
	}
	_, err := c.Request(ctx, "chat.send", params)
	return err
}

// AgentSend sends a message to an agent via the "agent" RPC method.
// Unlike ChatSend (which uses "chat.send" for webchat-only), this routes
// through the full agent pipeline and can deliver responses to external
// channels like Discord.
func (c *Client) AgentSend(ctx context.Context, params AgentParams) error {
	if params.IdempotencyKey == "" {
		params.IdempotencyKey = uuid.New().String()
	}
	_, err := c.Request(ctx, "agent", params)
	return err
}

// ChatHistory retrieves recent messages from a session.
func (c *Client) ChatHistory(ctx context.Context, sessionKey string, limit int) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	params := ChatHistoryParams{
		SessionKey: sessionKey,
		Limit:      limit,
	}
	return c.Request(ctx, "chat.history", params)
}

// ChannelsStatus returns the status of communication channels.
func (c *Client) ChannelsStatus(ctx context.Context) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return c.Request(ctx, "channels.status", struct{}{})
}

// --- Internal methods ---

func (c *Client) waitForChallenge(ctx context.Context) (*ChallengePayload, error) {
	// Read a single message expecting connect.challenge
	deadline := time.Now().Add(challengeTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set challenge read deadline: %w", err)
	}
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	_, msg, err := c.conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read challenge: %w", err)
	}

	var frame rawFrame
	if err := json.Unmarshal(msg, &frame); err != nil {
		return nil, fmt.Errorf("unmarshal frame: %w", err)
	}

	if frame.Type != FrameTypeEvent || frame.Event != "connect.challenge" {
		return nil, fmt.Errorf("expected connect.challenge, got type=%s event=%s", frame.Type, frame.Event)
	}

	var evt EventFrame
	if err := json.Unmarshal(msg, &evt); err != nil {
		return nil, fmt.Errorf("unmarshal challenge event: %w", err)
	}

	var challenge ChallengePayload
	if err := json.Unmarshal(evt.Payload, &challenge); err != nil {
		return nil, fmt.Errorf("unmarshal challenge payload: %w", err)
	}

	return &challenge, nil
}

func (c *Client) sendConnect(ctx context.Context, nonce string) (*HelloOk, error) {
	scopes := []string{"operator.admin", "operator.read", "operator.write", "operator.approvals", "operator.pairing"}

	params := ConnectParams{
		MinProtocol: ProtocolVersion,
		MaxProtocol: ProtocolVersion,
		Client: ClientInfo{
			ID:       clientID,
			Version:  clientVersion,
			Platform: runtime.GOOS,
			Mode:     clientMode,
		},
		Role:   "operator",
		Scopes: scopes,
		Auth: &ConnectAuth{
			Token:    c.password,
			Password: c.password,
		},
	}

	// Attach device identity if available (enables full scoped access)
	identity, err := LoadOrCreateIdentity()
	if err != nil {
		clientLog.Warn("device_identity_unavailable", slog.String("error", err.Error()))
	} else {
		device, err := BuildDeviceConnect(identity, nonce, scopes, runtime.GOOS, "", c.password)
		if err != nil {
			clientLog.Warn("device_connect_build_failed", slog.String("error", err.Error()))
		} else {
			params.Device = device
			// Include device token in auth if we have one
			tokens, err := LoadDeviceTokens()
			if err == nil && tokens.DeviceID == identity.DeviceID {
				if ref, ok := tokens.Tokens["operator"]; ok {
					params.Auth.DeviceToken = ref.Token
				}
			}
		}
	}

	id := uuid.New().String()
	frame := RequestFrame{
		Type:   FrameTypeRequest,
		ID:     id,
		Method: "connect",
		Params: params,
	}

	if err := c.writeJSON(frame); err != nil {
		return nil, fmt.Errorf("write connect: %w", err)
	}

	// Read hello-ok response
	deadline := time.Now().Add(challengeTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set hello read deadline: %w", err)
	}
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	_, msg, err := c.conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read hello-ok: %w", err)
	}

	var resp ResponseFrame
	if err := json.Unmarshal(msg, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.ID != id {
		return nil, fmt.Errorf("response id mismatch: got %s, want %s", resp.ID, id)
	}
	if !resp.OK {
		if resp.Error != nil {
			return nil, fmt.Errorf("connect rejected: %s", resp.Error.Error())
		}
		return nil, fmt.Errorf("connect rejected (no error details)")
	}

	var hello HelloOk
	if err := json.Unmarshal(resp.Payload, &hello); err != nil {
		return nil, fmt.Errorf("unmarshal hello-ok: %w", err)
	}

	return &hello, nil
}

func (c *Client) readLoop() {
	defer func() {
		c.connMu.Lock()
		if c.conn != nil {
			c.conn.Close()
		}
		c.connMu.Unlock()

		// Fail all pending requests
		c.pendMu.Lock()
		for id, ch := range c.pending {
			ch <- &ResponseFrame{
				OK:    false,
				Error: &ErrorShape{Code: "DISCONNECTED", Message: "connection closed"},
			}
			delete(c.pending, id)
		}
		c.pendMu.Unlock()

		// Auto-reconnect
		if c.autoReconnect {
			go c.reconnect()
		}
	}()

	for {
		select {
		case <-c.closed:
			return
		default:
		}

		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			select {
			case <-c.closed:
				return
			default:
				clientLog.Warn("read_error", slog.String("error", err.Error()))
				return
			}
		}

		c.handleMessage(msg)
	}
}

func (c *Client) handleMessage(msg []byte) {
	var frame rawFrame
	if err := json.Unmarshal(msg, &frame); err != nil {
		clientLog.Warn("unmarshal_frame", slog.String("error", err.Error()))
		return
	}

	switch frame.Type {
	case FrameTypeResponse:
		var resp ResponseFrame
		if err := json.Unmarshal(msg, &resp); err != nil {
			clientLog.Warn("unmarshal_response", slog.String("error", err.Error()))
			return
		}
		c.pendMu.Lock()
		ch, ok := c.pending[resp.ID]
		c.pendMu.Unlock()
		if ok {
			ch <- &resp
		}

	case FrameTypeEvent:
		var evt EventFrame
		if err := json.Unmarshal(msg, &evt); err != nil {
			clientLog.Warn("unmarshal_event", slog.String("error", err.Error()))
			return
		}
		// Non-blocking send to events channel
		select {
		case c.events <- &GatewayEvent{
			Name:    evt.Event,
			Payload: evt.Payload,
			Seq:     evt.Seq,
		}:
		default:
			clientLog.Warn("event_channel_full", slog.String("event", evt.Event))
		}
	}
}

func (c *Client) reconnect() {
	for {
		select {
		case <-c.closed:
			return
		default:
		}

		clientLog.Info("reconnecting", slog.Duration("backoff", c.backoff))

		// Notify consumers of reconnection attempt
		select {
		case c.events <- &GatewayEvent{Name: "_reconnecting"}:
		default:
		}

		time.Sleep(c.backoff)

		select {
		case <-c.closed:
			return
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := c.Connect(ctx)
		cancel()

		if err != nil {
			clientLog.Warn("reconnect_failed", slog.String("error", err.Error()))
			c.backoff = min(c.backoff*backoffFactor, maxBackoff)
			continue
		}

		// Notify consumers of reconnection
		select {
		case c.events <- &GatewayEvent{Name: "_reconnected"}:
		default:
		}
		return
	}
}

func (c *Client) writeJSON(v any) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	return c.conn.WriteJSON(v)
}
