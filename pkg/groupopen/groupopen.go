// Package groupopen keeps the set of chats whose silent dispatch rule ("agent": "-" with open_agent) is currently
// opened. The set is shared by the router and the group_open tool and persisted in a small JSON file.
package groupopen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// FileName lives in the main workspace folder.
const FileName = "group_open.json"

// Store is a goroutine-safe set of opened chat keys, written atomically on every change.
type Store struct {
	path string
	mu   sync.RWMutex
	open map[string]bool
}

var (
	storesMu sync.Mutex
	stores   = map[string]*Store{}
)

// For returns the shared store for a workspace folder (loaded on first use). An empty workspace gives an in-memory
// store that is never persisted.
func For(workspace string) *Store {
	path := ""
	if workspace != "" {
		path = filepath.Join(workspace, FileName)
	}
	storesMu.Lock()
	defer storesMu.Unlock()
	if s, ok := stores[path]; ok && path != "" {
		return s
	}
	s := &Store{path: path, open: map[string]bool{}}
	s.load()
	if path != "" {
		stores[path] = s
	}
	return s
}

func (s *Store) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // missing or unreadable = all closed
	}
	var keys []string
	if json.Unmarshal(data, &keys) != nil {
		return
	}
	for _, k := range keys {
		s.open[k] = true
	}
}

// IsOpen reports whether the chat key is opened.
func (s *Store) IsOpen(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.open[key]
}

// Set opens or closes the chat and persists the result; on a write error the in-memory state is rolled back.
func (s *Store) Set(key string, open bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.open[key]
	if open {
		s.open[key] = true
	} else {
		delete(s.open, key)
	}
	if err := s.save(); err != nil {
		if prev {
			s.open[key] = true
		} else {
			delete(s.open, key)
		}
		return err
	}
	return nil
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	keys := make([]string, 0, len(s.open))
	for k := range s.open {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	data, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
