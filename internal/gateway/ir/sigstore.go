package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// SigStore maintains a bounded concurrent cache mapping call IDs and thought digests
// to thought signatures required across multi-turn Gemini 2.5/3 and Anthropic tool loops.
type SigStore struct {
	mu       sync.RWMutex
	maxItems int
	order    []string
	entries  map[string]string
}

// DefaultSigStore is the process-wide signature store used by gateway codecs.
var DefaultSigStore = NewSigStore(4096)

// NewSigStore creates a bounded signature cache holding up to maxItems entries.
func NewSigStore(maxItems int) *SigStore {
	if maxItems <= 0 {
		maxItems = 1024
	}
	return &SigStore{
		maxItems: maxItems,
		order:    make([]string, 0, maxItems),
		entries:  make(map[string]string, maxItems),
	}
}

// SaveCall associates a thought signature with a tool call ID.
func (s *SigStore) SaveCall(callID, sig string) {
	if s == nil || callID == "" || sig == "" {
		return
	}
	s.set("call:"+canonicalCallID(callID), sig)
}

// LookupCall retrieves a previously saved thought signature for callID (or its sanitized/signed variant).
func (s *SigStore) LookupCall(callID string) string {
	if s == nil || callID == "" {
		return ""
	}
	if _, embedded := DecodeGeminiCallSig(callID); embedded != "" {
		return embedded
	}
	if sig := s.get("call:" + canonicalCallID(callID)); sig != "" {
		return sig
	}
	return s.get("call:" + callID)
}

func canonicalCallID(callID string) string {
	base, _ := DecodeGeminiCallSig(callID)
	if base != "" {
		callID = base
	}
	return UnsanitizeToolID(callID)
}

// SaveThought associates a thinking signature with its thinking text digest.
func (s *SigStore) SaveThought(text, sig string) {
	if s == nil || text == "" || sig == "" {
		return
	}
	s.set("thought:"+digestText(text), sig)
}

// LookupThought retrieves a thinking signature by matching the thinking text digest.
func (s *SigStore) LookupThought(text string) string {
	if s == nil || text == "" {
		return ""
	}
	return s.get("thought:" + digestText(text))
}

func (s *SigStore) set(key, val string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.entries[key]; !exists {
		if len(s.order) >= s.maxItems {
			oldest := s.order[0]
			copy(s.order, s.order[1:])
			s.order[len(s.order)-1] = key
			delete(s.entries, oldest)
		} else {
			s.order = append(s.order, key)
		}
	}
	s.entries[key] = val
}

func (s *SigStore) get(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries[key]
}

func digestText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:16])
}

// ThinkingBlock is one signed (or redacted) Claude thinking block of a reply.
type ThinkingBlock struct {
	Text      string `json:"t,omitempty"`
	Signature string `json:"s,omitempty"`
	Redacted  bool   `json:"r,omitempty"`
}

// SaveCallThinking remembers a Claude reply's thinking blocks under each tool call id it made, so
// they can be put back in front of that tool turn when a client that drops or rewrites thinking
// (OpenAI Chat, Gemini) sends the history again. Claude rejects tool loops whose assistant turn
// lost its signed thinking.
func (s *SigStore) SaveCallThinking(callIDs []string, blocks []ThinkingBlock) {
	if s == nil || len(callIDs) == 0 || len(blocks) == 0 {
		return
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		return
	}
	for _, id := range callIDs {
		if id != "" {
			s.set("think:"+canonicalCallID(id), string(raw))
		}
	}
}

// LookupCallThinking returns the thinking blocks saved for callID.
func (s *SigStore) LookupCallThinking(callID string) []ThinkingBlock {
	if s == nil || callID == "" {
		return nil
	}
	raw := s.get("think:" + canonicalCallID(callID))
	if raw == "" {
		return nil
	}
	var blocks []ThinkingBlock
	if json.Unmarshal([]byte(raw), &blocks) != nil {
		return nil
	}
	return blocks
}
