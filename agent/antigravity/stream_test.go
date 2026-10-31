package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAntigravitySession_StreamsToolProgressBeforeExit(t *testing.T) {
	if os.Getenv("GO_WANT_AGY_STREAM_HELPER") == "1" {
		args := strings.Join(os.Args, " ")
		if !strings.Contains(args, "--output-format stream-json") {
			os.Exit(2)
		}
		fmt.Println(`{"event":"init","conversation_id":"stream-session"}`)
		fmt.Println(`{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"parameters":{"CommandLine":"pwd"}}}}`)
		// Stay alive until the parent has observed progress. This proves events
		// are delivered during execution rather than replayed at process exit.
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(os.Getenv("AGY_STREAM_RELEASE")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Println(`{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"output":"/tmp"}}}`)
		// Deliberately split UTF-8 and produce a line larger than Scanner's
		// default limit. Also leave the final record without a trailing newline.
		delta := strings.Repeat("中", 25000)
		line, _ := json.Marshal(map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": 3, "state": "ACTIVE", "step_type": "agent_response", "text_delta": delta}})
		mid := strings.Index(string(line), "中") + 1
		_, _ = os.Stdout.Write(line[:mid])
		_, _ = os.Stdout.Write(append(line[mid:], '\n'))
		result, _ := json.Marshal(map[string]any{"event": "result", "result": map[string]any{"conversation_id": "stream-session", "status": "SUCCESS", "response": delta, "usage": map[string]int{"input_tokens": 123, "output_tokens": 25, "cache_read_tokens": 10}}})
		_, _ = os.Stdout.Write(result)
		os.Exit(0)
	}
	t.Setenv("HOME", t.TempDir())
	release := filepath.Join(t.TempDir(), "release")
	s, err := newAntigravitySession(context.Background(), os.Args[0], []string{"-test.run=^TestAntigravitySession_StreamsToolProgressBeforeExit$", "--"}, t.TempDir(), "", "yolo", "", []string{"GO_WANT_AGY_STREAM_HELPER=1", "AGY_STREAM_RELEASE=" + release}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send("pwd", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	next := func() core.Event {
		t.Helper()
		select {
		case event := <-s.Events():
			return event
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for stream progress")
			return core.Event{}
		}
	}
	if e := next(); e.SessionID != "stream-session" || s.CurrentSessionID() != "stream-session" {
		t.Fatalf("init = %+v", e)
	}
	if e := next(); e.Type != core.EventToolUse || e.ToolName != "run_command" || e.ToolInput != "pwd" {
		t.Fatalf("live progress = %+v", e)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if e := next(); e.Type != core.EventToolResult || e.ToolResult != "/tmp" || e.ToolStatus != "completed" || e.ToolSuccess == nil || !*e.ToolSuccess {
		t.Fatalf("tool result = %+v", e)
	}
	if e := next(); e.Type != core.EventText || e.Content != strings.Repeat("中", 25000) {
		t.Fatalf("text type=%s length=%d", e.Type, len(e.Content))
	}
	if e := next(); e.Type != core.EventResult || !e.Done || e.Error != nil || e.SessionID != "stream-session" || e.InputTokens != 123 || e.OutputTokens != 25 || e.CacheReadInputTokens != 10 {
		t.Fatalf("final result = %+v", e)
	}
	s.Close()
	for event := range s.Events() {
		t.Fatalf("duplicate final/text event: %+v", event)
	}
}

func TestAgyStream_DeduplicatesToolUpdatesAndIgnoresInternalEvents(t *testing.T) {
	session := &antigravitySession{ctx: context.Background(), events: make(chan core.Event, 20)}
	s := newAgyStream(session)
	for _, line := range []string{
		`{"event":"step_update","step_update":{"step_type":"user_input","text_delta":"user prompt"}}`,
		`{"event":"step_update","step_update":{"step_type":"thinking","text_delta":"private reasoning"}}`,
		`{"event":"unknown","text":"do not forward"}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_info":{"name":"view_file","parameters":{"AbsolutePath":"/tmp/a"}}}}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_info":{"name":"view_file"}}}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_info":{"name":"view_file","output":"contents"}}}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_info":{"name":"view_file","output":"contents"}}}`,
	} {
		s.handleLine(line)
	}
	if len(session.events) != 2 {
		t.Fatalf("event count = %d, want one start and one completion", len(session.events))
	}
	start, end := <-session.events, <-session.events
	if start.Type != core.EventToolUse || !strings.Contains(start.ToolInput, "/tmp/a") || end.Type != core.EventToolResult || end.ToolResult != "contents" {
		t.Fatalf("tool events = %+v / %+v", start, end)
	}
}

func TestAgyStream_ResultFallbackAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, delta, response, status, want string
		wantError                           bool
	}{
		{"result only", "", "OK", "SUCCESS", "OK", false},
		{"duplicate snapshot", "OK", "OK", "SUCCESS", "OK", false},
		{"remaining suffix", "O", "OK", "SUCCESS", "OK", false},
		{"failure with exit zero", "", "", "ERROR", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &antigravitySession{ctx: context.Background(), events: make(chan core.Event, 20)}
			s := newAgyStream(session)
			step, _ := json.Marshal(map[string]any{"event": "step_update", "step_update": map[string]any{"step_type": "agent_response", "text_delta": tc.delta}})
			s.handleLine(string(step))
			result, _ := json.Marshal(map[string]any{"event": "result", "result": map[string]any{"status": tc.status, "response": tc.response}})
			s.handleLine(string(result))
			var text strings.Builder
			for len(session.events) > 0 {
				text.WriteString((<-session.events).Content)
			}
			if text.String() != tc.want || (s.result.Error != nil) != tc.wantError {
				t.Fatalf("text=%q error=%v", text.String(), s.result.Error)
			}
		})
	}
}

func TestAgyStream_FinalSuffixAfterEarlierCommentary(t *testing.T) {
	session := &antigravitySession{ctx: context.Background(), events: make(chan core.Event, 20)}
	s := newAgyStream(session)
	s.handleLine(`{"event":"step_update","step_update":{"step_index":1,"step_type":"agent_response","text_delta":"Checking files.\n"}}`)
	s.handleLine(`{"event":"step_update","step_update":{"step_index":3,"step_type":"agent_response","text_delta":"O"}}`)
	s.handleLine(`{"event":"result","result":{"status":"SUCCESS","response":"OK"}}`)
	var text strings.Builder
	for len(session.events) > 0 {
		text.WriteString((<-session.events).Content)
	}
	if got := text.String(); got != "Checking files.\nOK" {
		t.Fatalf("duplicated final text: %q", got)
	}
}

func TestAgyStream_ToolFailureWithoutActiveUpdate(t *testing.T) {
	session := &antigravitySession{ctx: context.Background(), events: make(chan core.Event, 20)}
	s := newAgyStream(session)
	s.handleLine(`{"event":"step_update","step_update":{"step_index":2,"state":"FAILED","step_type":"tool","tool_name":"run_command","tool_info":{"output":"command failed"}}}`)
	if start := <-session.events; start.Type != core.EventToolUse {
		t.Fatalf("tool start = %+v", start)
	}
	if end := <-session.events; end.Type != core.EventToolResult || end.ToolSuccess == nil || *end.ToolSuccess || end.ToolStatus != "failed" || end.ToolResult != "command failed" {
		t.Fatalf("tool failure = %+v", end)
	}
}

func TestAgyStream_MalformedJSONNeverLeaksAsText(t *testing.T) {
	session := &antigravitySession{ctx: context.Background(), events: make(chan core.Event, 20)}
	s := newAgyStream(session)
	s.handleLine(`{"event":"step_update","secret":"do not display"`)
	if s.result.Error == nil || len(session.events) != 0 || strings.Contains(s.result.Error.Error(), "secret") {
		t.Fatal("malformed JSON must report a safe error without forwarding raw data")
	}
	s.handleLine("plain text\n")
	if e := <-session.events; !reflect.DeepEqual(e, core.Event{Type: core.EventText, Content: "plain text\n"}) {
		t.Fatalf("plain text fallback = %+v", e)
	}
}

// Opt-in provider check: exercises the actual CLI without sending IM messages.
func TestAntigravitySession_LiveStream(t *testing.T) {
	cmd := os.Getenv("CC_CONNECT_TEST_AGY_CMD")
	if cmd == "" {
		t.Skip("set CC_CONNECT_TEST_AGY_CMD to run the live AGY provider check")
	}
	s, err := newAntigravitySession(context.Background(), cmd, []string{"--print-timeout=45s"}, t.TempDir(), os.Getenv("CC_CONNECT_TEST_AGY_MODEL"), "yolo", "", nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	started := time.Now()
	if err := s.Send("Run pwd once, then reply with only OK. Do not read or edit files or call any other tool.", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	toolStarted, toolCompleted := false, false
	var firstProgress time.Duration
	var text strings.Builder
	deadline := time.After(time.Minute)
	for {
		select {
		case event := <-s.Events():
			switch event.Type {
			case core.EventError:
				t.Fatal(event.Error)
			case core.EventToolUse:
				toolStarted = true
				firstProgress = time.Since(started)
			case core.EventToolResult:
				toolCompleted = event.ToolSuccess != nil && *event.ToolSuccess
			case core.EventText:
				text.WriteString(event.Content)
			case core.EventResult:
				if event.Error != nil || !toolStarted || !toolCompleted || strings.TrimSpace(text.String()) != "OK" || event.SessionID == "" {
					t.Fatalf("live result error=%v tool_started=%v tool_completed=%v response_len=%d has_session=%v", event.Error, toolStarted, toolCompleted, text.Len(), event.SessionID != "")
				}
				t.Logf("live tool progress at %s; turn completed at %s; response=%q", firstProgress, time.Since(started), strings.TrimSpace(text.String()))
				return
			}
		case <-deadline:
			t.Fatal("live AGY stream timed out")
		}
	}
}
