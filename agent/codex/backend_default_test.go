package codex

import "testing"

func TestNew_DefaultBackendSupportsOrdinarySteering(t *testing.T) {
	for _, backend := range []string{"", "app_server", "exec"} {
		agent, err := New(map[string]any{"work_dir": t.TempDir(), "backend": backend})
		if err != nil {
			t.Fatal(err)
		}
		want := "app_server"
		if backend == "exec" {
			want = "exec"
		}
		if got := agent.(*Agent).backend; got != want {
			t.Fatalf("backend=%q: got %q, want %q", backend, got, want)
		}
	}
}
