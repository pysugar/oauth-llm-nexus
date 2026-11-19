// Package redact masks secrets and personal data in what the gateway sends to an upstream
// vendor and puts the values back in what the vendor answers, on its way to the client.
//
// A value is replaced by a placeholder {{KIND_xxxxxxxx}}, where xxxxxxxx is the lowercase
// base32 of HMAC-SHA256(key, kind+"\x00"+value) cut to 8 characters. The key lives in a 0600
// file next to the database, so the same value gets the same placeholder in every request and
// across restarts: a conversation's prefix stays byte-identical and prompt caches keep hitting.
// Placeholders are remembered in a bounded in-memory LRU (placeholder → value) for restoring.
//
// The design follows magpie's field-tested internal/redact (rule set, byte-preserving JSON walk,
// split-placeholder streaming restore), without its plugin runtime and local-model finders.
package redact

import (
	"container/list"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Kinds of values the rules find.
const (
	KindPrivateKey = "PRIVATE_KEY"
	KindAPIKey     = "API_KEY"
	KindToken      = "TOKEN"
	KindPassword   = "PASSWORD"
	KindSecret     = "SECRET"
	KindEmail      = "EMAIL"
	KindBankCard   = "BANK_CARD"
	KindIDCard     = "ID_CARD"
)

// Kinds lists every kind, in a stable order.
var Kinds = []string{KindPrivateKey, KindAPIKey, KindToken, KindPassword, KindSecret, KindEmail, KindBankCard, KindIDCard}

// IsKind reports whether k names one of Kinds.
func IsKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// Mode says what is done with a value of a kind.
type Mode string

const (
	ModeMask  Mode = "mask"  // replaced by its placeholder (default)
	ModeWarn  Mode = "warn"  // sent as written, counted and reported
	ModeBlock Mode = "block" // the request is refused, nothing is sent
	ModeOff   Mode = "off"   // the kind is not looked for
)

// ValidMode reports whether m is one of the modes.
func ValidMode(m Mode) bool {
	switch m {
	case ModeMask, ModeWarn, ModeBlock, ModeOff:
		return true
	}
	return false
}

// Policy is what the settings ask of one masking pass.
type Policy struct {
	Modes map[string]Mode // per kind; a missing kind is ModeMask
	// Allow lists values never masked: an exact value (case-insensitive), or "@domain" for
	// every email address at that domain or a subdomain of it.
	Allow []string
}

func (p Policy) mode(kind string) Mode {
	if m, ok := p.Modes[kind]; ok && ValidMode(m) {
		return m
	}
	return ModeMask
}

func (p Policy) allows(kind, v string) bool {
	for _, a := range p.Allow {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.EqualFold(a, v) {
			return true
		}
		if kind == KindEmail && strings.HasPrefix(a, "@") {
			at := strings.LastIndexByte(v, '@')
			host := strings.ToLower(v[at+1:])
			dom := strings.ToLower(a[1:])
			if host == dom || strings.HasSuffix(host, "."+dom) {
				return true
			}
		}
	}
	return false
}

// Hit is one value a pass found, told without the value.
type Hit struct {
	Kind        string `json:"kind"`
	Placeholder string `json:"placeholder"`
	Action      Mode   `json:"action"`
	Len         int    `json:"len"`
}

// Blocked reports whether hits hold a value whose kind is in block mode.
func Blocked(hits []Hit) bool {
	for _, h := range hits {
		if h.Action == ModeBlock {
			return true
		}
	}
	return false
}

// Masked counts the hits that were replaced by placeholders.
func Masked(hits []Hit) int {
	n := 0
	for _, h := range hits {
		if h.Action == ModeMask {
			n++
		}
	}
	return n
}

// DefaultCapacity bounds the placeholders remembered for restoring.
const DefaultCapacity = 100_000

type entry struct{ p, v string }

// KindStats counts what happened to values of one kind since start.
type KindStats struct {
	Masked  int64 `json:"masked"`
	Warned  int64 `json:"warned"`
	Blocked int64 `json:"blocked"`
}

type kindCounters struct{ masked, warned, blocked atomic.Int64 }

// Masker masks and restores values; it is safe for concurrent use.
type Masker struct {
	key []byte

	mu   sync.RWMutex
	cap  int
	ll   *list.List // front: most recently used
	byP  map[string]*list.Element
	kc   map[string]*kindCounters
	rest atomic.Int64
}

// New loads (or creates) the placeholder key at keyPath and returns a Masker remembering at most
// capacity placeholders. keyPath "" keeps a random key in memory only. A key file that exists but
// cannot be read, or is too short, is an error: masking with a fresh key would silently change
// every placeholder, so the caller fails closed instead.
func New(keyPath string, capacity int) (*Masker, error) {
	key, err := loadKey(keyPath)
	if err != nil {
		return nil, err
	}
	return NewWithKey(key, capacity), nil
}

// NewWithKey is New with an explicit key (tests).
func NewWithKey(key []byte, capacity int) *Masker {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	m := &Masker{
		key: append([]byte(nil), key...),
		cap: capacity,
		ll:  list.New(),
		byP: make(map[string]*list.Element),
		kc:  make(map[string]*kindCounters, len(Kinds)),
	}
	for _, k := range Kinds {
		m.kc[k] = &kindCounters{}
	}
	return m
}

func loadKey(path string) ([]byte, error) {
	if path == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		return b, nil
	}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(b) < 32 {
			return nil, fmt.Errorf("masking key %s is %d bytes, want 32", path, len(b))
		}
		_ = os.Chmod(path, 0o600)
		return b[:32], nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read masking key: %w", err)
	}
	b = make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create masking key dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return loadKey(path) // created concurrently
		}
		return nil, fmt.Errorf("create masking key: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return nil, fmt.Errorf("write masking key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write masking key: %w", err)
	}
	return b, nil
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Tag is the placeholder v of kind goes as, computed without remembering it.
func (m *Masker) Tag(kind, v string) string {
	h := hmac.New(sha256.New, m.key)
	h.Write([]byte(kind + "\x00" + v))
	return "{{" + kind + "_" + strings.ToLower(b32.EncodeToString(h.Sum(nil))[:8]) + "}}"
}

