package relay

import "sync"

// Loader is a concurrency-safe in-memory store of instance secrets.
// Implements the InstanceLoader interface.
type Loader struct {
	mu      sync.RWMutex
	secrets map[string][]byte
}

// NewInstanceLoader creates a Loader initialised with secrets from the instance list.
func NewInstanceLoader(specs []InstanceSpec) *Loader {
	m := make(map[string][]byte, len(specs))
	for _, s := range specs {
		if s.Secret != "" {
			m[s.ID] = []byte(s.Secret)
		}
	}
	return &Loader{secrets: m}
}

// Secret returns the secret for an instance by its ID. Returns nil if the instance is unknown.
func (l *Loader) Secret(id string) []byte {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.secrets[id]
}

// Swap atomically replaces the secrets map. Used on SIGHUP for hot-reload.
func (l *Loader) Swap(secrets map[string][]byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.secrets = secrets
}

// InstanceIDs returns a list of all known instance IDs.
func (l *Loader) InstanceIDs() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	ids := make([]string, 0, len(l.secrets))
	for id := range l.secrets {
		ids = append(ids, id)
	}
	return ids
}
