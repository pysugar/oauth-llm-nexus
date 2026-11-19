package ir

import (
	"sync"
	"time"
)

const (
	defaultResponseStoreCap = 2048
	defaultResponseStoreTTL = 2 * time.Hour
)

type responseEntry struct {
	messages  []Message
	maker     string // gateway provider whose reply this is ("" when unknown)
	expiresAt time.Time
}

// ResponseStore bridges stateful OpenAI Responses API `previous_response_id` turns across
// non-OpenAI translated backends by caching bounded conversation histories in memory.
type ResponseStore struct {
	mu       sync.Mutex
	maxItems int
	ttl      time.Duration
	order    []string
	entries  map[string]*responseEntry
	nowFunc  func() time.Time
}

// NewResponseStore creates a bounded TTL ResponseStore. Zero/negative arguments use safe defaults.
func NewResponseStore(maxItems int, ttl time.Duration) *ResponseStore {
	if maxItems <= 0 {
		maxItems = defaultResponseStoreCap
	}
	if ttl <= 0 {
		ttl = defaultResponseStoreTTL
	}
	return &ResponseStore{
		maxItems: maxItems,
		ttl:      ttl,
		order:    make([]string, 0, maxItems),
		entries:  make(map[string]*responseEntry, maxItems),
		nowFunc:  time.Now,
	}
}

func (s *ResponseStore) now() time.Time {
	if s != nil && s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

// Save stores the completed conversation turn (prior messages + assistant output) under responseID.
func (s *ResponseStore) Save(responseID string, prior []Message, assistantParts []Part) {
	s.SaveBy(responseID, "", prior, assistantParts)
}

// SaveBy is Save recording which provider answered, so a stateful provider is only handed a
// previous_response_id it issued itself.
func (s *ResponseStore) SaveBy(responseID, maker string, prior []Message, assistantParts []Part) {
	if s == nil || responseID == "" {
		return
	}
	history := cloneMessages(prior)
	if len(assistantParts) > 0 {
		history = append(history, Message{
			Role:  RoleAssistant,
			Parts: cloneParts(assistantParts),
		})
	}
	if len(history) == 0 {
		return
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.entries[responseID]; !exists {
		if len(s.order) >= s.maxItems {
			oldest := s.order[0]
			copy(s.order, s.order[1:])
			s.order[len(s.order)-1] = responseID
			delete(s.entries, oldest)
		} else {
			s.order = append(s.order, responseID)
		}
	}
	s.entries[responseID] = &responseEntry{
		messages:  history,
		maker:     maker,
		expiresAt: now.Add(s.ttl),
	}
}

// Maker returns the provider recorded for responseID ("" when unknown or expired).
func (s *ResponseStore) Maker(responseID string) string {
	if s == nil || responseID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ent, ok := s.entries[responseID]; ok && ent != nil && s.now().Before(ent.expiresAt) {
		return ent.maker
	}
	return ""
}

// History returns the conversation a Responses request continues: the cached turns of
// previousID followed by the request's own new messages. ok is false when previousID is set but
// not cached.
func (s *ResponseStore) History(previousID string, fresh []Message) ([]Message, bool) {
	if previousID == "" {
		return fresh, true
	}
	prev, ok := s.Lookup(previousID)
	if !ok {
		return fresh, false
	}
	return append(prev, cloneMessages(fresh)...), true
}

// Lookup returns a deep copy of cached conversation history for responseID if present and unexpired.
func (s *ResponseStore) Lookup(responseID string) ([]Message, bool) {
	if s == nil || responseID == "" {
		return nil, false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	ent, ok := s.entries[responseID]
	if !ok || ent == nil {
		return nil, false
	}
	if !now.Before(ent.expiresAt) {
		delete(s.entries, responseID)
		return nil, false
	}
	return cloneMessages(ent.messages), true
}

// CloneWithMessages returns a shallow copy of req with a fresh Messages slice and PreviousResponseID cleared.
func (r *Request) CloneWithMessages(msgs []Message) *Request {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Messages = cloneMessages(msgs)
	cp.PreviousResponseID = ""
	return &cp
}

func cloneMessages(in []Message) []Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]Message, len(in))
	for i, m := range in {
		out[i] = Message{
			Role:  m.Role,
			Name:  m.Name,
			Parts: cloneParts(m.Parts),
		}
	}
	return out
}

func cloneParts(in []Part) []Part {
	if len(in) == 0 {
		return nil
	}
	out := make([]Part, len(in))
	for i, p := range in {
		cp := p
		if len(p.Data) > 0 {
			cp.Data = append([]byte(nil), p.Data...)
		}
		if len(p.ResultParts) > 0 {
			cp.ResultParts = cloneParts(p.ResultParts)
		}
		out[i] = cp
	}
	return out
}
