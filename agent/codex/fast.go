package codex

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func normalizeServiceTier(tier string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "":
		return "", nil
	case "fast", "priority":
		return "fast", nil
	case "default":
		return "default", nil
	default:
		return "", fmt.Errorf("codex: unsupported service_tier %q (use fast or default)", tier)
	}
}

func (a *Agent) SetFastMode(enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.serviceTier = "default"
	if enabled {
		a.serviceTier = "fast"
	}
}

func (a *Agent) serviceTierPreference() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.serviceTier
}

func (a *Agent) FastModeEnabled() bool { return a.serviceTierPreference() == "fast" }

var codexVersionPattern = regexp.MustCompile(`codex-cli\s+(\d+)\.(\d+)\.(\d+)`)

// Fast controls here require the CLI contract verified with 0.166.0. Older or
// unidentified binaries must not silently accept an ignored config override.
func (a *Agent) FastModeSupported() error {
	a.mu.RLock()
	backend := a.backend
	a.mu.RUnlock()
	if backend == "exec" {
		return fmt.Errorf("codex: Fast switching is unsupported on exec; use backend=app_server (resume may retain the previous tier)")
	}
	a.fastSupportOnce.Do(func() {
		bin, err := resolveCodexExecutable(a.cmd)
		if err != nil {
			a.fastSupportErr = fmt.Errorf("codex: fast capability: %w", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, "--version").Output()
		if err != nil {
			a.fastSupportErr = fmt.Errorf("codex: fast version check: %w", err)
			return
		}
		parts := codexVersionPattern.FindStringSubmatch(string(out))
		if len(parts) != 4 {
			a.fastSupportErr = fmt.Errorf("codex: fast requires a recognized Codex CLI version >= 0.166.0")
			return
		}
		major, _ := strconv.Atoi(parts[1])
		minor, _ := strconv.Atoi(parts[2])
		if major == 0 && minor < 166 {
			a.fastSupportErr = fmt.Errorf("codex: fast requires Codex CLI >= 0.166.0")
		}
	})
	return a.fastSupportErr
}

func (s *appServerSession) requestedServiceTier() string {
	if s.serviceTier != nil {
		return s.serviceTier()
	}
	return ""
}

func validateServiceTierResponse(requested string, actual *string) error {
	if requested == "" {
		return nil
	}
	value := "default"
	if actual != nil && strings.TrimSpace(*actual) != "" {
		value = *actual
	}
	normalized, err := normalizeServiceTier(value)
	if err != nil || normalized != requested {
		return fmt.Errorf("codex: requested service tier %q, backend returned %q; Fast is not confirmed", requested, value)
	}
	return nil
}
