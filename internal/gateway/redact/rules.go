package redact

import (
	"regexp"
	"sort"
	"strings"
)

const (
	alnum   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	tokenCh = alnum + "_-"
	digits  = "0123456789"
	// quote is a field's or a value's quote, if it has one: " or ', or \" in JSON text.
	quote = `(?:\\?["'])?`
	// secretCh are the characters of a secret's value.
	secretCh = `[A-Za-z0-9_\-./+=~!@#%^&*]`
	// secretNames are what a field holding a secret has in its name.
	secretNames = `password|passwd|passphrase|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|priv[_-]?key|credential`
)

// rule finds values of one kind. A rule whose regexp has a capture group masks the group only.
type rule struct {
	kind    string
	re      *regexp.Regexp
	markers []string // a cheap pre-check: one of them must occur (in lower case when fold)
	fold    bool
	bound   string                        // characters that may not touch the value on either side
	ok      func(v string) bool           // the value itself is one
	at      func(s string, a, b int) bool // the value where it stands is one
}

var rules = []rule{
	{kind: KindPrivateKey, re: regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----[\s\S]+?-----END (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`), markers: []string{"PRIVATE KEY"}},
	{kind: KindAPIKey, re: regexp.MustCompile(`sk-(?:ant-|proj-|or-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`), markers: []string{"sk-"}, bound: tokenCh, ok: notASlug},
	{kind: KindAPIKey, re: regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,}|glpat-[A-Za-z0-9_-]{20,})`), markers: []string{"gh", "github_pat_", "glpat-"}, bound: tokenCh},
	{kind: KindAPIKey, re: regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`), markers: []string{"AIza"}, bound: tokenCh},
	{kind: KindAPIKey, re: regexp.MustCompile(`xox[abposr]-[0-9A-Za-z-]{10,}`), markers: []string{"xox"}, bound: tokenCh},
	{kind: KindAPIKey, re: regexp.MustCompile(`(?:sk|rk)_live_[0-9A-Za-z]{20,}`), markers: []string{"_live_"}, bound: tokenCh},
	{kind: KindAPIKey, re: regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`), markers: []string{"AKIA", "ASIA"}, bound: alnum},
	{kind: KindAPIKey, re: regexp.MustCompile(`(?:hf_[A-Za-z0-9]{30,}|gsk_[A-Za-z0-9]{40,}|xai-[A-Za-z0-9]{40,})`), markers: []string{"hf_", "gsk_", "xai-"}, bound: tokenCh},
	{kind: KindToken, re: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), markers: []string{"eyJ"}, bound: tokenCh},
	// the password of scheme://user:password@host (or :password@host, as a Redis URL has it)
	{kind: KindPassword, re: regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s:@/?#"'<>]*:([^\s@/?#"'<>]{3,})@`), markers: []string{"://"}, ok: notAVariable, at: notUserName},
	// an Authorization or Cookie header's value, whatever it looks like
	{kind: KindSecret, re: regexp.MustCompile(`(?im)^[ \t]*(?:proxy-)?(?:authorization|cookie|x-api-key)[ \t]*:[ \t]*(\S[^\r\n]{7,})`), markers: []string{"authorization", "cookie", "x-api-key"}, fold: true, ok: headerValue},
	// … and one written inline, as curl -H "Authorization: Bearer …" has it
	{kind: KindSecret, re: regexp.MustCompile(`(?i)(?:proxy-)?authorization["']?[ \t]*:[ \t]*["']?((?:bearer|basic|token|digest)[ \t]+[A-Za-z0-9._~+/=-]{8,})`), markers: []string{"authorization"}, fold: true},
	// password = …, API_KEY: "…", as .env files and configs have them
	{kind: KindSecret, re: regexp.MustCompile(`(?i)[A-Za-z0-9_.-]*(?:` + secretNames + `)[A-Za-z0-9_.-]*` + quote + `[ \t]*[:=][ \t]*` + quote + `(` + secretCh + `{8,})`),
		markers: []string{"pass", "secret", "token", "key", "credential"}, fold: true, ok: secretValue, at: fieldAt},
	// an env var's name that only ends in _KEY or _PASS, upper case: SERVICE_X_KEY, DB_PASS
	{kind: KindSecret, re: regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])[A-Za-z0-9_.-]*_(?:KEY|PASS)` + quote + `[ \t]*[:=][ \t]*` + quote + `(` + secretCh + `{8,})`),
		markers: []string{"_KEY", "_PASS"}, ok: secretValue, at: plainKeyAt},
	{kind: KindEmail, re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`), markers: []string{"@"}, bound: alnum + "_%+-", ok: realEmail, at: emailAt},
	{kind: KindIDCard, re: regexp.MustCompile(`[1-9]\d{5}(?:18|19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]`), bound: alnum, ok: chineseID, at: notDecimal},
	{kind: KindBankCard, re: regexp.MustCompile(`[3-6]\d{3}(?:[ -]?\d{4}){2,3}(?:[ -]?\d{1,3})?`), bound: alnum, ok: luhn, at: notDecimal},
}

