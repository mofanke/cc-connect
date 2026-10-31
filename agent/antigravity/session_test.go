package antigravity

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAvailableModels_UsesConfiguredCLI(t *testing.T) {
	if os.Getenv("GO_WANT_AGY_MODELS_HELPER") == "1" {
		if os.Args[len(os.Args)-1] != "models" || os.Getenv("MODEL_ENV") != "session" || os.Getenv("CONFIG_ONLY") != "yes" || os.Getenv("GEMINI_API_KEY") != "provider-key" {
			os.Exit(2)
		}
		wd, _ := os.Getwd()
		if wd != os.Getenv("EXPECTED_WORKDIR") {
			os.Exit(3)
		}
		_, _ = io.WriteString(os.Stdout, "Fetching available models...\nmodel-one\tModel One\nclaude-sonnet-4-6\tClaude Sonnet 4.6\nmodel-one\tDuplicate\ninvalid\n\tMissing ID\n")
		os.Exit(0)
	}
	wd := t.TempDir()
	a := &Agent{cmd: os.Args[0], cliExtraArgs: []string{"-test.run=^TestAvailableModels_UsesConfiguredCLI$", "--"}, workDir: wd,
		configEnv: []string{"GO_WANT_AGY_MODELS_HELPER=1", "MODEL_ENV=config", "CONFIG_ONLY=yes", "EXPECTED_WORKDIR=" + wd},
		providers: []core.ProviderConfig{{APIKey: "provider-key"}}, activeIdx: 0,
		sessionEnv: []string{"MODEL_ENV=session"}}
	got := a.AvailableModels(context.Background())
	want := []core.ModelOption{{Name: "model-one", Desc: "Model One"}, {Name: "claude-sonnet-4-6", Desc: "Claude Sonnet 4.6"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := a.AvailableModels(ctx); len(got) != 0 {
		t.Fatalf("cancelled discovery returned stale models: %#v", got)
	}
	a.cmd = filepath.Join(t.TempDir(), "missing-agy")
	if got := a.AvailableModels(context.Background()); len(got) != 0 {
		t.Fatalf("failed discovery returned stale models: %#v", got)
	}
	a.providers[0].Models = want[:1]
	if got := a.AvailableModels(context.Background()); !reflect.DeepEqual(got, want[:1]) {
		t.Fatalf("configured models not preferred: %#v", got)
	}
}

func TestReasoningAndModelReachSession(t *testing.T) {
	a, err := New(map[string]any{"cmd": os.Args[0], "work_dir": t.TempDir(), "model": "claude-sonnet-4-6", "reasoning_effort": " HIGH ", "mode": "plan", "env": map[string]string{"CONFIG_ONLY": "a=b"}})
	if err != nil {
		t.Fatal(err)
	}
	rs := a.(core.ReasoningEffortSwitcher)
	if rs.GetReasoningEffort() != "high" || !reflect.DeepEqual(rs.AvailableReasoningEfforts(), []string{"low", "medium", "high"}) {
		t.Fatal("reasoning options not initialized")
	}
	rs.SetReasoningEffort("med")
	a.(core.SessionEnvInjector).SetSessionEnv([]string{"SESSION_ONLY=yes"})
	opts := a.(core.WorkspaceAgentOptionSnapshotter).WorkspaceAgentOptions()
	opts["work_dir"] = t.TempDir()
	copyAgent, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(copyAgent.(*Agent).configEnv, []string{"CONFIG_ONLY=a=b"}) {
		t.Fatalf("config env not preserved: %#v", opts)
	}
	s, err := copyAgent.StartSession(context.Background(), "existing-session")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	args := s.(*antigravitySession).buildAntigravityArgs("existing-session", true, "plan", "", "hello")
	want := []string{"--effort", "medium", "--model", "claude-sonnet-4-6", "--conversation", "existing-session", "--sandbox", "-p", "hello"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	rs.SetReasoningEffort("xhigh")
	if rs.GetReasoningEffort() != "" {
		t.Fatal("unsupported effort should use CLI default")
	}
}

func TestParseModelLines_EmptyOrMalformed(t *testing.T) {
	for _, out := range []string{"", "Fetching available models...\n", "bad\n\tmissing\nid\t\n"} {
		if got := parseModelLines(out); len(got) != 0 {
			t.Fatalf("unexpected models: %#v", got)
		}
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"cc-connect", "cc-connect"},
		{"Daily", "daily"},
		{"My Project", "my-project"},
		{"hello_world", "hello-world"},
		{"Test.123", "test-123"},
		{"---weird---", "weird"},
		{"", "project"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slugify(tt.input)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"default", "default"},
		{"yolo", "yolo"},
		{"auto", "yolo"},
		{"force", "yolo"},
		{"plan", "plan"},
		{"sandbox", "plan"},
		{"invalid", "default"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeMode(tt.input)
			if got != tt.want {
				t.Errorf("normalizeMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSession_ContinueSessionTreatedAsFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "default", core.ContinueSession, nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if got := s.CurrentSessionID(); got != "" {
		t.Errorf("ContinueSession should be treated as fresh: chatID = %q, want empty", got)
	}
}

func TestBuildAntigravityArgs_PromptAtEnd(t *testing.T) {
	s, _ := newAntigravitySession(context.Background(), "echo", []string{"--verbose"}, "/tmp", "", "default", "", nil, 0)
	args := s.buildAntigravityArgs("sid-1", true, "plan", "/tmp/agy-config", "What is 1+1?")
	if len(args) < 2 {
		t.Fatalf("args too short: %v", args)
	}
	if args[len(args)-2] != "-p" || args[len(args)-1] != "What is 1+1?" {
		t.Fatalf("expected prompt to be final '-p <prompt>', got: %v", args)
	}
	if !contains(args, "--sandbox") {
		t.Fatalf("expected --sandbox in args, got: %v", args)
	}
	if !contains(args, "--gemini_dir=/tmp/agy-config") || !contains(args, "--print-timeout=24h") {
		t.Fatalf("expected isolated Agy config and extended print timeout, got: %v", args)
	}
	if !contains(args, "--verbose") {
		t.Fatalf("expected configured extra args, got: %v", args)
	}
	if contains(args, "-m") || contains(args, "--model") {
		t.Fatalf("did not expect model flags in args, got: %v", args)
	}
}

func TestAntigravitySession_ResumePassesConversationID(t *testing.T) {
	if os.Getenv("GO_WANT_ANTIGRAVITY_HELPER") == "1" {
		argsPath := os.Getenv("CC_ANTIGRAVITY_ARGS_FILE")
		if argsPath == "" {
			os.Exit(2)
		}
		if err := os.WriteFile(argsPath, []byte(strings.Join(os.Args, "\x00")), 0o600); err != nil {
			os.Exit(2)
		}
		_, _ = io.WriteString(os.Stdout, "ok\n")
		os.Exit(0)
	}

	argsPath := t.TempDir() + string(os.PathSeparator) + "args"
	workDir := t.TempDir()
	s, err := newAntigravitySession(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=TestAntigravitySession_ResumePassesConversationID", "--"},
		workDir,
		"",
		"default",
		"conversation-1",
		[]string{
			"GO_WANT_ANTIGRAVITY_HELPER=1",
			"CC_ANTIGRAVITY_ARGS_FILE=" + argsPath,
		},
		0,
	)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Send("second turn", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case event := <-s.Events():
		if event.Type != core.EventText {
			t.Fatalf("first event type = %q, want %q", event.Type, core.EventText)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for helper output")
	}
	select {
	case event := <-s.Events():
		if event.Type != core.EventResult || !event.Done {
			t.Fatalf("completion event = %+v, want done EventResult", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for EventResult")
	}

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	args := strings.Split(string(data), "\x00")
	if !contains(args, "--conversation") || !contains(args, "conversation-1") {
		t.Fatalf("resume args = %v, want --conversation conversation-1", args)
	}
	if !contains(args, "-p") || !contains(args, "second turn") {
		t.Fatalf("prompt args = %v, want -p second turn", args)
	}
}

func TestDefaultModeCreatesPermissionBridge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "default", "", nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if s.permissionBridge == nil {
		t.Fatal("permissionBridge = nil, want default-mode permission bridge")
	}
	if _, err := os.Stat(filepath.Join(s.permissionBridge.AgyConfigDir(), "config", "hooks.json")); err != nil {
		t.Fatalf("stat Agy hook overlay: %v", err)
	}
}

func TestNonDefaultModesDoNotCreatePermissionBridge(t *testing.T) {
	for _, mode := range []string{"yolo", "plan"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())

			s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", mode, "", nil, 0)
			if err != nil {
				t.Fatalf("newAntigravitySession: %v", err)
			}
			defer func() { _ = s.Close() }()

			if s.permissionBridge != nil {
				t.Fatalf("permissionBridge = %v, want nil", s.permissionBridge)
			}
		})
	}
}

func TestRespondPermissionRequiresDefaultMode(t *testing.T) {
	s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "plan", "", nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	err = s.RespondPermission("req", core.PermissionResult{Behavior: "allow"})
	if err == nil || !strings.Contains(err.Error(), "only available in default mode") {
		t.Fatalf("RespondPermission() error = %v, want default-mode error", err)
	}
}

func TestSendDoesNotHoldStdinOpen(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	workDir := t.TempDir()
	cmdPath := filepath.Join(t.TempDir(), "fake-agy.sh")
	script := "#!/bin/sh\ncat >/dev/null\nprintf 'done\\n'\n"
	if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile fake agy: %v", err)
	}

	s, err := newAntigravitySession(context.Background(), cmdPath, nil, workDir, "", "default", "", nil, 2*time.Second)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.After(3 * time.Second)
	var text strings.Builder
	for {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case core.EventPermissionRequest:
				t.Fatal("unexpected permission request from unstructured stdout")
			case core.EventText:
				text.WriteString(ev.Content)
			case core.EventResult:
				if !strings.Contains(text.String(), "done") {
					t.Fatalf("text = %q, want done", text.String())
				}
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for agy process to receive stdin EOF")
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if strings.TrimSpace(x) == want {
			return true
		}
	}
	return false
}
