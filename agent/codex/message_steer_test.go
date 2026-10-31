package codex

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerSession_OrdinaryMessageSteersActiveTurn(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4), ctx: context.Background(), currentTurn: "active", pending: make(map[int64]chan rpcResponseEnvelope)}
	s.alive.Store(true)
	s.threadID.Store("parent")
	w := &asyncSteerWriter{s: s}
	s.stdin = w
	sender, ok := any(s).(core.MessageSteerer)
	if !ok {
		t.Fatal("app-server does not support ordinary message steering")
	}
	accepted, err := sender.SteerMessage("include tests", "message-2", nil, nil)
	if err != nil || !accepted {
		t.Fatalf("SteerMessage = %v, %v", accepted, err)
	}
	if w.request.Method != "turn/steer" || w.request.Params["expectedTurnId"] != "active" || w.request.Params["threadId"] != "parent" {
		t.Fatalf("request = %#v", w.request)
	}
	input := w.request.Params["input"].([]any)[0].(map[string]any)
	if input["text"] != "include tests" {
		t.Fatalf("input = %#v", input)
	}
	if len(s.events) != 0 || s.currentTurn != "active" {
		t.Fatal("steering changed result ownership")
	}
}

func TestAppServerSession_OrdinarySteerPreservesAttachments(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), workDir: t.TempDir(), currentTurn: "active", pending: make(map[int64]chan rpcResponseEnvelope)}
	s.alive.Store(true)
	s.threadID.Store("parent")
	w := &asyncSteerWriter{s: s}
	s.stdin = w
	accepted, err := s.SteerMessage("read attachments", "msg", []core.ImageAttachment{{MimeType: "image/png", Data: []byte("image")}}, []core.FileAttachment{{FileName: "note.txt", Data: []byte("note")}})
	if err != nil || !accepted {
		t.Fatalf("SteerMessage = %v, %v", accepted, err)
	}
	input := w.request.Params["input"].([]any)
	if len(input) != 2 || !strings.Contains(input[0].(map[string]any)["text"].(string), "note.txt") {
		t.Fatalf("input = %v", input)
	}
	image := input[1].(map[string]any)
	data, err := os.ReadFile(image["path"].(string))
	if image["type"] != "localImage" || err != nil || string(data) != "image" {
		t.Fatalf("image = %v, data=%q, err=%v", image, data, err)
	}
}

type uncertainSteerWriter struct{ s *appServerSession }

func (w *uncertainSteerWriter) Write(data []byte) (int, error) {
	var request struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return 0, err
	}
	w.s.handleResponse(rpcResponseEnvelope{ID: request.ID, Error: &rpcError{Message: "connection lost"}})
	return len(data), nil
}
func (*uncertainSteerWriter) Close() error { return nil }

func TestAppServerSession_SteerUncertainTransportDoesNotRequestReplay(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), currentTurn: "active", pending: make(map[int64]chan rpcResponseEnvelope)}
	s.alive.Store(true)
	s.threadID.Store("parent")
	s.stdin = &uncertainSteerWriter{s: s}
	accepted, err := s.SteerMessage("message", "msg", nil, nil)
	if accepted || err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("accepted=%v err=%v", accepted, err)
	}
}

type messageSteerWriter struct {
	s             *appServerSession
	errorResponse bool
}

func (w *messageSteerWriter) Write(data []byte) (int, error) {
	var request struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return 0, err
	}
	// The active turn ends between the snapshot and the server receiving steer.
	w.s.completeTurn()
	response := rpcResponseEnvelope{ID: request.ID, Result: json.RawMessage(`{"turnId":"active"}`)}
	if w.errorResponse {
		response.Error = &rpcError{Code: -32600, Message: "no active turn"}
	}
	w.s.handleResponse(response)
	return len(data), nil
}
func (*messageSteerWriter) Close() error { return nil }

func TestAppServerSession_SteerAtTurnEndDoesNotLoseOrReplayMessage(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		s := &appServerSession{events: make(chan core.Event, 4), ctx: context.Background(), currentTurn: "active", pending: make(map[int64]chan rpcResponseEnvelope)}
		s.alive.Store(true)
		s.threadID.Store("parent")
		s.stdin = &messageSteerWriter{s: s, errorResponse: rejected}
		sender, ok := any(s).(core.MessageSteerer)
		if !ok {
			t.Fatal("missing ordinary steering capability")
		}
		accepted, err := sender.SteerMessage("last message", "last", nil, nil)
		if err != nil || accepted == rejected {
			t.Fatalf("rejected=%v: accepted=%v err=%v", rejected, accepted, err)
		}
		if accepted, err := sender.SteerMessage("idle message", "idle", nil, nil); err != nil || accepted {
			t.Fatalf("idle steer=%v,%v", accepted, err)
		}
	}
}
