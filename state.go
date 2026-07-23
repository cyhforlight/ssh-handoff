package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

type sessionInfo struct {
	ID      string        `json:"id"`
	Command string        `json:"connection_command"`
	Note    string        `json:"note,omitempty"`
	Mode    executionMode `json:"mode"`
	State   sessionState  `json:"state"`
}

type session struct {
	sessionInfo
	Platform platformSessionState `json:"platform"`
	PID      int                  `json:"pid"`
}

type sessionRegistry struct {
	dir string
}

var errSessionNotFound = errors.New("session not found")

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

func (registry *sessionRegistry) create(note string, mode executionMode, command string) (*session, error) {
	var created *session
	err := withFileLock(filepath.Join(registry.dir, ".registry.lock"), func() error {
		sessions, err := registry.list()
		if err != nil {
			return err
		}

		id := newSessionID(sessions)
		created = &session{
			sessionInfo: sessionInfo{
				ID:      id,
				Command: command,
				Note:    note,
				Mode:    mode,
				State:   stateStarting,
			},
			Platform: newPlatformSessionState(registry.dir, id),
			PID:      os.Getpid(),
		}
		return registry.write(created)
	})
	return created, err
}

func (registry *sessionRegistry) update(session *session) error {
	return registry.write(session)
}

func (registry *sessionRegistry) resolve(id string) (*session, error) {
	id = strings.ToUpper(id)
	sessions, err := registry.list()
	if err != nil {
		return nil, err
	}
	for _, candidate := range sessions {
		if candidate.ID == id {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", errSessionNotFound, id)
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

func (registry *sessionRegistry) list() ([]*session, error) {
	sessions, err := registry.loadAll()
	if err != nil {
		return nil, err
	}
	live := sessions[:0]
	for _, session := range sessions {
		if processAlive(session.PID) {
			live = append(live, session)
		} else {
			registry.removeFiles(session.ID)
		}
	}
	return live, nil
}

func (registry *sessionRegistry) statePath(id string) string {
	return filepath.Join(registry.dir, id+".json")
}

func newSessionID(sessions []*session) string {
	const alphabet = "ABCDEFGHJKMNPRSTUVWXYZ2345689"

	for {
		var candidate [4]byte
		for index := range candidate {
			candidate[index] = alphabet[rand.N(len(alphabet))]
		}
		id := string(candidate[:])
		if !slices.ContainsFunc(sessions, func(session *session) bool { return session.ID == id }) {
			return id
		}
	}
}
