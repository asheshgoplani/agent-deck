package openclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/termtext"
)

// Bridge styles
var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)

	statusConnected    = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	statusDisconnected = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	statusProcessing   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	statusReconnecting = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)

	timestampStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	senderStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	agentStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("135")).Bold(true)
	systemStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Italic(true)
	separatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	promptStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("62")).Bold(true)
)

// BridgeModel is the bubbletea model for the OpenClaw bridge TUI.
type BridgeModel struct {
	client    *Client
	agentID   string
	agentName string

	// UI components
	viewport  viewport.Model
	textInput textinput.Model
	messages  []ChatMessage
	status    BridgeStatus
	width     int
	height    int
	ready     bool

	// Connection
	ctx    context.Context
	cancel context.CancelFunc
	err    error
}

// --- Tea messages ---

type connectResultMsg struct{ err error }
type gatewayEventMsg struct{ event *GatewayEvent }
type sendResultMsg struct{ err error }
type historyResultMsg struct{ messages []ChatMessage }

// NewBridgeModel creates a new bridge TUI model.
func NewBridgeModel(gatewayURL, password, agentID, agentName string) *BridgeModel {
	ti := textinput.New()
	ti.Placeholder = "Type a message..."
	ti.Focus()
	ti.CharLimit = 4096
	ti.Width = 80

	ctx, cancel := context.WithCancel(context.Background())

	client := NewClient(gatewayURL, password)

	m := &BridgeModel{
		client:    client,
		agentID:   agentID,
		agentName: agentName,
		textInput: ti,
		status:    BridgeStatusDisconnected,
		ctx:       ctx,
		cancel:    cancel,
	}

	return m
}

func (m *BridgeModel) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		m.connectCmd(),
	)
}

func (m *BridgeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			m.cancel()
			m.client.Close()
			return m, tea.Quit
		case tea.KeyEnter:
			text := strings.TrimSpace(m.textInput.Value())
			if text != "" && m.status == BridgeStatusConnected {
				m.textInput.Reset()
				m.addMessage(ChatMessage{
					Timestamp: time.Now(),
					Sender:    "you",
					Content:   text,
					Direction: "outbound",
				})
				cmds = append(cmds, m.sendCmd(text))
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		headerHeight := 1
		inputHeight := 3 // separator + prompt + padding
		vpHeight := m.height - headerHeight - inputHeight
		if vpHeight < 1 {
			vpHeight = 1
		}

		if !m.ready {
			m.viewport = viewport.New(m.width, vpHeight)
			m.viewport.YPosition = headerHeight
			m.ready = true
		} else {
			m.viewport.Width = m.width
			m.viewport.Height = vpHeight
		}
		m.textInput.Width = m.width - len("openclaw> ") - 1
		m.updateViewportContent()

	case connectResultMsg:
		if msg.err != nil {
			m.status = BridgeStatusDisconnected
			m.err = msg.err
			m.addMessage(ChatMessage{
				Timestamp: time.Now(),
				Sender:    "system",
				Content:   fmt.Sprintf("Connection failed: %v", msg.err),
				Direction: "system",
			})
			// Retry after backoff
			cmds = append(cmds, m.reconnectCmd())
		} else {
			m.status = BridgeStatusConnected
			m.err = nil

			// Resolve agent display name from hello snapshot
			if m.agentName == "" {
				m.agentName = m.agentID
			}

			m.addMessage(ChatMessage{
				Timestamp: time.Now(),
				Sender:    "system",
				Content:   fmt.Sprintf("Connected to gateway (server %s)", m.client.Hello().Server.Version),
				Direction: "system",
			})
			// Load history and start listening for events
			cmds = append(cmds, m.loadHistoryCmd())
			cmds = append(cmds, m.listenEventsCmd())
		}

	case historyResultMsg:
		if len(msg.messages) > 0 {
			// Prepend history before any existing messages (keep system msgs)
			existing := m.messages
			m.messages = msg.messages
			m.messages = append(m.messages, existing...)
			m.updateViewportContent()
		}

	case gatewayEventMsg:
		if msg.event != nil {
			cmds = append(cmds, m.handleGatewayEvent(msg.event))
			// Continue listening
			cmds = append(cmds, m.listenEventsCmd())
		}

	case sendResultMsg:
		if msg.err != nil {
			m.addMessage(ChatMessage{
				Timestamp: time.Now(),
				Sender:    "system",
				Content:   fmt.Sprintf("Send failed: %v", msg.err),
				Direction: "system",
			})
		}
	}

	// Update text input
	var tiCmd tea.Cmd
	m.textInput, tiCmd = m.textInput.Update(msg)
	cmds = append(cmds, tiCmd)

	// Update viewport
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m *BridgeModel) View() string {
	if !m.ready {
		return "Initializing..."
	}

	header := m.renderHeader()
	separator := separatorStyle.Render(strings.Repeat("─", m.width))
	prompt := promptStyle.Render("openclaw> ") + m.textInput.View()

	return header + "\n" + m.viewport.View() + "\n" + separator + "\n" + prompt
}

