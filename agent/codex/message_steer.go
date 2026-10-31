package codex

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/chenhg5/cc-connect/core"
)

func (s *appServerSession) SteerMessage(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment) (bool, error) {
	s.stateMu.Lock()
	turnID := s.currentTurn
	s.stateMu.Unlock()
	if turnID == "" || !s.alive.Load() {
		return false, nil
	}
	if len(files) > 0 {
		prompt = core.AppendFileRefs(prompt, core.SaveFilesToDisk(s.workDir, messageID, files))
	}
	prompt, imagePaths, err := s.stageImages(prompt, images)
	if err != nil {
		return false, err
	}
	input := []map[string]any{{"type": "text", "text": prompt, "text_elements": []any{}}}
	for _, path := range imagePaths {
		input = append(input, map[string]any{"type": "localImage", "path": path})
	}
	var response struct {
		TurnID string `json:"turnId"`
	}
	err = s.request("turn/steer", map[string]any{
		"threadId": s.CurrentSessionID(), "expectedTurnId": turnID, "input": input,
	}, &response)
	if err != nil {
		var rejection *rpcError
		if errors.As(err, &rejection) && (rejection.Code == -32600 || rejection.Code == -32601 || rejection.Code == -32602) {
			// A server rejection (including a turn ending during the request)
			// is safe to retry through the ordinary queue. Transport timeouts
			// may have reached the server and must never duplicate user input.
			slog.Warn("codex app-server rejected steer; queueing input", "error", err)
			return false, nil
		}
		return false, fmt.Errorf("codex app-server turn/steer delivery uncertain: %w", err)
	}
	if response.TurnID != turnID {
		return false, fmt.Errorf("codex app-server steer returned turn %q, want %q", response.TurnID, turnID)
	}
	// Do not inspect currentTurn again: a successful steer remains accepted
	// even if its completion notification arrived before the response.
	return true, nil
}

var _ core.MessageSteerer = (*appServerSession)(nil)