type span struct {
	start, end int
	kind       string
}

// find returns the values the rules find in s, skipping kinds whose mode is off.
func find(s string, pol Policy) []span {
	if len(s) < 3 {
		return nil
	}
	var lower string
	var found []span
	for _, r := range rules {
		if pol.mode(r.kind) == ModeOff {
			continue
		}
		if len(r.markers) > 0 {
			hay := s
			if r.fold {
				if lower == "" {
					lower = strings.ToLower(s)
				}
				hay = lower
			}
			hit := false
			for _, mk := range r.markers {
				if strings.Contains(hay, mk) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		for _, m := range r.re.FindAllStringSubmatchIndex(s, -1) {
			a, b := m[0], m[1]
			if len(m) >= 4 && m[2] >= 0 {
				a, b = m[2], m[3]
			}
			if r.takes(s, a, b) {
				found = append(found, span{a, b, r.kind})
			}
		}
	}
	return found
}

func (r rule) takes(s string, a, b int) bool {
	if b <= a {
		return false
	}
	if r.bound != "" && (a > 0 && strings.IndexByte(r.bound, s[a-1]) >= 0 || b < len(s) && strings.IndexByte(r.bound, s[b]) >= 0) {
		return false
	}
	if r.ok != nil && !r.ok(s[a:b]) {
		return false
	}
	return r.at == nil || r.at(s, a, b)
}

// resolve orders found spans and drops overlaps: the first to start wins, of those the longest,
// and of two the same the rule listed first. Nothing inside an existing placeholder is kept.
func resolve(s string, found []span) []span {
	if len(found) == 0 {
		return nil
	}
	if strings.Contains(s, "{{") {
		for _, p := range placeholderRe.FindAllStringIndex(s, -1) {
			out := found[:0]
			for _, f := range found {
				if !(f.start < p[1] && f.end > p[0]) {
					out = append(out, f)
				}
			}
			found = out
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].start != found[j].start {
			return found[i].start < found[j].start
		}
		return found[i].end > found[j].end
	})
	out := found[:0]
	next := 0
	for _, f := range found {
		if f.start < next {
			continue
		}
		out = append(out, f)
		next = f.end
	}
	return out
}

// ---- value checks ----------------------------------------------------------------

// notAVariable turns away what stands in for a value: ${PASS}, <password>, ****.
func notAVariable(v string) bool {
	if v == "" || strings.ContainsAny(v[:1], "$%{<[*") {
		return false
	}
	return strings.Trim(v, "*xX.") != ""
}

// secretValue is a value with letters and digits both that is no variable or env lookup.
func secretValue(v string) bool {
	if !notAVariable(v) || strings.HasPrefix(v, "process.env") || strings.HasPrefix(v, "os.") {
		return false
	}
	return strings.ContainsAny(v, digits) && strings.IndexFunc(v, isLetter) >= 0
}

func isLetter(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }

// notASlug turns away an sk- word that is a name, not a key: sk-learn-preprocessing-utils.
func notASlug(v string) bool {
	if strings.ContainsFunc(v, func(r rune) bool { return r >= 'A' && r <= 'Z' }) {
		return true
	}
	if !strings.ContainsFunc(strings.TrimPrefix(v, "sk-"), func(r rune) bool {
		return r != '-' && r != '_' && (r < '0' || r > '9') && (r < 'a' || r > 'f')
	}) {
		return true // hex: a key
	}
	for _, w := range strings.FieldsFunc(v, func(r rune) bool { return r == '-' || r == '_' }) {
		if len(w) >= 16 {
			return true
		}
	}
	return false
}

var identPath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*$`)

var stampRe = regexp.MustCompile(`^\d{4}-\d{1,2}-\d{1,2}[Tt]\d{1,2}(?::\d{1,2}){0,2}(?:\.\d+)?(?:[Zz]|[+-]\d{2}:?\d{2})?$`)

var versionRe = regexp.MustCompile(`^(?:[~^]|[<>=!~]=?|==)?[vV]?\d+(?:\.\d+)+(?:[-+._]?(?:(?i:alpha|beta|rc|pre|preview|post|dev|canary|next|nightly|snapshot|release|final|ga|stable|build)\d*|[abcmABCM]\d+|\d+))*$`)

// fieldAt turns away a named field's value that is code naming a secret, not the secret:
// token: getToken(), secret = cfg.secret2, a version, a timestamp.
func fieldAt(s string, a, b int) bool {
	v := s[a:b]
	if v[0] == '=' || stampRe.MatchString(v) || versionRe.MatchString(v) {
		return false
	}
	if !identPath.MatchString(v) {
		return true
	}
	// an identifier: never a secret when it is called; after a colon unquoted it is code's
	// (an object literal's {token: myToken,}) when the code goes on right after it
	rest := strings.TrimLeft(s[b:], " \t")
	if strings.HasPrefix(rest, "(") {
		return false
	}
	if a > 0 && (s[a-1] == '"' || s[a-1] == '\'') || !strings.HasSuffix(strings.TrimRight(s[:a], " \t"), ":") {
		return true
	}
	return rest == "" || !strings.ContainsRune(";,)}]|?&<>", rune(rest[0]))
}

// plainKeyAt turns away the upper-case names that only end in KEY and hold no secret.
func plainKeyAt(s string, a, b int) bool {
	i := a
	for i > 0 && strings.IndexByte(`"' `+"\t:=\\", s[i-1]) >= 0 {
		i--
	}
	end := i
	for i > 0 && strings.IndexByte(alnum+"_.-", s[i-1]) >= 0 {
		i--
	}
	name := strings.ToLower(s[i:end])
	for _, plain := range []string{"sort_key", "cache_key", "public_key", "primary_key", "foreign_key", "partition_key"} {
		if strings.HasSuffix(name, plain) {
			return false
		}
	}
	return fieldAt(s, a, b)
}

