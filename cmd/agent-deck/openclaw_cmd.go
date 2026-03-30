package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/docker"
	"github.com/asheshgoplani/agent-deck/internal/openclaw"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleOpenClaw dispatches openclaw subcommands.
func handleOpenClaw(profile string, args []string) {
	if len(args) == 0 {
		printOpenClawHelp()
		return
	}

	switch args[0] {
	case "sync":
		handleOpenClawSync(profile, args[1:])
	case "bridge":
		handleOpenClawBridge(args[1:])
	case "status":
		handleOpenClawStatus(args[1:])
	case "list":
		handleOpenClawList(args[1:])
	case "send":
		handleOpenClawSend(args[1:])
	case "pair":
		handleOpenClawPair(args[1:])
	case "help", "--help", "-h":
		printOpenClawHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown openclaw command: %s\n", args[0])
		fmt.Fprintln(os.Stderr)
		printOpenClawHelp()
		os.Exit(1)
	}
}

func printOpenClawHelp() {
	fmt.Println("Usage: agent-deck openclaw <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  sync           Sync OpenClaw agents as agent-deck sessions")
	fmt.Println("  bridge         Launch bridge TUI for an agent (runs inside tmux)")
	fmt.Println("  status         Show gateway health and agent summary")
	fmt.Println("  list           List agents from gateway")
	fmt.Println("  send           Send a message to an agent")
	fmt.Println("  pair           Pair this device with the gateway (required for full access)")
	fmt.Println("  help           Show this help")
	fmt.Println()
	fmt.Println("Aliases: openclaw, oc")
}

// --- sync ---

func handleOpenClawSync(profile string, args []string) {
	fs := flag.NewFlagSet("openclaw sync", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	cfg := loadOpenClawConfig()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := openclaw.NewClient(cfg.GatewayURL, cfg.Password)
	if err := client.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to gateway: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	agentsResult, err := client.ListAgents(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list agents: %v\n", err)
		os.Exit(1)
	}

	// Load existing sessions
	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	groupName := cfg.GroupName
	if groupName == "" {
		groupName = "openclaw"
	}

	var created, updated int
	for _, agent := range agentsResult.Agents {
		existing := findOpenClawSession(instances, agent.ID)
		agentDisplayName := resolveAgentName(agent)

		if existing == nil {
			// Create new session
			command := buildOpenClawBridgeCommand(agent.ID)
			inst := session.NewInstanceWithGroupAndTool(agentDisplayName, os.Getenv("HOME"), groupName, "openclaw")
			inst.Command = command

			// Store agent ID in tool options
			opts := openclaw.OpenClawOptions{
				AgentID:   agent.ID,
				AgentName: agentDisplayName,
			}
			optsJSON, _ := json.Marshal(opts)
			wrapper := struct {
				Tool    string          `json:"tool"`
				Options json.RawMessage `json:"options"`
			}{
				Tool:    "openclaw",
				Options: optsJSON,
			}
			wrapperJSON, _ := json.Marshal(wrapper)
			inst.ToolOptionsJSON = wrapperJSON

			instances = append(instances, inst)
			created++
		} else {
			// Update title if agent name changed
			if existing.Title != agentDisplayName {
				existing.Title = agentDisplayName
				updated++
			}
		}
	}

	// Save
	groupTree := session.NewGroupTree(instances)
	_ = groupsData // unused but loaded for completeness
	groupTree.CreateGroup(groupName)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save sessions: %v\n", err)
		os.Exit(1)
	}

	if *jsonOutput {
		result := map[string]any{
			"total":   len(agentsResult.Agents),
			"created": created,
			"updated": updated,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write JSON output: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("Synced %d agents (%d new, %d updated)\n", len(agentsResult.Agents), created, updated)
	}
}

func buildOpenClawBridgeCommand(agentID string) string {
	return docker.ShellJoinArgs([]string{
		"agent-deck",
		"openclaw",
		"bridge",
		"--agent",
		agentID,
	})
}

// --- bridge ---

func handleOpenClawBridge(args []string) {
	fs := flag.NewFlagSet("openclaw bridge", flag.ExitOnError)
	agentID := fs.String("agent", "", "Agent ID to bridge")
	agentName := fs.String("name", "", "Agent display name (optional)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *agentID == "" {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck openclaw bridge --agent <id>")
		os.Exit(1)
	}

	cfg := loadOpenClawConfig()

	if err := openclaw.RunBridge(cfg.GatewayURL, cfg.Password, *agentID, *agentName); err != nil {
		fmt.Fprintf(os.Stderr, "Bridge error: %v\n", err)
		os.Exit(1)
	}
}

