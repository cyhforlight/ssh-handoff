package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type executionMode string

const (
	modeExec     executionMode = "exec"
	modeShellPTY executionMode = "shell-pty"
)

type sessionState string

const (
	stateStarting    sessionState = "starting"
	stateInteractive sessionState = "interactive"
	stateManaged     sessionState = "managed"
)

type session struct {
	ID        string               `json:"id"`
	Name      string               `json:"name,omitempty"`
	Mode      executionMode        `json:"mode"`
	State     sessionState         `json:"state"`
	Command   string               `json:"command"`
	Platform  platformSessionState `json:"platform"`
	PID       int                  `json:"pid"`
	StartedAt time.Time            `json:"started_at"`
}

type sessionSummary struct {
	ID        string        `json:"id"`
	Name      string        `json:"name,omitempty"`
	Mode      executionMode `json:"mode"`
	State     sessionState  `json:"state"`
	StartedAt time.Time     `json:"started_at"`
}

func (value *session) summary() sessionSummary {
	return sessionSummary{
		ID:        value.ID,
		Name:      value.Name,
		Mode:      value.Mode,
		State:     value.State,
		StartedAt: value.StartedAt,
	}
}

type sessionRegistry struct {
	dir string
}

var (
	errSessionNotFound = errors.New("session not found")
	errNameInUse       = errors.New("session name is already in use")
)

func parseMode(value string) (executionMode, error) {
	mode := executionMode(value)
	if mode != modeExec && mode != modeShellPTY {
		return "", fmt.Errorf("invalid mode %q; expected exec or shell-pty", value)
	}
	return mode, nil
}

func openRegistry() (*sessionRegistry, error) {
	dir := runtimeDirectory()
	if err := ensurePrivateDirectory(dir); err != nil {
		return nil, err
	}
	return &sessionRegistry{dir: dir}, nil
}

func (registry *sessionRegistry) create(name string, mode executionMode, command string) (*session, error) {
	var created *session
	err := withFileLock(filepath.Join(registry.dir, ".registry.lock"), func() error {
		sessions, err := registry.loadAll()
		if err != nil {
			return err
		}
		for _, candidate := range sessions {
			if !processAlive(candidate.PID) {
				registry.removeFiles(candidate.ID)
				continue
			}
			if name != "" && candidate.Name == name {
				return errNameInUse
			}
		}

		id, err := randomID()
		if err != nil {
			return err
		}
		created = &session{
			ID:        id,
			Name:      name,
			Mode:      mode,
			State:     stateStarting,
			Command:   command,
			Platform:  newPlatformSessionState(registry.dir, id),
			PID:       os.Getpid(),
			StartedAt: time.Now().UTC(),
		}
		return registry.write(created)
	})
	return created, err
}

func (registry *sessionRegistry) update(session *session) error {
	return registry.write(session)
}

func (registry *sessionRegistry) resolve(reference string) (*session, error) {
	sessions, err := registry.loadAll()
	if err != nil {
		return nil, err
	}
	for _, candidate := range sessions {
		if candidate.ID != reference && candidate.Name != reference {
			continue
		}
		if !processAlive(candidate.PID) {
			registry.removeFiles(candidate.ID)
			return nil, fmt.Errorf("%w: %s", errSessionNotFound, reference)
		}
		return candidate, nil
	}
	return nil, fmt.Errorf("%w: %s", errSessionNotFound, reference)
}

func (registry *sessionRegistry) list() ([]*session, error) {
	sessions, err := registry.loadAll()
	if err != nil {
		return nil, err
	}
	live := sessions[:0]
	for _, candidate := range sessions {
		if processAlive(candidate.PID) {
			live = append(live, candidate)
		} else {
			registry.removeFiles(candidate.ID)
		}
	}
	slices.SortFunc(live, func(a, b *session) int {
		return a.StartedAt.Compare(b.StartedAt)
	})
	return live, nil
}

func (registry *sessionRegistry) withSessionLock(id string, action func() error) error {
	return withFileLock(filepath.Join(registry.dir, id+".lock"), action)
}

func (registry *sessionRegistry) remove(id string) {
	registry.removeFiles(id)
}

func (registry *sessionRegistry) removeFiles(id string) {
	_ = os.Remove(registry.statePath(id))
	removePlatformSessionFiles(registry.dir, id)
	_ = os.Remove(filepath.Join(registry.dir, id+".lock"))
}

func (registry *sessionRegistry) write(session *session) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := registry.statePath(session.ID)
	temporary := fmt.Sprintf("%s.tmp-%d", path, os.Getpid())
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func (registry *sessionRegistry) loadAll() ([]*session, error) {
	entries, err := os.ReadDir(registry.dir)
	if err != nil {
		return nil, err
	}
	sessions := make([]*session, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(registry.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var session session
		if err := json.Unmarshal(data, &session); err != nil {
			return nil, fmt.Errorf("read session %s: %w", entry.Name(), err)
		}
		sessions = append(sessions, &session)
	}
	return sessions, nil
}

func (registry *sessionRegistry) statePath(id string) string {
	return filepath.Join(registry.dir, id+".json")
}

func randomID() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
