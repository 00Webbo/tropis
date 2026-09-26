// Package redact strips secrets and personal data from text before it reaches
// a model backend.
//
// Redaction is mandatory and runs on every backend, hosted or local. It is
// pattern-based and therefore imperfect: it reduces exposure, it does not
// eliminate it. Where logs must never reach a third party, the answer is a
// local backend, not a better regex. SECURITY.md says so plainly.
//
// Two kinds of replacement are made:
//
//   - Secrets — keys, tokens, credentials, passwords — become
//     "[REDACTED:<kind>]". There is nothing diagnostic in a secret's value.
//   - Emails and IP addresses become stable pseudonyms such as "[IP-3]",
//     numbered by first appearance and consistent across every string one
//     Redactor processes. The model can still see that the address in an
//     event is the one in a log line, which is often the whole diagnosis,
//     without seeing the address.
//
// Replacements never introduce or remove JSON structural characters, so
// redacting serialised API objects leaves them parseable.
package redact

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Kinds of redaction, used in replacement tokens and in the Summary.
const (
	KindPrivateKey = "private_key"
	KindJWT        = "jwt"
	KindToken      = "token"
	KindAuthHeader = "auth_header"
	KindURLCreds   = "url_credentials"
	KindSecret     = "secret"
	KindEmail      = "email"
	KindIP         = "ip"
	KindLiteral    = "literal"
)

// Redactor applies redaction rules and remembers the pseudonyms it assigned.
// It is safe for concurrent use. Use one Redactor per analysis, so pseudonyms
// are consistent within that analysis and unrelated across analyses.
type Redactor struct {
	mu         sync.Mutex
	pseudonyms map[string]map[string]string // kind -> original -> pseudonym
	counts     map[string]int
	literals   []literal
}

type literal struct {
	value string
	label string
}

// New returns a Redactor with the default rules.
func New() *Redactor {
	return &Redactor{
		pseudonyms: map[string]map[string]string{},
		counts:     map[string]int{},
	}
}

// AddLiteral registers a specific value to redact wherever it appears, for
// sensitive values known structurally rather than by pattern — a drive serial
// number, for instance. Values shorter than four characters are ignored, since
// redacting them would shred unrelated text.
func (r *Redactor) AddLiteral(value, label string) {
	value = strings.TrimSpace(value)
	if len(value) < 4 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.literals {
		if l.value == value {
			return
		}
	}
	r.literals = append(r.literals, literal{value: value, label: label})
	// Longest first, so a value containing another is replaced whole.
	sort.SliceStable(r.literals, func(i, j int) bool { return len(r.literals[i].value) > len(r.literals[j].value) })
}

// rule is one pattern-based redaction.
type rule struct {
	kind string
	re   *regexp.Regexp
	// replace returns the replacement for a match's submatches. m[0] is the
	// full match.
	replace func(r *Redactor, m []string) string
}

func whole(kind string) func(*Redactor, []string) string {
	return func(r *Redactor, _ []string) string { return r.secret(kind) }
}

// keyword-bearing names whose values are secrets.
const secretKey = `[A-Za-z0-9_.-]*(?:password|passwd|pwd|passphrase|secret|token|` +
	`(?:api|access|secret|private|account|signing|encryption|master)[_-]?key|apikey|credentials?|sig)`

