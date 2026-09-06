package dynacat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const OIDC_SESSION_COOKIE_NAME = "oidc_session"
const OIDC_SESSION_VALID_PERIOD = 30 * 24 * time.Hour

type oidcSession struct {
	Username  string        `json:"username"`
	Groups    []string      `json:"groups"`
	Token     *oauth2.Token `json:"token"`
	CreatedAt time.Time     `json:"created_at"`
}

type sessionStore struct {
	path     string
	mu       sync.RWMutex
	sessions map[string]*oidcSession
}

func newSessionStore(path string) *sessionStore {
	store := &sessionStore{
		path:     path,
		sessions: make(map[string]*oidcSession),
	}
	store.load()
	return store
}

func (s *sessionStore) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var loaded map[string]*oidcSession
	if err := json.Unmarshal(data, &loaded); err != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range loaded {
		if now.Sub(v.CreatedAt) <= OIDC_SESSION_VALID_PERIOD {
			s.sessions[k] = v
		}
	}
}

func (s *sessionStore) persist() {
	if s.path == "" {
		return
	}
	s.mu.RLock()
	data, err := json.Marshal(s.sessions)
	s.mu.RUnlock()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0755)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func (s *sessionStore) set(id string, session *oidcSession) {
	s.mu.Lock()
	s.sessions[id] = session
	s.mu.Unlock()
	s.persist()
}

func (s *sessionStore) get(id string) (*oidcSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	s.persist()
}

func (s *sessionStore) sweepExpired(maxAge time.Duration) {
	now := time.Now()
	changed := false
	s.mu.Lock()
	for k, v := range s.sessions {
		if now.Sub(v.CreatedAt) > maxAge {
			delete(s.sessions, k)
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.persist()
	}
}

func (s *sessionStore) runSweeper(ctx context.Context, interval, maxAge time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepExpired(maxAge)
		}
	}
}
