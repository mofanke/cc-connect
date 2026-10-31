package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestTraeAgentListSessions_IncludesForkedSessions(t *testing.T) {
	fakeTraeCLIOnPath(t)
	traeHome := t.TempDir()
	workDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeTraeSession(t, traeHome, "2026/09/12", "parent", workDir, "")
	writeTraeSession(t, traeHome, "2026/09/12", "fork", workDir, "parent")
	writeTraeSession(t, traeHome, "2026/09/12", "other-project", t.TempDir(), "")
	index := "" +
		`{"id":"parent","thread_name":"Original","updated_at":"2030-09-12T01:00:00Z"}` + "\n" +
		`{"id":"fork","thread_name":"Fork draft","updated_at":"2030-09-12T02:00:00Z"}` + "\n" +
		`{"id":"fork","thread_name":"Fork final","updated_at":"2030-09-12T03:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(traeHome, "cli", "session_index.jsonl"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}

	agent, err := New(map[string]any{
		"cmd":       "traecli",
		"args":      []string{"acp", "serve"},
		"work_dir":  workDir,
		"trae_home": traeHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("ListSessions() returned %d sessions, want parent and fork: %+v", len(sessions), sessions)
	}
	if sessions[0].ID != "fork" || sessions[0].Summary != "Fork final" {
		t.Fatalf("sessions[0] = %+v, want latest fork with latest indexed title", sessions[0])
	}
	if sessions[0].MessageCount != 2 {
		t.Fatalf("fork MessageCount = %d, want 2", sessions[0].MessageCount)
	}
}

func TestResolveTraeDataDir_AcceptsDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveTraeDataDir(dataDir); got != dataDir {
		t.Fatalf("resolveTraeDataDir() = %q, want %q", got, dataDir)
	}
}

func TestTraeAgentListSessions_CurrentHistoryFormat(t *testing.T) {
	fakeTraeCLIOnPath(t)
	traeHome := t.TempDir()
	workDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(traeHome, "cli", "sessions", "2026", "09", "12")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf(`{"type":"session_meta","payload":{"id":"current","cwd":%q,"source":"cli"}}`, workDir) + "\n" +
		`{"type":"history_mutation","payload":{"operation":"append","items":[{"type":"message","id":"developer-1","role":"developer","content":[{"type":"input_text","text":"instructions"}]},{"type":"message","id":"user-1","role":"user","content":[{"type":"input_text","text":"Fix the session list"}]}]}}` + "\n" +
		`{"type":"history_mutation","payload":{"operation":"append","items":[{"type":"message","id":"assistant-1","role":"assistant","content":[{"type":"output_text","text":"Done"}]}]}}` + "\n" +
		`{"type":"history_mutation","payload":{"operation":"replace","items":[{"type":"message","id":"user-1","role":"user","content":[{"type":"input_text","text":"Fix the session list"}]}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-current.jsonl"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(traeHome, "cli", "session_index.jsonl"), []byte(`{"id":"current","thread_name":"","updated_at":"2030-09-12T01:00:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	agent, err := New(map[string]any{"cmd": "traecli", "work_dir": workDir, "trae_home": traeHome})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() returned %d sessions, want 1", len(sessions))
	}
	if sessions[0].MessageCount != 2 {
		t.Fatalf("MessageCount = %d, want 2 unique user/assistant messages", sessions[0].MessageCount)
	}
	if sessions[0].Summary != "Fix the session list" {
		t.Fatalf("Summary = %q, want first user message", sessions[0].Summary)
	}
}

func writeTraeSession(t *testing.T, traeHome, date, id, cwd, forkedFrom string) {
	t.Helper()
	dir := filepath.Join(traeHome, "cli", "sessions", filepath.FromSlash(date))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	forkField := ""
	if forkedFrom != "" {
		forkField = fmt.Sprintf(`,"forked_from_id":%q`, forkedFrom)
	}
	contents := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q%s,"cwd":%q,"source":"cli"}}`, id, forkField, cwd) + "\n" +
		`{"type":"response_item","payload":{"role":"user"}}` + "\n" +
		`{"type":"response_item","payload":{"role":"assistant"}}` + "\n"
	path := filepath.Join(dir, "rollout-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
