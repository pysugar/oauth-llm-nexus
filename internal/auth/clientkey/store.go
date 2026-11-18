package clientkey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

// KeyPrefix marks client keys so they are distinguishable from the legacy "sk-<hex>" key.
const KeyPrefix = "sk-nx-"

// touchInterval throttles last_used_at writes so authenticated requests rarely hit SQLite.
const touchInterval = time.Minute

var (
	// ErrNotFound is returned when no client key matches the given id or name.
	ErrNotFound = errors.New("client key not found")
	// ErrDuplicateName is returned when creating a key whose name is already taken.
	ErrDuplicateName = errors.New("a client key with this name already exists")
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Spec describes a client key to create.
type Spec struct {
	Name             string
	AllowedProviders []string
	AllowedModels    []string
}

// Patch updates a client key: nil fields are left unchanged, empty slices clear an allowlist.
type Patch struct {
	AllowedProviders *[]string
	AllowedModels    *[]string
	Disabled         *bool
}

// Hash returns the hex SHA-256 digest under which a plaintext key is stored and looked up.
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Mask renders a secret for display, keeping a short prefix and the last four characters.
func Mask(key string) string {
	if len(key) <= 12 {
		return "***"
	}
	return key[:8] + "…" + key[len(key)-4:]
}

// Generate returns a new random plaintext client key ("sk-nx-" + 48 hex chars, 192 bits).
func Generate() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms (Go ≥1.24 aborts instead)
	return KeyPrefix + hex.EncodeToString(b)
}

// ValidateName checks that name is a usable, non-reserved client key name.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid key name %q: use 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit", name)
	}
	if strings.EqualFold(name, LegacyName) {
		return fmt.Errorf("key name %q is reserved for the legacy API key", name)
	}
	return nil
}