// --- Rendering ---

func (m *BridgeModel) renderHeader() string {
	agentDisplay := termtext.Clean(m.agentName)
	if agentDisplay == "" {
		agentDisplay = termtext.Clean(m.agentID)
	}

	var statusStr string
	switch m.status {
	case BridgeStatusConnected:
		statusStr = statusConnected.Render("[CONNECTED]")
	case BridgeStatusDisconnected:
		statusStr = statusDisconnected.Render("[DISCONNECTED]")
	case BridgeStatusProcessing:
		statusStr = statusProcessing.Render("[PROCESSING]")
	case BridgeStatusConnecting:
		statusStr = statusReconnecting.Render("[CONNECTING]")
	case BridgeStatusReconnecting:
		statusStr = statusReconnecting.Render("[RECONNECTING]")
	}

	title := fmt.Sprintf(" openclaw > %s ", agentDisplay)
	return headerStyle.Render(title) + " " + statusStr
}

func (m *BridgeModel) renderMessages() string {
	if len(m.messages) == 0 {
		return systemStyle.Render("  Waiting for messages...")
	}

	var sb strings.Builder
	for _, msg := range m.messages {
		ts := timestampStyle.Render(fmt.Sprintf("[%s]", msg.Timestamp.Format("15:04")))

		switch msg.Direction {
		case "system":
			sb.WriteString(fmt.Sprintf("  %s %s\n", ts, systemStyle.Render(cleanLine(msg.Content))))
		case "outbound":
			sb.WriteString(fmt.Sprintf("  %s %s\n", ts, senderStyle.Render("you:")))
			for _, line := range strings.Split(msg.Content, "\n") {
				sb.WriteString(fmt.Sprintf("    %s\n", cleanLine(line)))
			}
		default:
			sender := termtext.Clean(msg.Sender)
			if msg.AgentID != "" {
				sender = agentStyle.Render(termtext.Clean(msg.AgentID) + ":")
			} else {
				sender = senderStyle.Render(sender + ":")
			}
			sb.WriteString(fmt.Sprintf("  %s %s\n", ts, sender))
			for _, line := range strings.Split(msg.Content, "\n") {
				sb.WriteString(fmt.Sprintf("    %s\n", cleanLine(line)))
			}
		}
	}
	return sb.String()
}

// cleanLine makes one line of message text safe to render: chat content,
// history, agent errors and the shutdown reason all come from the gateway.
// Tabs become spaces so indentation survives termtext.Clean.
func cleanLine(s string) string {
	return termtext.Clean(strings.ReplaceAll(s, "\t", "    "))
}

func (m *BridgeModel) updateViewportContent() {
	if m.ready {
		content := m.renderMessages()
		m.viewport.SetContent(content)
		m.viewport.GotoBottom()
	}
}

