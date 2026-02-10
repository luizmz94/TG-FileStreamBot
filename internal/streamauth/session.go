package streamauth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

type Session struct {
	UserID    string
	Email     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type sessionStore struct {
	log             *zap.Logger
	ttl             time.Duration
	cleanupInterval time.Duration

	mu       sync.RWMutex
	sessions map[string]Session
	stopCh   chan struct{}
}

func newSessionStore(log *zap.Logger, ttl time.Duration, cleanupInterval time.Duration) *sessionStore {
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	if cleanupInterval <= 0 {
		cleanupInterval = time.Minute
	}

	s := &sessionStore{
		log:             log.Named("SessionStore"),
		ttl:             ttl,
		cleanupInterval: cleanupInterval,
		sessions:        make(map[string]Session),
		stopCh:          make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

func (s *sessionStore) Create(userID string, email string) (string, time.Time, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	now := time.Now()
	expiresAt := now.Add(s.ttl)
	session := Session{
		UserID:    userID,
		Email:     email,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}

	s.mu.Lock()
	s.sessions[token] = session
	s.mu.Unlock()

	return token, expiresAt, nil
}

func (s *sessionStore) Validate(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}

	s.mu.RLock()
	session, ok := s.sessions[token]
	s.mu.RUnlock()
	if !ok {
		return Session{}, false
	}

	if time.Now().After(session.ExpiresAt) {
		s.mu.Lock()
		delete(s.sessions, token)
		s.mu.Unlock()
		return Session{}, false
	}

	return session, true
}

func (s *sessionStore) cleanupLoop() {
	ticker := time.NewTicker(s.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanupExpired()
		case <-s.stopCh:
			return
		}
	}
}

func (s *sessionStore) cleanupExpired() {
	now := time.Now()
	removed := 0

	s.mu.Lock()
	for token, session := range s.sessions {
		if now.After(session.ExpiresAt) {
			delete(s.sessions, token)
			removed++
		}
	}
	remaining := len(s.sessions)
	s.mu.Unlock()

	if removed > 0 {
		s.log.Debug("Expired sessions removed",
			zap.Int("removed", removed),
			zap.Int("remaining", remaining))
	}
}