// --- status ---

func handleOpenClawStatus(args []string) {
	cfg := loadOpenClawConfig()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := openclaw.NewClient(cfg.GatewayURL, cfg.Password)
	if err := client.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Gateway: OFFLINE (%v)\n", err)
		os.Exit(1)
	}
	defer client.Close()

	hello := client.Hello()
	fmt.Printf("Gateway: ONLINE (v%s, conn=%s)\n", hello.Server.Version, hello.Server.ConnID)
	fmt.Printf("Protocol: %d\n", hello.Protocol)
	fmt.Printf("Uptime: %s\n", formatDuration(time.Duration(hello.Snapshot.UptimeMs)*time.Millisecond))

	if hello.Snapshot.AuthMode != "" {
		fmt.Printf("Auth: %s\n", hello.Snapshot.AuthMode)
	}

	// Show channels — the response structure varies by gateway version,
	// so we look for keys that look like channel names (e.g., "discord")
	// and skip config-like keys.
	channelsPayload, err := client.ChannelsStatus(ctx)
	if err == nil && channelsPayload != nil {
		var channels map[string]json.RawMessage
		if json.Unmarshal(channelsPayload, &channels) == nil {
			// Filter to only entries that unmarshal as ChannelStatus
			var found []string
			for name, data := range channels {
				var status openclaw.ChannelStatus
				if json.Unmarshal(data, &status) == nil && (status.Enabled || status.Connected) {
					state := "disconnected"
					if status.Connected {
						state = "connected"
					}
					entry := fmt.Sprintf("  %s: %s", name, state)
					if status.Guilds > 0 {
						entry += fmt.Sprintf(" (%d guilds)", status.Guilds)
					}
					if status.Error != "" {
						entry += fmt.Sprintf(" [error: %s]", status.Error)
					}
					found = append(found, entry)
				}
			}
			if len(found) > 0 {
				fmt.Println()
				fmt.Println("Channels:")
				for _, f := range found {
					fmt.Println(f)
				}
			}
		}
	}

	// Show agents
	agentsResult, err := client.ListAgents(ctx)
	if err == nil {
		fmt.Println()
		fmt.Printf("Agents (%d):\n", len(agentsResult.Agents))
		for _, agent := range agentsResult.Agents {
			name := resolveAgentName(agent)
			defaultMarker := ""
			if agent.ID == agentsResult.DefaultID {
				defaultMarker = " (default)"
			}
			fmt.Printf("  %s: %s%s\n", agent.ID, name, defaultMarker)
		}
	}

	// Show presence
	if len(hello.Snapshot.Presence) > 0 {
		fmt.Println()
		fmt.Printf("Connected clients (%d):\n", len(hello.Snapshot.Presence))
		for _, p := range hello.Snapshot.Presence {
			mode := p.Mode
			if mode == "" {
				mode = "unknown"
			}
			platform := p.Platform
			if platform == "" {
				platform = "?"
			}
			fmt.Printf("  %s/%s", mode, platform)
			if p.Version != "" {
				fmt.Printf(" v%s", p.Version)
			}
			fmt.Println()
		}
	}
}

// --- list ---

