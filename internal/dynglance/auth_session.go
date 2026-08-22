package dynglance

import (
	"context"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const OIDC_SESSION_COOKIE_NAME = "oidc_session"
const OIDC_SESSION_VALID_PERIOD = 14 * 24 * time.Hour

type oidcSession struct {
	Username  string
	Groups    []string
	Token     *oauth2.Token
	CreatedAt time.Time
}

type sessionStore struct {
	sessions sync.Map // map[sessionID]*oidcSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{}
}

func (s *sessionStore) set(id string, session *oidcSession) {
	s.sessions.Store(id, session)
}

func (s *sessionStore) get(id string) (*oidcSession, bool) {
	v, ok := s.sessions.Load(id)
	if !ok {
		return nil, false
	}
	sess, ok := v.(*oidcSession)
	return sess, ok
}

func (s *sessionStore) delete(id string) {
	s.sessions.Delete(id)
}

func (s *sessionStore) sweepExpired(maxAge time.Duration) {
	now := time.Now()
	s.sessions.Range(func(k, v any) bool {
		sess, ok := v.(*oidcSession)
		if !ok || now.Sub(sess.CreatedAt) > maxAge {
			s.sessions.Delete(k)
		}
		return true
	})
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
