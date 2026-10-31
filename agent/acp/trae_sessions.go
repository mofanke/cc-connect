package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type traeSessionTitle struct {
	title     string
	updatedAt time.Time
}

func (a *TraeAgent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	a.mu.RLock()
	workDir := a.workDir
	traeHome := a.traeHome
	configuredHome := a.staticEnv["TRAE_HOME"]
	a.mu.RUnlock()

	if traeHome == "" {
		traeHome = configuredHome
	}
	sessions, storeFound, err := listTraeSessions(workDir, traeHome)
	if err != nil {
		return nil, err
	}
	if storeFound {
		return sessions, nil
	}
	return a.Agent.ListSessions(ctx)
}

func resolveTraeDataDir(explicit string) string {
	root := strings.TrimSpace(explicit)
	if root == "" {
		root = strings.TrimSpace(os.Getenv("TRAE_HOME"))
	}
	if root == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(homeDir, ".trae")
	}
	if _, err := os.Stat(filepath.Join(root, "sessions")); err == nil {
		return root
	}
	return filepath.Join(root, "cli")
}

func listTraeSessions(workDir, traeHome string) ([]core.AgentSessionInfo, bool, error) {
	dataDir := resolveTraeDataDir(traeHome)
	sessionsDir := filepath.Join(dataDir, "sessions")
	stat, err := os.Stat(sessionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !stat.IsDir() {
		return nil, false, nil
	}

	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}
	titles := loadTraeSessionTitles(dataDir)
	byID := make(map[string]core.AgentSessionInfo)
	err = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		session := parseTraeSessionFile(path, absWorkDir)
		if session == nil {
			return nil
		}
		if indexed, ok := titles[session.ID]; ok {
			if indexed.title != "" {
				session.Summary = truncateRunes(indexed.title, 60)
			}
			if indexed.updatedAt.After(session.ModifiedAt) {
				session.ModifiedAt = indexed.updatedAt
			}
		}
		if existing, ok := byID[session.ID]; !ok || session.ModifiedAt.After(existing.ModifiedAt) {
			byID[session.ID] = *session
		}
		return nil
	})
	if err != nil {
		return nil, true, err
	}

	out := make([]core.AgentSessionInfo, 0, len(byID))
	for _, session := range byID {
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModifiedAt.After(out[j].ModifiedAt) })
	return out, true, nil
}

func loadTraeSessionTitles(dataDir string) map[string]traeSessionTitle {
	f, err := os.Open(filepath.Join(dataDir, "session_index.jsonl"))
	if err != nil {
		return nil
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Warn("acp: failed to close Trae session index", "error", err)
		}
	}()

	titles := make(map[string]traeSessionTitle)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
			UpdatedAt  string `json:"updated_at"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.ID == "" {
			continue
		}
		updatedAt, _ := time.Parse(time.RFC3339Nano, entry.UpdatedAt)
		current, exists := titles[entry.ID]
		if !exists || current.updatedAt.IsZero() || !updatedAt.Before(current.updatedAt) {
			titles[entry.ID] = traeSessionTitle{title: strings.TrimSpace(entry.ThreadName), updatedAt: updatedAt}
		}
	}
	return titles
}

func parseTraeSessionFile(path, cwdFilter string) *core.AgentSessionInfo {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil
	}
	reader := bufio.NewReader(f)
	firstLine, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	var envelope struct {
		Type    string `json:"type"`
		Payload struct {
			ID     string          `json:"id"`
			Cwd    string          `json:"cwd"`
			Source json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if json.Unmarshal(firstLine, &envelope) != nil || envelope.Type != "session_meta" || envelope.Payload.ID == "" {
		return nil
	}
	if cwdFilter != "" && envelope.Payload.Cwd != "" && !strings.EqualFold(filepath.Clean(envelope.Payload.Cwd), filepath.Clean(cwdFilter)) {
		return nil
	}
	if isTraeSubagentSessionSource(envelope.Payload.Source) {
		return nil
	}

	messageCount := 0
	messageIDs := make(map[string]struct{})
	summary := ""
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 256*1024), 16*1024*1024)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				Items []struct {
					Type    string `json:"type"`
					ID      string `json:"id"`
					Role    string `json:"role"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"items"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.Type == "response_item" && isTraeConversationRole(entry.Payload.Role) {
			messageCount++
			if summary == "" && entry.Payload.Role == "user" {
				summary = firstTraeText(entry.Payload.Content)
			}
		}
		if entry.Type != "history_mutation" {
			continue
		}
		for _, item := range entry.Payload.Items {
			if item.Type != "message" || !isTraeConversationRole(item.Role) {
				continue
			}
			if item.ID != "" {
				if _, exists := messageIDs[item.ID]; exists {
					continue
				}
				messageIDs[item.ID] = struct{}{}
			}
			messageCount++
			if summary == "" && item.Role == "user" {
				summary = firstTraeText(item.Content)
			}
		}
	}

	return &core.AgentSessionInfo{
		ID:           envelope.Payload.ID,
		Summary:      truncateRunes(strings.TrimSpace(summary), 60),
		MessageCount: messageCount,
		ModifiedAt:   stat.ModTime(),
	}
}

func isTraeConversationRole(role string) bool {
	return role == "user" || role == "assistant"
}

func firstTraeText(content []struct {
	Text string `json:"text"`
}) string {
	for _, part := range content {
		if text := strings.TrimSpace(part.Text); text != "" {
			return text
		}
	}
	return ""
}

func isTraeSubagentSessionSource(source json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(source, &object) != nil {
		return false
	}
	_, ok := object["subagent"]
	return ok
}
