package slackdm

import (
	"strings"
	"sync"
)

type emailUserIDCache struct {
	mu             sync.RWMutex
	userIDsByEmail map[string]string
}

func newEmailUserIDCache() *emailUserIDCache {
	return &emailUserIDCache{
		userIDsByEmail: make(map[string]string),
	}
}

func (c *emailUserIDCache) Get(email string) (string, bool) {
	if c == nil {
		return "", false
	}
	normalizedEmail := normalizeEmail(email)
	if normalizedEmail == "" {
		return "", false
	}
	c.mu.RLock()
	userID, ok := c.userIDsByEmail[normalizedEmail]
	c.mu.RUnlock()
	userID = strings.TrimSpace(userID)
	if !ok || userID == "" {
		return "", false
	}
	return userID, true
}

func (c *emailUserIDCache) Put(email, userID string) {
	if c == nil {
		return
	}
	normalizedEmail := normalizeEmail(email)
	normalizedUserID := strings.TrimSpace(userID)
	if normalizedEmail == "" || normalizedUserID == "" {
		return
	}
	c.mu.Lock()
	if c.userIDsByEmail == nil {
		c.userIDsByEmail = make(map[string]string)
	}
	c.userIDsByEmail[normalizedEmail] = normalizedUserID
	c.mu.Unlock()
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