func handleOpenClawList(args []string) {
	fs := flag.NewFlagSet("openclaw list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	cfg := loadOpenClawConfig()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := openclaw.NewClient(cfg.GatewayURL, cfg.Password)
	if err := client.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to gateway: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	result, err := client.ListAgents(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list agents: %v\n", err)
		os.Exit(1)
	}

	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write JSON output: %v\n", err)
			os.Exit(1)
		}
		return
	}

	for _, agent := range result.Agents {
		name := resolveAgentName(agent)
		defaultMarker := ""
		if agent.ID == result.DefaultID {
			defaultMarker = " *"
		}
		emoji := ""
		if agent.Identity != nil && agent.Identity.Emoji != "" {
			emoji = agent.Identity.Emoji + " "
		}
		fmt.Printf("  %s%s (%s)%s\n", emoji, name, agent.ID, defaultMarker)
	}
}

// --- send ---

func handleOpenClawSend(args []string) {
	fs := flag.NewFlagSet("openclaw send", flag.ExitOnError)
	agentID := fs.String("agent", "", "Agent ID to send to")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *agentID == "" || fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck openclaw send --agent <id> <message>")
		os.Exit(1)
	}

	message := strings.Join(fs.Args(), " ")

	cfg := loadOpenClawConfig()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := openclaw.NewClient(cfg.GatewayURL, cfg.Password)
	if err := client.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to gateway: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	deliver := true
	params := openclaw.AgentParams{
		Message: message,
		AgentID: *agentID,
		Channel: "discord",
		Deliver: &deliver,
	}

	if err := client.AgentSend(ctx, params); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to send message: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Message sent to %s\n", *agentID)
}

// --- Helpers ---

func loadOpenClawConfig() *session.OpenClawSettings {
	config, err := session.LoadUserConfig()
	if err != nil || config == nil {
		return &session.OpenClawSettings{
			GatewayURL: openclaw.DefaultGatewayURL,
			Password:   os.Getenv("OPENCLAW_PASSWORD"),
		}
	}
	cfg := &config.OpenClaw
	if cfg.GatewayURL == "" {
		cfg.GatewayURL = openclaw.DefaultGatewayURL
	}
	// Expand env var references in password (e.g. "$OPENCLAW_PASSWORD")
	if strings.HasPrefix(cfg.Password, "$") {
		cfg.Password = os.ExpandEnv(cfg.Password)
	}
	// Fall back to env var if password is still empty
	if cfg.Password == "" {
		cfg.Password = os.Getenv("OPENCLAW_PASSWORD")
	}
	return cfg
}

func findOpenClawSession(instances []*session.Instance, agentID string) *session.Instance {
	for _, inst := range instances {
		if inst.Tool != "openclaw" {
			continue
		}
		if inst.ToolOptionsJSON == nil {
			continue
		}
		var wrapper struct {
			Tool    string          `json:"tool"`
			Options json.RawMessage `json:"options"`
		}
		if err := json.Unmarshal(inst.ToolOptionsJSON, &wrapper); err != nil {
			continue
		}
		var opts openclaw.OpenClawOptions
		if err := json.Unmarshal(wrapper.Options, &opts); err != nil {
			continue
		}
		if opts.AgentID == agentID {
			return inst
		}
	}
	return nil
}

func resolveAgentName(agent openclaw.AgentSummary) string {
	if agent.Identity != nil && agent.Identity.Name != "" {
		return agent.Identity.Name
	}
	if agent.Name != "" {
		return agent.Name
	}
	return agent.ID
}

// --- pair ---