// notUserName turns away scheme://name:name@ where the "password" is the user name repeated.
func notUserName(s string, a, b int) bool {
	i := strings.LastIndex(s[:a], "//")
	if i < 0 || s[a-1] != ':' {
		return true
	}
	return s[i+2:a-1] != s[a:b]
}

// headerValue says the value after Authorization: or Cookie: is a credential, not prose.
func headerValue(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || !notAVariable(v) {
		return false
	}
	notLetter := func(r rune) bool { return !isLetter(r) }
	f := strings.Fields(v)
	if len(f) == 1 || len(f) == 2 && strings.IndexFunc(f[0], notLetter) < 0 {
		cred := f[len(f)-1]
		return len(cred) >= 8 && notAVariable(cred) && strings.IndexFunc(cred, notLetter) >= 0
	}
	for _, kv := range strings.Split(v, ";") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return false
		}
	}
	return strings.Contains(v, "=")
}

// emailAt turns away an address that is part of a longer dotted token or a URL's userinfo.
func emailAt(s string, a, b int) bool {
	if a > 0 && s[a-1] == '.' {
		return false
	}
	if b+1 < len(s) && s[b] == '.' && strings.IndexByte(tokenCh, s[b+1]) >= 0 {
		return false
	}
	i := a
	for i > 0 && !strings.ContainsRune(" \t\r\n/?#\"'<>@`", rune(s[i-1])) {
		i--
	}
	return !(i >= 3 && s[i-3:i] == "://")
}

