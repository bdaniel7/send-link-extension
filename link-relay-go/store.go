package main

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// Link mirrors the shape used by the Node relay so the extension doesn't
// need to change: seenBy tracks which devices have already received it,
// rather than deleting on read, so more than two devices could share one
// relay later without redesigning anything.
type Link struct {
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	Title     string   `json:"title"`
	From      string   `json:"from"`
	CreatedAt int64    `json:"createdAt"`
	SeenBy    []string `json:"seenBy"`
}

// Store persists links to a JSON file. All access goes through Update,
// which holds the lock for the full load-mutate-save cycle — net/http
// serves requests concurrently (unlike Node's single-threaded event loop),
// so without this a POST and a GET arriving at the same instant could
// race and silently drop one of them.
type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Update(fn func([]Link) []Link) ([]Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	links, err := s.load()
	if err != nil {
		return nil, err
	}
	links = fn(links)
	if err := s.save(links); err != nil {
		return nil, err
	}
	return links, nil
}

func (s *Store) load() ([]Link, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Link{}, nil
	}
	if err != nil {
		return nil, err
	}
	var links []Link
	if err := json.Unmarshal(data, &links); err != nil {
		// Tolerate an empty or corrupt file rather than refusing to start.
		return []Link{}, nil
	}
	return links, nil
}

func (s *Store) save(links []Link) error {
	data, err := json.MarshalIndent(links, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}