// NormalizeProviders lower-cases, trims and de-duplicates provider IDs, dropping empties.
func NormalizeProviders(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		if p = NormalizeProvider(p); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// NormalizeModels trims and de-duplicates model aliases, dropping empties.
func NormalizeModels(in []string) []string {
	out := make([]string, 0, len(in))
	for _, m := range in {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

func encodeList(list []string) string {
	if len(list) == 0 {
		return ""
	}
	b, _ := json.Marshal(list)
	return string(b)
}

func decodeList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// A corrupt allowlist must fail closed rather than silently granting full access.
		return []string{"\x00invalid"}
	}
	return out
}

// AllowedProvidersOf decodes the provider allowlist stored on row.
func AllowedProvidersOf(row models.ClientKey) []string { return decodeList(row.AllowedProviders) }

// AllowedModelsOf decodes the model allowlist stored on row.
func AllowedModelsOf(row models.ClientKey) []string { return decodeList(row.AllowedModels) }

// ToCaller converts a stored key into the request-scoped caller identity.
func ToCaller(row models.ClientKey) *Caller {
	return &Caller{
		ID:               row.ID,
		Name:             row.Name,
		AllowedProviders: AllowedProvidersOf(row),
		AllowedModels:    AllowedModelsOf(row),
	}
}

// Create stores a new client key and returns it with the plaintext secret, which is not
// recoverable afterwards.
func Create(gdb *gorm.DB, spec Spec) (*models.ClientKey, string, error) {
	name := strings.TrimSpace(spec.Name)
	if err := ValidateName(name); err != nil {
		return nil, "", err
	}
	var count int64
	if err := gdb.Model(&models.ClientKey{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return nil, "", err
	}
	if count > 0 {
		return nil, "", ErrDuplicateName
	}
	plaintext := Generate()
	row := models.ClientKey{
		Name:             name,
		KeyHash:          Hash(plaintext),
		KeyHint:          Mask(plaintext),
		AllowedProviders: encodeList(NormalizeProviders(spec.AllowedProviders)),
		AllowedModels:    encodeList(NormalizeModels(spec.AllowedModels)),
	}
	if err := gdb.Create(&row).Error; err != nil {
		return nil, "", err
	}
	Invalidate(gdb)
	return &row, plaintext, nil
}

// List returns all client keys ordered by creation.
func List(gdb *gorm.DB) ([]models.ClientKey, error) {
	var rows []models.ClientKey
	err := gdb.Order("id asc").Find(&rows).Error
	return rows, err
}

// Get finds a client key by numeric id or by name.
func Get(gdb *gorm.DB, ref string) (*models.ClientKey, error) {
	ref = strings.TrimSpace(ref)
	var row models.ClientKey
	q := gdb.Where("name = ?", ref)
	if id, err := strconv.ParseUint(ref, 10, 64); err == nil {
		q = gdb.Where("id = ?", id)
	}
	if err := q.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// Update applies p to the key with id and returns the updated row.
func Update(gdb *gorm.DB, id uint, p Patch) (*models.ClientKey, error) {
	if _, err := Get(gdb, strconv.FormatUint(uint64(id), 10)); err != nil {
		return nil, err
	}
	updates := map[string]any{}
	if p.AllowedProviders != nil {
		updates["allowed_providers"] = encodeList(NormalizeProviders(*p.AllowedProviders))
	}
	if p.AllowedModels != nil {
		updates["allowed_models"] = encodeList(NormalizeModels(*p.AllowedModels))
	}
	if p.Disabled != nil {
		updates["disabled"] = *p.Disabled
	}
	if len(updates) > 0 {
		if err := gdb.Model(&models.ClientKey{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return nil, err
		}
		Invalidate(gdb)
	}
	return Get(gdb, strconv.FormatUint(uint64(id), 10))
}

// Delete removes the key with id; requests presenting it are rejected immediately.
func Delete(gdb *gorm.DB, id uint) error {
	res := gdb.Delete(&models.ClientKey{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	Invalidate(gdb)
	touchMu.Lock()
	delete(touchedAt, touchKey{gdb, id})
	touchMu.Unlock()
	return nil
}

// ===== in-process lookup cache =====

type keyEntry struct {
	caller   *Caller
	disabled bool
}

type keySet struct {
	byHash map[string]keyEntry
}

var (
	setsMu sync.Mutex // serializes cache fills with invalidation so a fill never resurrects stale rows
	sets   sync.Map   // map[*gorm.DB]*keySet
)

// Invalidate drops the cached key set for gdb after a mutation.
func Invalidate(gdb *gorm.DB) {
	setsMu.Lock()
	sets.Delete(gdb)
	setsMu.Unlock()
}

func load(gdb *gorm.DB) *keySet {
	if gdb == nil {
		return &keySet{}
	}
	if v, ok := sets.Load(gdb); ok {
		return v.(*keySet)
	}
	setsMu.Lock()
	defer setsMu.Unlock()
	if v, ok := sets.Load(gdb); ok {
		return v.(*keySet)
	}
	set := &keySet{byHash: map[string]keyEntry{}}
	if gdb.Migrator().HasTable(&models.ClientKey{}) {
		var rows []models.ClientKey
		if err := gdb.Find(&rows).Error; err != nil {
			return set // do not cache a failed read
		}
		for _, row := range rows {
			set.byHash[row.KeyHash] = keyEntry{caller: ToCaller(row), disabled: row.Disabled}
		}
	}
	sets.Store(gdb, set)
	return set
}

// HasKeys reports whether any client key (enabled or not) exists, which turns on authentication
// even when no legacy key is configured.
func HasKeys(gdb *gorm.DB) bool {
	return len(load(gdb).byHash) > 0
}

// Resolve looks up a presented plaintext key. ok is false when no client key matches; a matching
// but disabled key returns ok=true, disabled=true.
func Resolve(gdb *gorm.DB, presented string) (caller *Caller, disabled bool, ok bool) {
	if presented == "" {
		return nil, false, false
	}
	e, found := load(gdb).byHash[Hash(presented)]
	if !found {
		return nil, false, false
	}
	return e.caller, e.disabled, true
}

type touchKey struct {
	db *gorm.DB
	id uint
}

var (
	touchMu   sync.Mutex
	touchedAt = map[touchKey]time.Time{}
)

// Touch records that key id authenticated a request, persisting last_used_at at most once per
// minute per key.
func Touch(gdb *gorm.DB, id uint) {
	if gdb == nil || id == 0 {
		return
	}
	now := time.Now().UTC()
	k := touchKey{gdb, id}
	touchMu.Lock()
	if last, ok := touchedAt[k]; ok && now.Sub(last) < touchInterval {
		touchMu.Unlock()
		return
	}
	touchedAt[k] = now
	touchMu.Unlock()
	gdb.Model(&models.ClientKey{}).Where("id = ?", id).UpdateColumn("last_used_at", now)
}