// put remembers v under its placeholder and returns the placeholder.
func (m *Masker) put(kind, v string) string {
	p := m.Tag(kind, v)
	m.mu.Lock()
	if el, ok := m.byP[p]; ok {
		m.ll.MoveToFront(el)
	} else {
		m.byP[p] = m.ll.PushFront(&entry{p: p, v: v})
		for m.ll.Len() > m.cap {
			old := m.ll.Back()
			m.ll.Remove(old)
			delete(m.byP, old.Value.(*entry).p)
		}
	}
	m.mu.Unlock()
	return p
}

// known reports whether v of kind has been masked before and is still remembered.
func (m *Masker) known(kind, v string) (string, bool) {
	p := m.Tag(kind, v)
	m.mu.RLock()
	_, ok := m.byP[p]
	m.mu.RUnlock()
	return p, ok
}

// Value is the value behind placeholder p, while it is remembered.
func (m *Masker) Value(p string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if el, ok := m.byP[p]; ok {
		return el.Value.(*entry).v, true
	}
	return "", false
}

// Size is how many placeholders are remembered.
func (m *Masker) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ll.Len()
}

func (m *Masker) holding() bool { return m.Size() > 0 }

// Stats is the per-kind counters and how many placeholders were put back in replies.
func (m *Masker) Stats() (map[string]KindStats, int64) {
	out := make(map[string]KindStats, len(m.kc))
	for k, c := range m.kc {
		out[k] = KindStats{Masked: c.masked.Load(), Warned: c.warned.Load(), Blocked: c.blocked.Load()}
	}
	return out, m.rest.Load()
}

func (m *Masker) count(hits []Hit) {
	for _, h := range hits {
		c := m.kc[h.Kind]
		if c == nil {
			continue
		}
		switch h.Action {
		case ModeMask:
			c.masked.Add(1)
		case ModeWarn:
			c.warned.Add(1)
		case ModeBlock:
			c.blocked.Add(1)
		}
	}
}
