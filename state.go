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

const sessionIDAlphabet = "ABCDEFGHJKMNPRSTUVWXYZ2345689"

type session struct {
	ID         string               `json:"id"`
	Connection connectionSpec       `json:"connection"`
	Note       string               `json:"note,omitzero"`
	Mode       executionMode        `json:"mode"`
	Platform   platformSessionState `json:"platform"`
	PID        int                  `json:"pid"`
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

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{dir: runtimeDirectory()}
}

func (registry *sessionRegistry) create(
	note string,
	mode executionMode,
	connection connectionSpec,
) (*session, error) {
	if err := connection.validate(); err != nil {
		return nil, err
	}
	if err := validatePlatformConnection(connection); err != nil {
		return nil, err
	}
	if err := ensurePrivateDirectory(registry.dir); err != nil {
		return nil, err
	}
	var created *session
	err := withRegistryLock(registry.dir, func() error {
		sessions, err := registry.loadAll()
		if err != nil {
			return err
		}
		sessions = slices.DeleteFunc(sessions, func(session *session) bool {
			if processAlive(session.PID) {
				return false
			}
			registry.removeFiles(session.ID)
			return true
		})

		id := newSessionID(sessions)
		created = &session{
			ID:         id,
			Connection: connection,
			Note:       note,
			Mode:       mode,
			Platform:   newPlatformSessionState(registry.dir, id),
			PID:        os.Getpid(),
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
	notFound := fmt.Errorf("%w: %s", errSessionNotFound, id)
	if !validSessionID(id) {
		return nil, notFound
	}
	if err := validatePrivateDirectory(registry.dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, notFound
		}
		return nil, err
	}
	session, err := registry.load(id)
	if err != nil {
		return nil, err
	}
	if session == nil || !processAlive(session.PID) {
		return nil, notFound
	}
	return session, nil
}

func (registry *sessionRegistry) removeFiles(id string) {
	_ = os.Remove(registry.statePath(id))
	removePlatformSessionFiles(registry.dir, id)
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
	if err := validatePrivateDirectory(registry.dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(registry.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	sessions := make([]*session, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validSessionID(id) {
			continue
		}
		session, err := registry.load(id)
		if err != nil {
			return nil, err
		}
		if session != nil {
			sessions = append(sessions, session)
		}
	}
	return sessions, nil
}

func (registry *sessionRegistry) load(id string) (*session, error) {
	data, err := os.ReadFile(registry.statePath(id))
	if errors.Is(err, os.ErrNotExist) {
		// The owner may have exited after the directory was read.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var session session
	if err := json.Unmarshal(data, &session); err != nil || session.ID != id {
		return nil, nil
	}
	if err := session.Connection.validate(); err != nil {
		return nil, nil
	}
	if err := validatePlatformConnection(session.Connection); err != nil {
		return nil, nil
	}
	return &session, nil
}

func (registry *sessionRegistry) list() ([]*session, error) {
	sessions, err := registry.loadAll()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(sessions, func(session *session) bool {
		return !processAlive(session.PID)
	}), nil
}

func (registry *sessionRegistry) statePath(id string) string {
	return filepath.Join(registry.dir, id+".json")
}

func validSessionID(id string) bool {
	return len(id) == 4 && strings.Trim(id, sessionIDAlphabet) == ""
}

func newSessionID(sessions []*session) string {
	for {
		var candidate [4]byte
		for index := range candidate {
			candidate[index] = sessionIDAlphabet[rand.N(len(sessionIDAlphabet))]
		}
		id := string(candidate[:])
		if !slices.ContainsFunc(sessions, func(session *session) bool { return session.ID == id }) {
			return id
		}
	}
}
