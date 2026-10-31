package antigravity

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// Agy's stream-json protocol uses event/step_update, unlike Gemini CLI's
// type/message protocol. Only public text deltas and tool progress are relayed;
// internal reasoning and the init tool inventory are not chat messages.
type agyStreamEvent struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	Step           struct {
		ConversationID string `json:"conversation_id"`
		Index          int    `json:"step_index"`
		State          string `json:"state"`
		Type           string `json:"step_type"`
		TextDelta      string `json:"text_delta"`
		ToolName       string `json:"tool_name"`
		Tool           struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
			Output     string         `json:"output"`
		} `json:"tool_info"`
	} `json:"step_update"`
	Result struct {
		ConversationID string `json:"conversation_id"`
		Status         string `json:"status"`
		Response       string `json:"response"`
		Usage          struct {
			InputTokens     int `json:"input_tokens"`
			OutputTokens    int `json:"output_tokens"`
			CacheReadTokens int `json:"cache_read_tokens"`
		} `json:"usage"`
	} `json:"result"`
}

type agyStream struct {
	session  *antigravitySession
	result   core.Event
	text     strings.Builder
	stepText strings.Builder
	textStep int
	tools    map[int]bool
	done     map[int]bool
}

func newAgyStream(session *antigravitySession) *agyStream {
	return &agyStream{session: session, tools: make(map[int]bool), done: make(map[int]bool)}
}

func (s *agyStream) emit(event core.Event) {
	select {
	case s.session.events <- event:
	case <-s.session.ctx.Done():
	}
}

func (s *agyStream) setSessionID(id string) {
	if id != "" && id != s.session.CurrentSessionID() {
		s.session.chatID.Store(id)
		s.emit(core.Event{Type: core.EventText, SessionID: id})
	}
}

func (s *agyStream) handleLine(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	// Preserve support for wrappers/older CLI versions producing plain text.
	// JSON is always parsed, never forwarded as an answer or logged verbatim.
	if !strings.HasPrefix(trimmed, "{") {
		s.text.WriteString(line)
		s.emit(core.Event{Type: core.EventText, Content: line})
		return
	}
	var event agyStreamEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		s.result.Error = fmt.Errorf("antigravity: invalid stream-json event")
		return
	}
	s.setSessionID(event.ConversationID)
	switch event.Event {
	case "step_update":
		s.handleStep(event)
	case "result":
		s.handleResult(event)
	}
}

func (s *agyStream) handleStep(event agyStreamEvent) {
	step := event.Step
	s.setSessionID(step.ConversationID)
	if step.Type == "agent_response" && step.TextDelta != "" {
		if step.Index != s.textStep {
			s.stepText.Reset()
			s.textStep = step.Index
		}
		s.stepText.WriteString(step.TextDelta)
		s.text.WriteString(step.TextDelta)
		s.emit(core.Event{Type: core.EventText, Content: step.TextDelta})
	}
	if step.Type != "tool" {
		return
	}
	name := step.ToolName
	if name == "" {
		name = step.Tool.Name
	}
	if !s.tools[step.Index] {
		s.tools[step.Index] = true
		s.emit(core.Event{Type: core.EventToolUse, ToolName: name, ToolInput: formatAgyToolInput(step.Tool.Parameters)})
	}
	if step.State == "ACTIVE" || step.State == "PENDING" || step.State == "" || s.done[step.Index] {
		return
	}
	s.done[step.Index] = true
	success := step.State == "DONE"
	status := "failed"
	if success {
		status = "completed"
	}
	s.emit(core.Event{Type: core.EventToolResult, ToolName: name, ToolResult: step.Tool.Output, ToolStatus: status, ToolSuccess: &success})
}

func (s *agyStream) handleResult(event agyStreamEvent) {
	r := event.Result
	s.setSessionID(r.ConversationID)
	// result.response is a snapshot of text already streamed. Some CLI versions
	// only populate the result, so retain that fallback without duplicating it.
	text := s.text.String()
	if r.Response != "" && !strings.HasSuffix(text, r.Response) {
		response := r.Response
		if strings.HasPrefix(response, text) {
			response = strings.TrimPrefix(response, text)
		} else if lastStep := s.stepText.String(); lastStep != "" && strings.HasPrefix(response, lastStep) {
			response = strings.TrimPrefix(response, lastStep)
		}
		s.text.WriteString(response)
		s.emit(core.Event{Type: core.EventText, Content: response})
	}
	s.result.InputTokens = r.Usage.InputTokens
	s.result.OutputTokens = r.Usage.OutputTokens
	s.result.CacheReadInputTokens = r.Usage.CacheReadTokens
	if r.Status != "SUCCESS" {
		// Keep errors visible even when the CLI returns exit code zero.
		s.result.Error = fmt.Errorf("antigravity: turn ended with status %s", r.Status)
	}
}
