package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

type projectStateData struct {
	ServiceTierOverride           string            `json:"service_tier_override,omitempty"`
	WorkspaceServiceTierOverrides map[string]string `json:"workspace_service_tier_overrides,omitempty"`
	WorkDirOverride               string            `json:"work_dir_override,omitempty"`
	WorkspaceDirOverrides         map[string]string `json:"workspace_dir_overrides,omitempty"`
	WorkspaceModelOverrides       map[string]string `json:"workspace_model_overrides,omitempty"`
}

// ProjectStateStore persists lightweight runtime state for one project.
type ProjectStateStore struct {
	mu        sync.RWMutex
	storePath string
	state     projectStateData
}

func NewProjectStateStore(path string) *ProjectStateStore {
	ps := &ProjectStateStore{storePath: path}
	if path != "" {
		ps.load()
	}
	return ps
}

func (ps *ProjectStateStore) WorkDirOverride() string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.state.WorkDirOverride
}

func (ps *ProjectStateStore) SetWorkDirOverride(dir string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.state.WorkDirOverride = dir
}

func (ps *ProjectStateStore) WorkspaceDirOverride(workspace string) string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if ps.state.WorkspaceDirOverrides == nil {
		return ""
	}
	return ps.state.WorkspaceDirOverrides[workspace]
}

func (ps *ProjectStateStore) SetWorkspaceDirOverride(workspace, dir string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceDirOverrides == nil {
		ps.state.WorkspaceDirOverrides = make(map[string]string)
	}
	ps.state.WorkspaceDirOverrides[workspace] = dir
}

func (ps *ProjectStateStore) ClearWorkspaceDirOverride(workspace string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceDirOverrides == nil {
		return
	}
	delete(ps.state.WorkspaceDirOverrides, workspace)
	if len(ps.state.WorkspaceDirOverrides) == 0 {
		ps.state.WorkspaceDirOverrides = nil
	}
}

func (ps *ProjectStateStore) WorkspaceModelOverride(workspace string) string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if ps.state.WorkspaceModelOverrides == nil {
		return ""
	}
	return ps.state.WorkspaceModelOverrides[workspace]
}

func (ps *ProjectStateStore) SetWorkspaceModelOverride(workspace, model string) {
	if model == "" {
		ps.ClearWorkspaceModelOverride(workspace)
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceModelOverrides == nil {
		ps.state.WorkspaceModelOverrides = make(map[string]string)
	}
	ps.state.WorkspaceModelOverrides[workspace] = model
}

func (ps *ProjectStateStore) ClearWorkspaceModelOverride(workspace string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.state.WorkspaceModelOverrides == nil {
		return
	}
	delete(ps.state.WorkspaceModelOverrides, workspace)
	if len(ps.state.WorkspaceModelOverrides) == 0 {
		ps.state.WorkspaceModelOverrides = nil
	}
}

func (ps *ProjectStateStore) ClearWorkDirOverride() {
	ps.SetWorkDirOverride("")
}

func (ps *ProjectStateStore) Save() {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if err := ps.saveLocked(); err != nil {
		slog.Error("project_state: failed to save", "error", err)
	}
}

func (ps *ProjectStateStore) saveLocked() error {
	if ps.storePath == "" {
		return nil
	}

	data, err := json.MarshalIndent(ps.state, "", "  ")
	if err != nil {
		return fmt.Errorf("project state: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(ps.storePath), 0o755); err != nil {
		return fmt.Errorf("project state: create directory: %w", err)
	}
	if err := AtomicWriteFile(ps.storePath, data, 0o644); err != nil {
		return fmt.Errorf("project state: write: %w", err)
	}
	return nil
}

func (ps *ProjectStateStore) load() {
	data, err := os.ReadFile(ps.storePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("project_state: failed to read", "path", ps.storePath, "error", err)
		}
		return
	}

	var state projectStateData
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Error("project_state: failed to unmarshal", "path", ps.storePath, "error", err)
		return
	}
	ps.state = state
}

// ServiceTierOverride returns the persisted request preference for a workspace.
// The empty workspace identifies the default project agent.
func (ps *ProjectStateStore) ServiceTierOverride(workspace string) string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if workspace == "" {
		return ps.state.ServiceTierOverride
	}
	return ps.state.WorkspaceServiceTierOverrides[workspace]
}

// SaveServiceTierOverride commits the preference before the live agent changes.
// Failed writes restore the previous in-memory state too.
func (ps *ProjectStateStore) SaveServiceTierOverride(workspace, tier string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if tier != "fast" && tier != "default" {
		return fmt.Errorf("project state: invalid service tier %q", tier)
	}
	old := ps.state
	overrides := make(map[string]string, len(old.WorkspaceServiceTierOverrides))
	for k, v := range old.WorkspaceServiceTierOverrides {
		overrides[k] = v
	}
	if workspace == "" {
		ps.state.ServiceTierOverride = tier
	} else {
		overrides[workspace] = tier
		ps.state.WorkspaceServiceTierOverrides = overrides
	}
	if err := ps.saveLocked(); err != nil {
		ps.state = old
		return err
	}
	return nil
}
