package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type stubFastAgent struct {
	stubAgent
	mu         sync.Mutex
	enabled    bool
	supportErr error
}

func (a *stubFastAgent) FastModeSupported() error { return a.supportErr }
func (a *stubFastAgent) SetFastMode(v bool)       { a.mu.Lock(); defer a.mu.Unlock(); a.enabled = v }
func (a *stubFastAgent) FastModeEnabled() bool    { a.mu.Lock(); defer a.mu.Unlock(); return a.enabled }

func TestFastCommand_ValidationAndMissingCapability(t *testing.T) {
	for _, args := range [][]string{{"yes"}, {"on", "off"}} {
		a := &stubFastAgent{}
		p := &stubPlatformEngine{n: "test"}
		e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
		e.cmdFast(p, &Message{SessionKey: "test:user", ReplyCtx: "ctx"}, args)
		if a.FastModeEnabled() || p.getSent()[0] != e.i18n.T(MsgFastUsage) {
			t.Fatalf("args=%v replies=%v", args, p.getSent())
		}
	}
	for _, agent := range []Agent{&stubAgent{}, &stubFastAgent{supportErr: errors.New("requires Codex CLI >= 0.166.0")}} {
		p := &stubPlatformEngine{n: "test"}
		e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
		e.cmdFast(p, &Message{SessionKey: "test:user", ReplyCtx: "ctx"}, []string{"on"})
		if strings.Contains(p.getSent()[0], "preference: on") {
			t.Fatal("unsupported fast reported as enabled")
		}
	}
}

func TestFastCommand_PersistenceFailureLeavesPreferenceUnchanged(t *testing.T) {
	a := &stubFastAgent{}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewProjectStateStore(filepath.Join(file, "state.json"))
	e.SetProjectStateStore(store)
	e.cmdFast(p, &Message{SessionKey: "test:user", ReplyCtx: "ctx"}, []string{"on"})
	if a.FastModeEnabled() || store.ServiceTierOverride("") != "" || !strings.Contains(p.getSent()[0], "not changed") {
		t.Fatalf("state=%q replies=%v", store.ServiceTierOverride(""), p.getSent())
	}
}

func TestFastPreference_WorkspacesPersistIndependently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewProjectStateStore(path)
	global := &stubFastAgent{}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", global, []Platform{p}, "", LangEnglish)
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))
	e.SetProjectStateStore(store)
	for i, cmd := range []string{"on", "off"} {
		dir := normalizeWorkspacePath(t.TempDir())
		channel := []string{"fast-a", "fast-b"}[i]
		key := "test:" + channel + ":u1"
		e.workspaceBindings.Bind("project:test", channel, "chan", dir)
		ws := e.workspacePool.GetOrCreate(dir)
		ws.agent = &stubFastAgent{}
		ws.sessions = NewSessionManager("")
		e.cmdFast(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{cmd})
		tier := []string{"fast", "default"}[i]
		if got := NewProjectStateStore(path).ServiceTierOverride(dir); got != tier {
			t.Fatalf("workspace tier=%q replies=%v", got, p.getSent())
		}
	}
	if global.FastModeEnabled() || store.ServiceTierOverride("") != "" {
		t.Fatal("workspace preference leaked to global")
	}
}

func TestFastHelp_AllLanguagesAndRegistration(t *testing.T) {
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		e := NewEngine("test", &stubFastAgent{}, nil, "", lang)
		if !strings.Contains(e.i18n.T(MsgHelp), "/fast [on|off]") {
			t.Fatalf("missing help for %v", lang)
		}
		if got := e.i18n.T(MsgBuiltinCmdFast); got == string(MsgBuiltinCmdFast) {
			t.Fatalf("missing description for %v", lang)
		}
	}
	if got := matchPrefix("fast", builtinCommands); got != "fast" {
		t.Fatalf("routing=%s", got)
	}
}