func (m *BridgeModel) addMessage(msg ChatMessage) {
	m.messages = append(m.messages, msg)
	// Keep a rolling window of messages
	if len(m.messages) > 500 {
		m.messages = m.messages[len(m.messages)-400:]
	}
	m.updateViewportContent()
}

// --- Commands ---

func (m *BridgeModel) connectCmd() tea.Cmd {
	return func() tea.Msg {
		m.status = BridgeStatusConnecting
		err := m.client.ConnectWithReconnect(m.ctx)
		return connectResultMsg{err: err}
	}
}

func (m *BridgeModel) reconnectCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
			return nil
		case <-time.After(3 * time.Second):
		}
		m.status = BridgeStatusReconnecting
		err := m.client.ConnectWithReconnect(m.ctx)
		return connectResultMsg{err: err}
	}
}

func (m *BridgeModel) sendCmd(message string) tea.Cmd {
	return func() tea.Msg {
		deliver := true
		params := AgentParams{
			Message: message,
			AgentID: m.agentID,
			Channel: "discord",
			Deliver: &deliver,
		}

		err := m.client.AgentSend(m.ctx, params)
		return sendResultMsg{err: err}
	}
}

func (m *BridgeModel) loadHistoryCmd() tea.Cmd {
	return func() tea.Msg {
		// Each agent has its own session key in "agent:<agentId>:main" format
		sessionKey := "agent:" + m.agentID + ":main"

		payload, err := m.client.ChatHistory(m.ctx, sessionKey, 50)
		if err != nil {
			return historyResultMsg{}
		}

		var resp HistoryResponse
		if err := json.Unmarshal(payload, &resp); err != nil {
			return historyResultMsg{}
		}

		var messages []ChatMessage
		for _, msg := range resp.Messages {
			if msg.Role == "system" {
				continue
			}

			content := extractHistoryContent(msg.Content)
			if content == "" {
				continue
			}

			ts := time.UnixMilli(msg.Timestamp)
			direction := "inbound"
			sender := m.agentName
			agentID := m.agentID
			if msg.Role == "user" {
				direction = "outbound"
				sender = "you"
				agentID = ""
			}

			messages = append(messages, ChatMessage{
				Timestamp: ts,
				Sender:    sender,
				Content:   content,
				Direction: direction,
				AgentID:   agentID,
			})
		}

		return historyResultMsg{messages: messages}
	}
}

// extractHistoryContent pulls text from a history message content field,
// which can be a JSON string or an array of content blocks.
func extractHistoryContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try as string first
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try as array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	return ""
}

func (m *BridgeModel) listenEventsCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
			return nil
		case evt, ok := <-m.client.Events():
			if !ok {
				return nil
			}
			return gatewayEventMsg{event: evt}
		}
	}
}

// --- Event handling ---

func (m *BridgeModel) handleGatewayEvent(evt *GatewayEvent) tea.Cmd {
	switch evt.Name {
	case "chat":
		var chatEvt ChatEvent
		if err := json.Unmarshal(evt.Payload, &chatEvt); err != nil {
			clientLog.Warn("unmarshal_chat_event", "error", err.Error())
			return nil
		}
		// Filter: only handle chat events for this bridge's agent
		if !m.isMyEvent("", chatEvt.SessionKey) {
			return nil
		}
		m.handleChatEvent(&chatEvt)

	case "agent":
		var agentEvt AgentEvent
		if err := json.Unmarshal(evt.Payload, &agentEvt); err != nil {
			return nil
		}
		if !m.isMyEvent(agentEvt.AgentID, agentEvt.SessionKey) {
			return nil
		}
		m.handleAgentEvent(&agentEvt)

	case "tick":
		// Heartbeat — no action needed

	case "shutdown":
		var shutdown ShutdownPayload
		if err := json.Unmarshal(evt.Payload, &shutdown); err == nil {
			m.addMessage(ChatMessage{
				Timestamp: time.Now(),
				Sender:    "system",
				Content:   fmt.Sprintf("Gateway shutting down: %s", shutdown.Reason),
				Direction: "system",
			})
		}
		m.status = BridgeStatusDisconnected

	case "_reconnecting":
		m.status = BridgeStatusReconnecting
		m.addMessage(ChatMessage{
			Timestamp: time.Now(),
			Sender:    "system",
			Content:   "Reconnecting...",
			Direction: "system",
		})

	case "_reconnected":
		m.status = BridgeStatusConnected
		m.addMessage(ChatMessage{
			Timestamp: time.Now(),
			Sender:    "system",
			Content:   "Reconnected",
			Direction: "system",
		})
	}

	return nil
}