func handleOpenClawPair(args []string) {
	fs := flag.NewFlagSet("openclaw pair", flag.ExitOnError)
	showID := fs.Bool("id", false, "Show device ID only")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	identity, err := openclaw.LoadOrCreateIdentity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create device identity: %v\n", err)
		os.Exit(1)
	}

	if *showID {
		fmt.Println(identity.DeviceID)
		return
	}

	// Check if already paired
	tokens, _ := openclaw.LoadDeviceTokens()
	if tokens != nil && tokens.DeviceID == identity.DeviceID {
		if ref, ok := tokens.Tokens["operator"]; ok && ref.Token != "" {
			fmt.Printf("Device already paired (id: %s...%s)\n", identity.DeviceID[:8], identity.DeviceID[len(identity.DeviceID)-8:])
			fmt.Println("Testing connection with device auth...")

			cfg := loadOpenClawConfig()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			client := openclaw.NewClient(cfg.GatewayURL, cfg.Password)
			if err := client.Connect(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Connection failed: %v\n", err)
				fmt.Println("Try: agent-deck openclaw pair --force to re-pair")
				os.Exit(1)
			}
			defer client.Close()

			hello := client.Hello()
			if hello.Auth != nil && len(hello.Auth.Scopes) > 0 {
				fmt.Printf("Scopes: %s\n", strings.Join(hello.Auth.Scopes, ", "))
				fmt.Println("Device pairing is working.")
			} else {
				fmt.Println("Connected but no scopes granted. Try re-pairing.")
			}
			return
		}
	}

	cfg := loadOpenClawConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := openclaw.NewClient(cfg.GatewayURL, cfg.Password)
	if err := client.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	rawPub, _ := openclaw.RawPublicKeyBase64URL(identity.PublicKeyPEM)

	pairParams := map[string]any{
		"deviceId":     identity.DeviceID,
		"publicKey":    rawPub,
		"displayName":  fmt.Sprintf("Agent Deck (%s)", "darwin"),
		"platform":     "darwin",
		"deviceFamily": "mac",
		"clientId":     "gateway-client",
		"clientMode":   "backend",
		"role":         "operator",
		"roles":        []string{"operator"},
		"scopes":       []string{"operator.admin", "operator.read", "operator.write", "operator.approvals", "operator.pairing"},
		"silent":       false,
	}

	fmt.Printf("Device ID: %s\n", identity.DeviceID)
	fmt.Println("Requesting pairing...")

	payload, err := client.Request(ctx, "device.pair.request", pairParams)
	if err != nil {
		if strings.Contains(err.Error(), "already paired") {
			fmt.Println("Device is already paired with the gateway.")
			return
		}
		fmt.Fprintf(os.Stderr, "Pairing request failed: %v\n", err)
		fmt.Println()
		fmt.Println("Approve this device on the gateway:")
		fmt.Printf("  ssh openclaw \"openclaw device approve %s\"\n", identity.DeviceID[:12])
		os.Exit(1)
	}

	var pairResult struct {
		RequestID string `json:"requestId"`
		Device    *struct {
			DeviceID string `json:"deviceId"`
			Tokens   map[string]struct {
				Token  string   `json:"token"`
				Role   string   `json:"role"`
				Scopes []string `json:"scopes"`
			} `json:"tokens"`
		} `json:"device"`
	}
	if err := json.Unmarshal(payload, &pairResult); err == nil && pairResult.Device != nil && pairResult.Device.Tokens != nil {
		for role, tokenInfo := range pairResult.Device.Tokens {
			_ = openclaw.SaveDeviceToken(identity.DeviceID, role, tokenInfo.Token, tokenInfo.Scopes)
		}
		fmt.Println("Paired successfully!")
		return
	}

	fmt.Println()
	fmt.Println("Pairing request sent. Approve on the gateway:")
	if pairResult.RequestID != "" {
		fmt.Printf("  ssh openclaw \"openclaw device approve %s\"\n", pairResult.RequestID)
	} else {
		fmt.Printf("  ssh openclaw \"openclaw device approve %s\"\n", identity.DeviceID[:12])
	}
	fmt.Println()
	fmt.Println("After approving, run: agent-deck openclaw status")
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	if hours < 24 {
		return fmt.Sprintf("%dh %dm", hours, int(d.Minutes())%60)
	}
	days := hours / 24
	return fmt.Sprintf("%dd %dh", days, hours%24)
}