// Rules run in order. More specific rules come first so that, for example, a
// JWT inside an Authorization header is reported as an auth header rather
// than half-matched by something looser.
var rules = []rule{
	{
		kind:    KindPrivateKey,
		re:      regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
		replace: whole(KindPrivateKey),
	},
	{
		// Authorization: Bearer <token> / Basic <base64>, in headers or logs.
		kind: KindAuthHeader,
		re:   regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9._~+/=-]{8,}`),
		replace: func(r *Redactor, m []string) string {
			return m[1] + m[2] + r.secret(KindAuthHeader)
		},
	},
	{
		kind:    KindJWT,
		re:      regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`),
		replace: whole(KindJWT),
	},
	{
		// Provider tokens with recognisable prefixes: GitHub, Slack, Anthropic,
		// OpenAI-style, Google API keys, AWS access key IDs, Stripe.
		kind: KindToken,
		re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|` +
			`xox[abprs]-[A-Za-z0-9-]{10,}|sk-ant-[A-Za-z0-9_-]{20,}|sk-[A-Za-z0-9_-]{32,}|` +
			`AIza[0-9A-Za-z_-]{35}|(?:AKIA|ASIA)[0-9A-Z]{16}|[rs]k_live_[0-9A-Za-z]{20,})\b`),
		replace: whole(KindToken),
	},
	{
		// scheme://user:password@host — keep scheme and host, which are
		// diagnostic; drop the credentials, which are not.
		kind: KindURLCreds,
		re:   regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s:@/"'<>]+:[^\s@/"'<>]+@`),
		replace: func(r *Redactor, m []string) string {
			return m[1] + r.secret(KindURLCreds) + "@"
		},
	},
	{
		// A Kubernetes env var whose name marks it as a secret, in API JSON:
		// {"name":"DB_PASSWORD","value":"hunter2"}. The name and value are
		// separate fields, so the key=value rule below cannot see it.
		kind: KindSecret,
		re:   regexp.MustCompile(`(?i)("name"\s*:\s*"` + secretKey + `"\s*,\s*"value"\s*:\s*")((?:[^"\\]|\\.)*)(")`),
		replace: func(r *Redactor, m []string) string {
			return m[1] + r.secret(KindSecret) + m[3]
		},
	},
	{
		// key=value, key: value, "key":"value", in logs, env dumps, DSNs and
		// query strings. The key is kept; it says what kind of thing was there.
		kind: KindSecret,
		re:   regexp.MustCompile(`(?i)\b(` + secretKey + `)(["']?\s*[:=]\s*)(["']?)([^\s"',;&{}\[\]]+)`),
		replace: func(r *Redactor, m []string) string {
			switch strings.ToLower(m[4]) {
			case "true", "false", "null", "none", "":
				// A boolean named like a secret, e.g.
				// automountServiceAccountToken: false. Nothing to hide.
				return m[0]
			}
			return m[1] + m[2] + m[3] + r.secret(KindSecret)
		},
	},
	{
		kind: KindEmail,
		re:   regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`),
		replace: func(r *Redactor, m []string) string {
			return r.pseudonym(KindEmail, "EMAIL", strings.ToLower(m[0]))
		},
	},
	{
		kind: KindIP,
		re:   regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`),
		replace: func(r *Redactor, m []string) string {
			ip := net.ParseIP(m[0])
			if ip == nil || ip.To4() == nil {
				return m[0] // 999.1.2.3 and similar: not an address
			}
			return r.pseudonym(KindIP, "IP", ip.String())
		},
	},
	{
		// IPv6 candidates: hex groups separated by colons, confirmed by the
		// parser. Timestamps such as 12:00:00 do not parse and are left alone.
		//
		// Two further guards keep source code in stack traces intact, since
		// std::vector and a::b are syntactically valid IPv6: the candidate must
		// not be embedded in a word, and must have at least three hex groups
		// unless it is the loopback. Real addresses in logs essentially always
		// qualify. The zone suffix names a local interface and goes with the
		// address.
		kind: KindIP,
		re:   regexp.MustCompile(`(?i)(^|[^0-9a-z_.:])((?:[0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4})(?:%[0-9a-z]+)?([^0-9a-z_:]|$)`),
		replace: func(r *Redactor, m []string) string {
			lead, candidate, trail := m[1], m[2], m[3]
			groups := 0
			for _, g := range strings.Split(candidate, ":") {
				if g != "" {
					groups++
				}
			}
			if groups < 3 && candidate != "::1" {
				return m[0]
			}
			ip := net.ParseIP(candidate)
			if ip == nil || ip.To4() != nil {
				return m[0]
			}
			return lead + r.pseudonym(KindIP, "IP", ip.String()) + trail
		},
	},
}

// String redacts s.
func (r *Redactor) String(s string) string {
	if s == "" {
		return s
	}

	r.mu.Lock()
	literals := append([]literal(nil), r.literals...)
	r.mu.Unlock()

	for _, l := range literals {
		if n := strings.Count(s, l.value); n > 0 {
			s = strings.ReplaceAll(s, l.value, "[REDACTED:"+l.label+"]")
			r.count(KindLiteral, n)
		}
	}

	for _, rl := range rules {
		rl := rl
		s = rl.re.ReplaceAllStringFunc(s, func(match string) string {
			return rl.replace(r, rl.re.FindStringSubmatch(match))
		})
	}
	return s
}

// Bytes redacts b.
func (r *Redactor) Bytes(b []byte) []byte {
	return []byte(r.String(string(b)))
}

func (r *Redactor) secret(kind string) string {
	r.count(kind, 1)
	return "[REDACTED:" + kind + "]"
}

func (r *Redactor) pseudonym(kind, prefix, original string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[kind]++
	m := r.pseudonyms[kind]
	if m == nil {
		m = map[string]string{}
		r.pseudonyms[kind] = m
	}
	if p, ok := m[original]; ok {
		return p
	}
	p := fmt.Sprintf("[%s-%d]", prefix, len(m)+1)
	m[original] = p
	return p
}

func (r *Redactor) count(kind string, n int) {
	r.mu.Lock()
	r.counts[kind] += n
	r.mu.Unlock()
}

// Summary reports how many redactions of each kind were made. It carries
// counts only, never values, so it is safe to log.
func (r *Redactor) Summary() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.counts))
	for k, v := range r.counts {
		out[k] = v
	}
	return out
}