func (m *BridgeModel) handleChatEvent(evt *ChatEvent) {
	// Text content is handled exclusively via agent events to avoid duplication.
	// Chat events are only used for error/abort/status.
	switch evt.State {
	case "delta":
		m.status = BridgeStatusProcessing
	case "final":
		m.status = BridgeStatusConnected
	case "error":
		m.status = BridgeStatusConnected
		errMsg := evt.ErrorMessage
		if errMsg == "" {
			errMsg = "unknown error"
		}
		m.addMessage(ChatMessage{
			Timestamp: time.Now(),
			Sender:    "system",
			Content:   fmt.Sprintf("Agent error: %s", errMsg),
			Direction: "system",
		})
	case "aborted":
		m.status = BridgeStatusConnected
		m.addMessage(ChatMessage{
			Timestamp: time.Now(),
			Sender:    "system",
			Content:   "Response aborted",
			Direction: "system",
		})
	}
}

func (m *BridgeModel) handleAgentEvent(evt *AgentEvent) {
	switch evt.Stream {
	case "assistant":
		m.status = BridgeStatusProcessing
		// The gateway sends "text" (full accumulated) and optionally "delta" (new chunk).
		// Use "text" as a full replacement to avoid duplication.
		text, _ := evt.Data["text"].(string)
		if text == "" {
			return
		}
		// Replace content only if same runId (same conversation turn)
		if len(m.messages) > 0 && evt.RunID != "" {
			last := &m.messages[len(m.messages)-1]
			if last.RunID == evt.RunID && last.Direction == "inbound" {
				last.Content = text
				m.updateViewportContent()
				return
			}
		}
		m.addMessage(ChatMessage{
			Timestamp: time.Now(),
			Sender:    m.agentName,
			Content:   text,
			Direction: "inbound",
			AgentID:   m.agentID,
			RunID:     evt.RunID,
		})

	case "lifecycle":
		phase, _ := evt.Data["phase"].(string)
		switch phase {
		case "start":
			m.status = BridgeStatusProcessing
		case "end":
			m.status = BridgeStatusConnected
		case "error":
			m.status = BridgeStatusConnected
			errMsg, _ := evt.Data["error"].(string)
			if errMsg != "" {
				m.addMessage(ChatMessage{
					Timestamp: time.Now(),
					Sender:    "system",
					Content:   fmt.Sprintf("Agent error: %s", errMsg),
					Direction: "system",
				})
			}
		}

	case "result":
		m.status = BridgeStatusConnected

	default:
		m.status = BridgeStatusProcessing
	}
}

// isMyEvent checks if an event belongs to this bridge's agent by matching
// agentID or sessionKey. Session keys use format "agent:<agentId>:<session>".
func (m *BridgeModel) isMyEvent(agentID, sessionKey string) bool {
	if agentID != "" {
		return agentID == m.agentID
	}
	if sessionKey != "" {
		return sessionKey == m.agentID ||
			strings.HasPrefix(sessionKey, m.agentID+":") ||
			strings.HasPrefix(sessionKey, "agent:"+m.agentID+":")
	}
	return false
}

// RunBridge launches the bridge TUI as a bubbletea program.
func RunBridge(gatewayURL, password, agentID, agentName string) error {
	model := NewBridgeModel(gatewayURL, password, agentID, agentName)
	p := tea.NewProgram(model)
	_, err := p.Run()
	return err
}
