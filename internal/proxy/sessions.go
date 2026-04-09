package proxy

import (
	"context"
	"sync"
	"time"
)

type sessionKey struct {
	sessionID string
	modelID   string
}

type SessionEntry struct {
	CredentialName string
	LastAccess     time.Time
}

type SessionStore struct {
	mu      sync.RWMutex
	entries map[sessionKey]*SessionEntry
	ttl     time.Duration
}

func NewSessionStore(ttl time.Duration) *SessionStore {
	return &SessionStore{
		entries: make(map[sessionKey]*SessionEntry),
		ttl:     ttl,
	}
}

func (s *SessionStore) Get(sessionID, modelID string) (string, bool) {
	key := sessionKey{sessionID: sessionID, modelID: modelID}

	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}

	if time.Since(entry.LastAccess) <= s.ttl {
		return entry.CredentialName, true
	}

	s.mu.Lock()
	entry, ok = s.entries[key]
	if ok && time.Since(entry.LastAccess) > s.ttl {
		delete(s.entries, key)
	}
	s.mu.Unlock()
	return "", false
}

func (s *SessionStore) Set(sessionID, modelID, credentialName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[sessionKey{sessionID: sessionID, modelID: modelID}] = &SessionEntry{
		CredentialName: credentialName,
		LastAccess:     time.Now(),
	}
}

func (s *SessionStore) Delete(sessionID, modelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, sessionKey{sessionID: sessionID, modelID: modelID})
}

func (s *SessionStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(time.Now())
	return len(s.entries)
}

func (s *SessionStore) StartCleanup(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.mu.Lock()
			s.cleanupExpiredLocked(now)
			s.mu.Unlock()
		}
	}
}

func (s *SessionStore) cleanupExpiredLocked(now time.Time) {
	for key, entry := range s.entries {
		if now.Sub(entry.LastAccess) > s.ttl {
			delete(s.entries, key)
		}
	}
}