// notDecimal turns away digits that are part of a decimal number or a longer digit run.
func notDecimal(s string, a, b int) bool {
	if a > 0 && s[a-1] == '.' {
		return false
	}
	return !(b+1 < len(s) && s[b] == '.' && s[b+1] >= '0' && s[b+1] <= '9')
}

func realEmail(v string) bool {
	at := strings.LastIndexByte(v, '@')
	local, host := v[:at], strings.ToLower(v[at+1:])
	if strings.EqualFold(local, "git") || strings.HasSuffix(local, "@") || strings.Contains(strings.ToLower(local), "noreply") {
		return false
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" {
			return false
		}
	}
	if docFiles[host] {
		return false
	}
	for _, fake := range []string{"example.com", "example.org", "example.net", "localhost", ".test", ".invalid", ".example"} {
		if host == strings.TrimPrefix(fake, ".") || strings.HasSuffix(host, fake) {
			return false
		}
	}
	return mailTLD(labels[len(labels)-1])
}

// docFiles are files agents read by name: "@AGENTS.md" after a word is no address.
var docFiles = map[string]bool{
	"agents.md": true, "claude.md": true, "gemini.md": true, "readme.md": true, "skill.md": true,
	"changelog.md": true, "contributing.md": true, "license.md": true, "todo.md": true,
}

// mailTLD says tld is one mail can be sent to: a common generic TLD, an organisation's own,
// or a two-letter country code that is not more often a file's extension in an agent's text.
// (A curated set rather than the public suffix list, to keep the gateway dependency-free.)
func mailTLD(tld string) bool {
	if localTLDs[tld] || genericTLDs[tld] {
		return true
	}
	return len(tld) == 2 && !fileExt2[tld]
}

var localTLDs = map[string]bool{"local": true, "internal": true, "lan": true, "corp": true, "home": true, "intranet": true, "localdomain": true}

var genericTLDs = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range strings.Fields(`com net org edu gov mil int info biz name pro mobi aero coop museum jobs travel tel asia cat
		io ai app dev page cloud tech online site store shop xyz top club live email world life today space website blog news
		design studio agency company solutions services digital network systems media group global center academy
		work works team chat social link click one fun icu vip art pub wiki zone run tools codes software engineering
		email mail me tv cc ws bz fm am la gg sh moe ink red blue pink lol wtf sexy best cool rocks ninja guru expert
		bank finance capital money fund insure law legal health care clinic dental doctor hospital university school
		college education science capital ventures partners holdings international foundation church community city
		london paris berlin tokyo nyc wang xin top ltd llc inc gmbh love family photo pics video games game`) {
		m[t] = true
	}
	return m
}()

// fileExt2 are two-letter endings that read as a file's extension far more often than as a
// country in a coding agent's text (they are ccTLDs all the same: mail there is rare).
var fileExt2 = map[string]bool{"md": true, "js": true, "ts": true, "py": true, "rs": true, "rb": true, "cs": true, "go": true, "kt": true, "gz": true, "hs": true, "ex": true}

// chineseID checks the mod-11 check digit of an 18-digit PRC resident ID number.
func chineseID(v string) bool {
	w := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i := 0; i < 17; i++ {
		sum += int(v[i]-'0') * w[i]
	}
	return strings.ToUpper(v[17:]) == string("10X98765432"[sum%11])
}

// luhn checks a 15–19 digit card number (spaces and dashes ignored).
func luhn(v string) bool {
	var ds []int
	for _, r := range v {
		if r >= '0' && r <= '9' {
			ds = append(ds, int(r-'0'))
		}
	}
	if len(ds) < 15 || len(ds) > 19 {
		return false
	}
	sum := 0
	for i := range ds {
		d := ds[len(ds)-1-i]
		if i%2 == 1 {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}
