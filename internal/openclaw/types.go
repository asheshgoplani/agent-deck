package openclaw

import (
	"encoding/json"
	"time"
)

// Protocol version supported by this client.
const ProtocolVersion = 3

// Frame types for the gateway WebSocket protocol.
const (
	FrameTypeRequest  = "req"
	FrameTypeResponse = "res"
	FrameTypeEvent    = "event"
)

// RequestFrame is a JSON-RPC request sent to the gateway.
type RequestFrame struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

// ResponseFrame is a JSON-RPC response from the gateway.
type ResponseFrame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *ErrorShape     `json:"error,omitempty"`
}

// EventFrame is a server-pushed event.
type EventFrame struct {
	Type         string          `json:"type"`
	Event        string          `json:"event"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Seq          *int            `json:"seq,omitempty"`
	StateVersion *StateVersion   `json:"stateVersion,omitempty"`
}

// StateVersion tracks presence and health versions for differential updates.
type StateVersion struct {
	Presence int `json:"presence"`
	Health   int `json:"health"`
}

// ErrorShape is the error object in a failed response.
type ErrorShape struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Details      any    `json:"details,omitempty"`
	Retryable    bool   `json:"retryable,omitempty"`
	RetryAfterMs int    `json:"retryAfterMs,omitempty"`
}

func (e *ErrorShape) Error() string {
	return e.Code + ": " + e.Message
}

// rawFrame is used to peek at the type/event fields before full deserialization.
type rawFrame struct {
	Type  string `json:"type"`
	Event string `json:"event,omitempty"`
	ID    string `json:"id,omitempty"`
}

// --- Connect flow types ---

// ChallengePayload is the payload of a "connect.challenge" event.
type ChallengePayload struct {
	Nonce string `json:"nonce"`
}

// ConnectParams are sent in the "connect" request.
type ConnectParams struct {
	MinProtocol int            `json:"minProtocol"`
	MaxProtocol int            `json:"maxProtocol"`
	Client      ClientInfo     `json:"client"`
	Caps        []string       `json:"caps,omitempty"`
	Role        string         `json:"role,omitempty"`
	Scopes      []string       `json:"scopes,omitempty"`
	Auth        *ConnectAuth   `json:"auth,omitempty"`
	Device      *DeviceConnect `json:"device,omitempty"`
	Locale      string         `json:"locale,omitempty"`
	UserAgent   string         `json:"userAgent,omitempty"`
}

// ClientInfo identifies this client to the gateway.
type ClientInfo struct {
	ID              string `json:"id"`
	DisplayName     string `json:"displayName,omitempty"`
	Version         string `json:"version"`
	Platform        string `json:"platform"`
	DeviceFamily    string `json:"deviceFamily,omitempty"`
	ModelIdentifier string `json:"modelIdentifier,omitempty"`
	Mode            string `json:"mode"`
	InstanceID      string `json:"instanceId,omitempty"`
}

// ConnectAuth holds authentication credentials for the connect request.
type ConnectAuth struct {
	Token       string `json:"token,omitempty"`
	DeviceToken string `json:"deviceToken,omitempty"`
	Password    string `json:"password,omitempty"`
}

// HelloOk is the payload of a successful "connect" response.
type HelloOk struct {
	Type     string      `json:"type"` // "hello-ok"
	Protocol int         `json:"protocol"`
	Server   ServerInfo  `json:"server"`
	Features Features    `json:"features"`
	Snapshot Snapshot    `json:"snapshot"`
	Auth     *HelloAuth  `json:"auth,omitempty"`
	Policy   HelloPolicy `json:"policy"`
}

// ServerInfo identifies the gateway server.
type ServerInfo struct {
	Version string `json:"version"`
	ConnID  string `json:"connId"`
}

// Features lists supported methods and events.
type Features struct {
	Methods []string `json:"methods"`
	Events  []string `json:"events"`
}

// HelloAuth is returned in hello-ok with auth tokens.
type HelloAuth struct {
	DeviceToken string   `json:"deviceToken,omitempty"`
	Role        string   `json:"role"`
	Scopes      []string `json:"scopes"`
	IssuedAtMs  int64    `json:"issuedAtMs,omitempty"`
}

// HelloPolicy defines connection limits.
type HelloPolicy struct {
	MaxPayload       int `json:"maxPayload"`
	MaxBufferedBytes int `json:"maxBufferedBytes"`
	TickIntervalMs   int `json:"tickIntervalMs"`
}

// Snapshot is the initial state snapshot in hello-ok.
type Snapshot struct {
	Presence        []PresenceEntry  `json:"presence"`
	Health          json.RawMessage  `json:"health"`
	StateVersion    StateVersion     `json:"stateVersion"`
	UptimeMs        int64            `json:"uptimeMs"`
	ConfigPath      string           `json:"configPath,omitempty"`
	StateDir        string           `json:"stateDir,omitempty"`
	SessionDefaults *SessionDefaults `json:"sessionDefaults,omitempty"`
	AuthMode        string           `json:"authMode,omitempty"`
}

// SessionDefaults from the snapshot.
type SessionDefaults struct {
	DefaultAgentID string `json:"defaultAgentId"`
	MainKey        string `json:"mainKey"`
	MainSessionKey string `json:"mainSessionKey"`
	Scope          string `json:"scope,omitempty"`
}

// PresenceEntry represents a connected client's presence info.
type PresenceEntry struct {
	Host             string   `json:"host,omitempty"`
	IP               string   `json:"ip,omitempty"`
	Version          string   `json:"version,omitempty"`
	Platform         string   `json:"platform,omitempty"`
	DeviceFamily     string   `json:"deviceFamily,omitempty"`
	ModelIdentifier  string   `json:"modelIdentifier,omitempty"`
	Mode             string   `json:"mode,omitempty"`
	LastInputSeconds *int     `json:"lastInputSeconds,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Text             string   `json:"text,omitempty"`
	Ts               int64    `json:"ts"`
	DeviceID         string   `json:"deviceId,omitempty"`
	Roles            []string `json:"roles,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	InstanceID       string   `json:"instanceId,omitempty"`
}

// --- Agent types ---

// AgentSummary is returned by agents.list.
type AgentSummary struct {
	ID       string         `json:"id"`
	Name     string         `json:"name,omitempty"`
	Identity *AgentIdentity `json:"identity,omitempty"`
}

// AgentIdentity holds display info for an agent.
type AgentIdentity struct {
	Name      string `json:"name,omitempty"`
	Theme     string `json:"theme,omitempty"`
	Emoji     string `json:"emoji,omitempty"`
	Avatar    string `json:"avatar,omitempty"`
	AvatarURL string `json:"avatarUrl,omitempty"`
}

// AgentsListResult is the response from agents.list.
type AgentsListResult struct {
	DefaultID string         `json:"defaultId"`
	MainKey   string         `json:"mainKey"`
	Scope     string         `json:"scope"`
	Agents    []AgentSummary `json:"agents"`
}

// --- Session types ---

// SessionEntry is returned by sessions.list.
type SessionEntry struct {
	Key          string          `json:"key"`
	SessionID    string          `json:"sessionId,omitempty"`
	AgentID      string          `json:"agentId,omitempty"`
	Title        string          `json:"title,omitempty"`
	DerivedTitle string          `json:"derivedTitle,omitempty"`
	Label        string          `json:"label,omitempty"`
	LastMessage  json.RawMessage `json:"lastMessage,omitempty"`
	LastActiveMs int64           `json:"lastActiveMs,omitempty"`
	CreatedMs    int64           `json:"createdMs,omitempty"`
	MessageCount int             `json:"messageCount,omitempty"`
	SpawnedBy    string          `json:"spawnedBy,omitempty"`
}

// SessionsListParams are parameters for sessions.list.
type SessionsListParams struct {
	Limit                int    `json:"limit,omitempty"`
	ActiveMinutes        int    `json:"activeMinutes,omitempty"`
	IncludeGlobal        bool   `json:"includeGlobal,omitempty"`
	IncludeUnknown       bool   `json:"includeUnknown,omitempty"`
	IncludeDerivedTitles bool   `json:"includeDerivedTitles,omitempty"`
	IncludeLastMessage   bool   `json:"includeLastMessage,omitempty"`
	Label                string `json:"label,omitempty"`
	SpawnedBy            string `json:"spawnedBy,omitempty"`
	AgentID              string `json:"agentId,omitempty"`
	Search               string `json:"search,omitempty"`
}

// --- Chat types ---

// ChatSendParams are parameters for chat.send.
type ChatSendParams struct {
	SessionKey     string `json:"sessionKey"`
	Message        string `json:"message"`
	Thinking       string `json:"thinking,omitempty"`
	Deliver        *bool  `json:"deliver,omitempty"`
	TimeoutMs      int    `json:"timeoutMs,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// ChatEvent is a streaming chat event from the gateway.
type ChatEvent struct {
	RunID        string          `json:"runId"`
	SessionKey   string          `json:"sessionKey"`
	Seq          int             `json:"seq"`
	State        string          `json:"state"` // "delta", "final", "aborted", "error"
	Message      json.RawMessage `json:"message,omitempty"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	Usage        json.RawMessage `json:"usage,omitempty"`
	StopReason   string          `json:"stopReason,omitempty"`
}

// ChatHistoryParams for chat.history.
type ChatHistoryParams struct {
	SessionKey string `json:"sessionKey"`
	Limit      int    `json:"limit,omitempty"`
}

// --- Agent event types ---

// AgentEvent is a streaming event from agent execution.
type AgentEvent struct {
	RunID      string         `json:"runId"`
	Seq        int            `json:"seq"`
	Stream     string         `json:"stream"` // "assistant", "lifecycle", "tool", "result"
	Ts         int64          `json:"ts"`
	Data       map[string]any `json:"data"`
	SessionKey string         `json:"sessionKey,omitempty"`
	AgentID    string         `json:"agentId,omitempty"`
}

// AgentParams for sending a message to an agent via the "agent" RPC method.
type AgentParams struct {
	Message        string `json:"message"`
	AgentID        string `json:"agentId,omitempty"`
	SessionKey     string `json:"sessionKey,omitempty"`
	Channel        string `json:"channel,omitempty"`
	Deliver        *bool  `json:"deliver,omitempty"`
	To             string `json:"to,omitempty"`
	AccountID      string `json:"accountId,omitempty"`
	ReplyChannel   string `json:"replyChannel,omitempty"`
	ReplyAccountID string `json:"replyAccountId,omitempty"`
	ThreadID       string `json:"threadId,omitempty"`
	GroupID        string `json:"groupId,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// --- History types ---

// HistoryResponse is the response from chat.history.
type HistoryResponse struct {
	SessionKey string           `json:"sessionKey"`
	Messages   []HistoryMessage `json:"messages"`
}

// HistoryMessage is a message from chat history.
type HistoryMessage struct {
	Role      string          `json:"role"` // "user", "assistant", "system"
	Timestamp int64           `json:"timestamp"`
	Content   json.RawMessage `json:"content"` // string or []ContentBlock
}

// --- Tick event ---

// TickPayload is the payload of a "tick" heartbeat event.
type TickPayload struct {
	Ts int64 `json:"ts"`
}

// ShutdownPayload is the payload of a "shutdown" event.
type ShutdownPayload struct {
	Reason            string `json:"reason"`
	RestartExpectedMs int    `json:"restartExpectedMs,omitempty"`
}

// --- Channels status ---

// ChannelsStatusResult is the response from channels.status.
type ChannelsStatusResult struct {
	Discord *ChannelStatus `json:"discord,omitempty"`
}

// ChannelStatus represents the status of a communication channel.
type ChannelStatus struct {
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	Guilds    int    `json:"guilds,omitempty"`
	Error     string `json:"error,omitempty"`
}

// --- Bridge display types ---

// ChatMessage is a normalized message for display in the bridge TUI.
type ChatMessage struct {
	Timestamp time.Time
	Sender    string
	Content   string
	Direction string // "inbound" (from Discord), "outbound" (from bridge), "system"
	AgentID   string
	RunID     string // tracks which conversation turn this belongs to
}

// BridgeStatus represents the connection state shown in the bridge header.
type BridgeStatus int

const (
	BridgeStatusDisconnected BridgeStatus = iota
	BridgeStatusConnecting
	BridgeStatusConnected
	BridgeStatusProcessing
	BridgeStatusReconnecting
)

func (s BridgeStatus) String() string {
	switch s {
	case BridgeStatusDisconnected:
		return "DISCONNECTED"
	case BridgeStatusConnecting:
		return "CONNECTING"
	case BridgeStatusConnected:
		return "CONNECTED"
	case BridgeStatusProcessing:
		return "PROCESSING"
	case BridgeStatusReconnecting:
		return "RECONNECTING"
	default:
		return "UNKNOWN"
	}
}

// OpenClawOptions is stored in ToolOptionsJSON to identify which agent a session bridges.
type OpenClawOptions struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name,omitempty"`
}
