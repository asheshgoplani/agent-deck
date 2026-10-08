package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// QueueOpenCodePrompt admits one queued input through the selected launcher's
// session API, never through terminal keystrokes. A missing session identity or
// unconfirmed response fails closed; ambiguous outcomes are not retried.
func (i *Instance) QueueOpenCodePrompt(ctx context.Context, text string) error {
	if i == nil || i.Tool != "opencode" {
		return fmt.Errorf("native queue requires an OpenCode session")
	}
	i.mu.RLock()
	id, project := i.OpenCodeSessionID, i.ProjectPath
	i.mu.RUnlock()
	if !strings.HasPrefix(id, "ses_") || strings.ContainsAny(id, " \t\r\n") {
		return fmt.Errorf("message not queued: OpenCode session identity is unavailable")
	}
	body, err := json.Marshal(struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
	}{text, "queue"})
	if err != nil {
		return err
	}
	cmd, err := i.openCodeCLICommand(ctx, project, "api", "session.prompt", "--param", "sessionID="+id, "--data", string(body))
	if err != nil {
		return err
	}
	// Do not include stdout/stderr in errors: server responses can contain drafts.
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("queue acceptance unconfirmed (not retried): %w", err)
	}
	var response struct {
		Data struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Delivery  string `json:"delivery"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &response); err != nil ||
		!strings.HasPrefix(response.Data.ID, "msg_") || response.Data.SessionID != id || response.Data.Delivery != "queue" {
		return fmt.Errorf("queue acceptance unconfirmed (not retried): unexpected session API response")
	}
	return nil
}
