package core

import (
	"errors"
	"strings"
	"testing"
)

type steerProbeSession struct {
	stubAgentSession
	accepted          bool
	err               error
	prompt, messageID string
	images            []ImageAttachment
	files             []FileAttachment
}

func (s *steerProbeSession) SteerMessage(prompt, id string, images []ImageAttachment, files []FileAttachment) (bool, error) {
	s.prompt, s.messageID, s.images, s.files = prompt, id, images, files
	return s.accepted, s.err
}

func TestBusyMessageSteer_FallbackHistoryAndAttachments(t *testing.T) {
	for _, outcome := range []string{"accepted", "idle", "uncertain", "starting", "backlog"} {
		t.Run(outcome, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			e := NewEngine("steer", &stubAgent{}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
			e.SetInjectSender(true)
			key := "test:chat:user"
			session := e.sessions.GetOrCreateActive(key)
			probe := &steerProbeSession{accepted: outcome == "accepted"}
			if outcome == "uncertain" {
				probe.err = errors.New("steer timed out")
			}
			state := &interactiveState{agentSession: probe, busySession: session}
			if outcome == "starting" {
				state.agentSession = nil
			}
			if outcome == "backlog" {
				state.pendingMessages = []queuedMessage{{content: "earlier"}}
			}
			e.interactiveStates[key] = state
			msg := &Message{SessionKey: key, Platform: "test", UserID: "user", UserName: "Name", Content: "include tests", MessageID: "second", UserMessageTimeMs: 2000,
				Images: []ImageAttachment{{Data: []byte("image")}}, Files: []FileAttachment{{Data: []byte("file")}}}
			if !e.queueMessageForBusySession(p, msg, key) {
				t.Fatal("message not handled")
			}
			queued := outcome == "idle" || outcome == "starting" || outcome == "backlog"
			if queued != (len(state.pendingMessages) > 0) {
				t.Fatalf("pending messages = %v", state.pendingMessages)
			}
			if outcome == "accepted" {
				if !strings.Contains(probe.prompt, "Name") || !strings.Contains(probe.prompt, msg.Content) || probe.messageID != msg.MessageID || len(probe.images) != 1 || len(probe.files) != 1 {
					t.Fatalf("steer input = %#v", probe)
				}
				history := session.GetHistory(0)
				if len(history) != 1 || history[0].Content != msg.Content {
					t.Fatalf("history = %v", history)
				}
				if state.currentTurnUserMessageTimeMs != 2000 {
					t.Fatal("steered input not included in watermark")
				}
				// Older platform messages must not be steered.
				probe.prompt = ""
				msg.UserMessageTimeMs = 1000
				e.queueMessageForBusySession(p, msg, key)
				if probe.prompt != "" {
					t.Fatal("duplicate message steered")
				}
			} else if len(session.GetHistory(0)) != 0 {
				t.Fatal("unaccepted input recorded as delivered")
			}
			if outcome == "uncertain" && !strings.Contains(strings.Join(p.getSent(), "\n"), "steer timed out") {
				t.Fatal("uncertain delivery hidden")
			}
		})
	}
}
