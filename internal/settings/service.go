package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/xlsft/openclick/internal/store"
)

const kvKey = "app"

// upgrades[n] migrates settings from version n to n+1 in place.
var upgrades = map[int]func(*Settings){}

// Service is the live settings holder. Safe for concurrent use.
type Service struct {
	kv       *store.KV
	current  int
	upgrades map[int]func(*Settings)

	mu   sync.RWMutex
	cur  Settings
	subs map[int]chan Settings
	next int
}

// NewService loads settings from kv, writing defaults on first run and
// running upgrades when the stored version is older.
func NewService(ctx context.Context, kv *store.KV) (*Service, error) {
	return newService(ctx, kv, CurrentVersion, upgrades)
}

func newService(ctx context.Context, kv *store.KV, current int, ups map[int]func(*Settings)) (*Service, error) {
	s := &Service{kv: kv, current: current, upgrades: ups, subs: map[int]chan Settings{}}
	raw, err := kv.Get(ctx, kvKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.cur = Defaults()
		s.cur.Version = current
		if err := s.persist(ctx, s.cur); err != nil {
			return nil, err
		}
		return s, nil
	case err != nil:
		return nil, err
	}
	// Decode over defaults so fields added in later versions get their default.
	loaded := Defaults()
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return nil, fmt.Errorf("settings: decode stored: %w", err)
	}
	upgraded, err := s.upgrade(loaded)
	if err != nil {
		return nil, err
	}
	if err := upgraded.Validate(); err != nil {
		return nil, err
	}
	if upgraded.Version != loaded.Version {
		if err := s.persist(ctx, upgraded); err != nil {
			return nil, err
		}
	}
	s.cur = upgraded
	return s, nil
}

func (s *Service) upgrade(v Settings) (Settings, error) {
	if v.Version > s.current {
		return v, fmt.Errorf("settings: stored version %d is newer than supported %d", v.Version, s.current)
	}
	for v.Version < s.current {
		if fn := s.upgrades[v.Version]; fn != nil {
			fn(&v)
		}
		v.Version++
	}
	return v, nil
}

func (s *Service) persist(ctx context.Context, v Settings) error {
	return store.SetJSON(ctx, s.kv, kvKey, v)
}

// Get returns a snapshot of the current settings.
func (s *Service) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Update applies fn to a copy, validates, persists and notifies subscribers.
// On any error the current settings stay untouched.
func (s *Service) Update(ctx context.Context, fn func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cur
	fn(&next)
	next.Version = s.current
	return s.commitLocked(ctx, next)
}

func (s *Service) commitLocked(ctx context.Context, next Settings) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if err := s.persist(ctx, next); err != nil {
		return err
	}
	s.cur = next
	for _, ch := range s.subs {
		// Subscribers only care about the latest value: replace a stale
		// unread one instead of blocking the writer.
		select {
		case ch <- next:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- next:
			default:
			}
		}
	}
	return nil
}

// Subscribe returns a channel receiving the new settings after every
// change (latest value wins if the reader is slow). cancel closes it.
func (s *Service) Subscribe() (<-chan Settings, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.next
	s.next++
	ch := make(chan Settings, 1)
	s.subs[id] = ch
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.subs, id)
			close(ch)
		})
	}
}

// Export writes the settings as indented JSON with all secret fields removed.
func (s *Service) Export(w io.Writer) error {
	snap := s.Get()
	stripSecrets(&snap)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(snap)
}

// Import replaces settings with the JSON read from r. Groups missing from
// the input keep their current values; secret fields in the input are
// ignored and the local ones are kept.
func (s *Service) Import(ctx context.Context, r io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cur
	if err := json.NewDecoder(r).Decode(&next); err != nil {
		return fmt.Errorf("settings: import decode: %w", err)
	}
	restoreSecrets(&next, &s.cur)
	next, err := s.upgrade(next)
	if err != nil {
		return err
	}
	return s.commitLocked(ctx, next)
}
