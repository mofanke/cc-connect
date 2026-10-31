package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

type fastRPCWriter struct {
	s        *appServerSession
	requests []struct {
		Method string
		Params map[string]any
	}
	tier *string
	err  *rpcError
}

func (w *fastRPCWriter) Close() error { return nil }
func (w *fastRPCWriter) Write(data []byte) (int, error) {
	var req struct {
		ID     int64
		Method string
		Params map[string]any
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return 0, err
	}
	w.requests = append(w.requests, struct {
		Method string
		Params map[string]any
	}{req.Method, req.Params})
	result := map[string]any{"turn": map[string]any{"id": "active"}, "turnId": "active", "thread": map[string]any{"id": "parent"}, "serviceTier": w.tier}
	raw, _ := json.Marshal(result)
	w.s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: raw, Error: w.err})
	return len(data), nil
}

func fastTestSession(a *Agent) (*appServerSession, *fastRPCWriter) {
	s := &appServerSession{events: make(chan core.Event, 8), ctx: context.Background(), pending: make(map[int64]chan rpcResponseEnvelope), serviceTier: a.serviceTierPreference}
	s.alive.Store(true)
	s.threadID.Store("parent")
	tier := "fast"
	w := &fastRPCWriter{s: s, tier: &tier}
	s.stdin = w
	return s, w
}

func TestFastPreference_NewResumeAndNextTurnPreserveSteer(t *testing.T) {
	a := &Agent{}
	a.SetFastMode(true)
	s, w := fastTestSession(a)
	for _, resume := range []string{"", "existing-thread"} {
		if err := s.ensureThread(resume); err != nil {
			t.Fatal(err)
		}
		if got := w.requests[len(w.requests)-1].Params["serviceTier"]; got != "fast" {
			t.Fatalf("thread tier=%v", got)
		}
	}
	if err := s.Send("first", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := w.requests[len(w.requests)-1].Params["serviceTierForTurn"]; got != "fast" {
		t.Fatalf("turn tier=%v", got)
	}
	a.SetFastMode(false)
	if accepted, err := s.SteerMessage("follow up", "m2", nil, nil); !accepted || err != nil {
		t.Fatalf("steer=%v,%v", accepted, err)
	}
	req := w.requests[len(w.requests)-1]
	if req.Method != "turn/steer" || req.Params["serviceTier"] != nil || req.Params["serviceTierForTurn"] != nil {
		t.Fatalf("steer changed tier: %+v", req)
	}
	if s.currentTurn != "active" || len(s.events) != 0 {
		t.Fatal("fast switch ended active turn")
	}
	s.completeTurn()
	if err := s.Send("next", "m3", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := w.requests[len(w.requests)-1].Params["serviceTierForTurn"]; got != "default" {
		t.Fatalf("off turn tier=%v", got)
	}
	w.tier = nil
	if err := s.ensureThread("existing-thread"); err != nil {
		t.Fatal(err)
	}
	if got := w.requests[len(w.requests)-1].Params["serviceTier"]; got != "default" {
		t.Fatalf("off resume tier=%v", got)
	}
}

func TestFastPreference_BackendIgnoredOrRejectedIsNotConfirmed(t *testing.T) {
	a := &Agent{}
	a.SetFastMode(true)
	for _, resume := range []string{"", "existing"} {
		s, w := fastTestSession(a)
		w.tier = nil
		if err := s.ensureThread(resume); err == nil || !strings.Contains(err.Error(), "not confirmed") {
			t.Fatalf("missing echo=%v", err)
		}
	}
	s, w := fastTestSession(a)
	w.err = &rpcError{Code: -32602, Message: "account does not support priority"}
	if err := s.Send("test", "m", nil, nil); err == nil || !strings.Contains(err.Error(), "account does not support") {
		t.Fatalf("rejection=%v", err)
	}
	if s.currentTurn != "" {
		t.Fatal("rejected turn became active")
	}
	priority := "priority"
	if err := validateServiceTierResponse("fast", &priority); err != nil {
		t.Fatal(err)
	}
}

func TestFastPreference_ExecCannotClaimToSwitchPersistedTier(t *testing.T) {
	a := &Agent{backend: "exec"}
	if err := a.FastModeSupported(); err == nil || !strings.Contains(err.Error(), "unsupported on exec") {
		t.Fatalf("exec capability=%v", err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(map[string]any{"cmd": bin, "backend": "exec", "service_tier": "fast"}); err == nil {
		t.Fatal("exec accepted unsupported Fast config")
	}
}

func TestFastPreference_ConfigValidationAndWorkspaceSnapshot(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range []string{"fast", "priority", "default", ""} {
		ag, err := New(map[string]any{"cmd": bin, "service_tier": tier})
		if err != nil {
			t.Fatal(err)
		}
		a := ag.(*Agent)
		if a.FastModeEnabled() != (tier == "fast" || tier == "priority") {
			t.Fatalf("tier=%q", tier)
		}
		a.SetFastMode(true)
		if a.WorkspaceAgentOptions()["service_tier"] != "fast" {
			t.Fatal("workspace lost preference")
		}
	}
	if _, err := New(map[string]any{"cmd": bin, "service_tier": "typo"}); err == nil {
		t.Fatal("invalid tier accepted")
	}
}

func TestFastPreference_ConcurrentRequestsAndChanges(t *testing.T) {
	a := &Agent{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				a.SetFastMode(j%2 == 0)
				_ = a.serviceTierPreference()
				_ = a.WorkspaceAgentOptions()
			}
		}()
	}
	wg.Wait()
}

func TestFastPreference_OldOrUnknownCLICannotReportSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixture")
	}
	for _, version := range []string{"codex-cli 0.165.0", "unknown-wrapper", "codex-cli 0.166.0", "codex-cli 1.0.0"} {
		bin := filepath.Join(t.TempDir(), "codex")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		a := &Agent{cmd: bin}
		err := a.FastModeSupported()
		wantSupported := version == "codex-cli 0.166.0" || version == "codex-cli 1.0.0"
		if (err == nil) != wantSupported {
			t.Fatalf("version=%s err=%v", version, err)
		}
	}
}
