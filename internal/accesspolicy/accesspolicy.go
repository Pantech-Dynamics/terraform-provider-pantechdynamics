// Package accesspolicy holds what the access policy resource, the policy
// document data source and the policy lookup share: plan-time checks that
// mirror the backend's (internal/control/domain/access_policy.go), the
// normal form two statement lists are compared in so plans do not flap, and
// the JSON document form ({"statements": [...]}) that policy_json takes.
package accesspolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// Limits from ADR 0172 and the access policies API.
const (
	MaxStatements         = 20
	MaxActions            = 50
	MaxResources          = 50
	MaxSourceIPs          = 20
	MaxSidLength          = 64
	MaxNameLength         = 64
	MaxDescriptionLength  = 255
	maxResourceLength     = 260
	maxNamePatternLength  = 253
	maxActionPatternLimit = 128
)

// Problem is one thing the backend would refuse in a statement list.
// Statement is the statement's index, or -1 for the list itself; Field is the
// attribute inside the statement ("actions", "conditions.source_ip", ...).
type Problem struct {
	Statement int
	Field     string
	Message   string
}

// Document is the JSON form of a policy's statements, the shape policy_json
// takes and pantechdynamics_access_policy_document produces.
type Document struct {
	Statements []client.PolicyStatement `json:"statements"`
}

// Validate checks statements as the backend does and returns every problem,
// so the user fixes them all at once.
func Validate(statements []client.PolicyStatement) []Problem {
	var problems []Problem
	if len(statements) == 0 || len(statements) > MaxStatements {
		return []Problem{{Statement: -1, Message: fmt.Sprintf("A policy has 1 to %d statements, got %d.", MaxStatements, len(statements))}}
	}
	sids := map[string]bool{}
	for i, s := range statements {
		add := func(field, msg string) {
			problems = append(problems, Problem{Statement: i, Field: field, Message: msg})
		}
		if s.Sid != "" {
			if !validSid(s.Sid) {
				add("sid", fmt.Sprintf("A sid is 1 to %d letters, digits, '_' or '-', got %q.", MaxSidLength, s.Sid))
			} else if sids[s.Sid] {
				add("sid", fmt.Sprintf("The sid %q is used by more than one statement; each must be unique.", s.Sid))
			}
			sids[s.Sid] = true
		}
		if s.Effect != "allow" && s.Effect != "deny" {
			add("effect", fmt.Sprintf(`Must be "allow" or "deny", got %q.`, s.Effect))
		}
		if n := len(s.Actions); n == 0 || n > MaxActions {
			add("actions", fmt.Sprintf("A statement has 1 to %d actions, got %d.", MaxActions, n))
		}
		for _, a := range s.Actions {
			if msg := ActionProblem(a); msg != "" {
				add("actions", msg)
			}
		}
		if n := len(s.Resources); n == 0 || n > MaxResources {
			add("resources", fmt.Sprintf("A statement has 1 to %d resources, got %d.", MaxResources, n))
		}
		for _, r := range s.Resources {
			if msg := ResourceProblem(r); msg != "" {
				add("resources", msg)
			}
		}
		if c := s.Conditions; c != nil && c.SourceIP != nil {
			if n := len(c.SourceIP); n == 0 || n > MaxSourceIPs {
				add("conditions.source_ip", fmt.Sprintf("source_ip has 1 to %d CIDR blocks, got %d; leave it out for any address.", MaxSourceIPs, n))
			}
			for _, cidr := range c.SourceIP {
				if _, ok := CanonicalCIDR(cidr); !ok {
					add("conditions.source_ip", fmt.Sprintf("%q is not an IPv4 or IPv6 CIDR block such as 102.89.0.0/16, or a single address.", cidr))
				}
			}
		}
	}
	return problems
}

// ActionProblem checks one action pattern's form: "*", or <service>:<verb>
// where the service is lower-case letters and underscores and the verb is
// letters, either with "*" wildcards. Whether it names a catalogue action is
// checked against GET /iam/actions (MatchesAction), since the catalogue grows.
func ActionProblem(a string) string {
	if a == "*" {
		return ""
	}
	if a == "" || len(a) > maxActionPatternLimit {
		return fmt.Sprintf("%q is not an action such as instances:Stop, a wildcard such as instances:* or *:Read, or *.", a)
	}
	service, verb, ok := strings.Cut(a, ":")
	if !ok || service == "" || verb == "" || strings.Contains(verb, ":") {
		return fmt.Sprintf("%q must be <service>:<verb>, such as instances:Stop.", a)
	}
	for _, r := range service {
		if !isLower(r) && r != '_' && r != '*' {
			return fmt.Sprintf("In %q, the service is lower-case letters and underscores, such as load_balancers.", a)
		}
	}
	for _, r := range verb {
		if !isLower(r) && !isUpper(r) && r != '*' {
			return fmt.Sprintf("In %q, the verb is letters, such as Stop or ChangeRecords.", a)
		}
	}
	return ""
}

// MatchesAction reports whether the pattern matches the catalogue action.
func MatchesAction(pattern, action string) bool {
	if pattern == "*" {
		return true
	}
	ps, pv, ok1 := strings.Cut(pattern, ":")
	as, av, ok2 := strings.Cut(action, ":")
	return ok1 && ok2 && glob(ps, as) && glob(pv, av)
}

// glob matches '*' against any run of characters, case-sensitively.
func glob(pattern, s string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// ResourceProblem checks one resource: "*", a public id such as vm_01JD..., or
// name:<glob>.
func ResourceProblem(r string) string {
	const msg = `%q must be "*", a resource id such as vm_01JD..., or name:<pattern> such as name:staging-*.`
	if r == "*" {
		return ""
	}
	if r == "" || len(r) > maxResourceLength || !utf8.ValidString(r) {
		return fmt.Sprintf(msg, r)
	}
	if pattern, ok := strings.CutPrefix(r, "name:"); ok {
		if pattern == "" || utf8.RuneCountInString(pattern) > maxNamePatternLength {
			return fmt.Sprintf("%q: a name pattern is 1 to %d characters after name:.", r, maxNamePatternLength)
		}
		for _, c := range pattern {
			if !unicode.IsPrint(c) {
				return fmt.Sprintf("%q: a name pattern is printable characters.", r)
			}
		}
		return ""
	}
	if !isResourceID(r) {
		return fmt.Sprintf(msg, r)
	}
	return ""
}

// isResourceID mirrors the backend's shape check: a lower-case prefix, one
// '_', then 1 to 64 letters and digits.
func isResourceID(s string) bool {
	prefix, rest, ok := strings.Cut(s, "_")
	if !ok || prefix == "" || len(prefix) > 16 || rest == "" || len(rest) > 64 {
		return false
	}
	for i, r := range prefix {
		if !isLower(r) && (i == 0 || !isDigit(r)) {
			return false
		}
	}
	for _, r := range rest {
		if !isLower(r) && !isUpper(r) && !isDigit(r) {
			return false
		}
	}
	return true
}

// CanonicalCIDR returns the block as the backend stores it: masked to its
// network, a bare address as its /32 or /128. IPv4-mapped IPv6 is refused,
// as the backend refuses it.
func CanonicalCIDR(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 {
		return "", false
	}
	if !strings.Contains(raw, "/") {
		addr, err := netip.ParseAddr(raw)
		if err != nil || addr.Zone() != "" || addr.Is4In6() {
			return "", false
		}
		return netip.PrefixFrom(addr, addr.BitLen()).String(), true
	}
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || prefix.Addr().Is4In6() {
		return "", false
	}
	return prefix.Masked().String(), true
}

func validSid(sid string) bool {
	if sid == "" || len(sid) > MaxSidLength {
		return false
	}
	for _, r := range sid {
		if !isLower(r) && !isUpper(r) && !isDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// Normalize returns the statements in the form two lists are compared in:
// actions, resources and CIDRs de-duplicated and sorted (their order means
// nothing), CIDRs canonical, and empty conditions as none. The statement
// order is kept. The input is not modified.
func Normalize(statements []client.PolicyStatement) []client.PolicyStatement {
	out := make([]client.PolicyStatement, 0, len(statements))
	for _, s := range statements {
		n := client.PolicyStatement{Sid: s.Sid, Effect: s.Effect, Actions: sortedUnique(s.Actions), Resources: sortedUnique(s.Resources)}
		if c := s.Conditions; c != nil && (len(c.SourceIP) > 0 || c.MFA != nil) {
			nc := &client.PolicyConditions{}
			if len(c.SourceIP) > 0 {
				cidrs := make([]string, 0, len(c.SourceIP))
				for _, raw := range c.SourceIP {
					if canon, ok := CanonicalCIDR(raw); ok {
						cidrs = append(cidrs, canon)
					} else {
						cidrs = append(cidrs, raw)
					}
				}
				nc.SourceIP = sortedUnique(cidrs)
			}
			if c.MFA != nil {
				v := *c.MFA
				nc.MFA = &v
			}
			n.Conditions = nc
		}
		out = append(out, n)
	}
	return out
}

// Equal reports whether two statement lists mean the same policy.
func Equal(a, b []client.PolicyStatement) bool {
	return reflect.DeepEqual(Normalize(a), Normalize(b))
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// ParseDocument reads policy JSON: {"statements": [...]}. Unknown fields are
// refused, as the backend refuses them, so a typo such as "action" is caught
// at plan time.
func ParseDocument(raw string) ([]client.PolicyStatement, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc Document
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf(`policy JSON must be {"statements": [{"effect", "actions", "resources", ...}]}: %w`, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("policy JSON holds more than one document")
	}
	return doc.Statements, nil
}

// MarshalDocument writes statements as policy JSON, indented, in the order
// given. The same statements always produce the same text.
func MarshalDocument(statements []client.PolicyStatement) (string, error) {
	if statements == nil {
		statements = []client.PolicyStatement{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(Document{Statements: statements}); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// NameProblem checks a policy name as the backend does: 1 to 64 letters,
// digits, spaces, '.', '_' or '-', starting with a letter or digit, and not
// the reserved pantech_ prefix.
func NameProblem(name string) string {
	if name == "" || len(name) > MaxNameLength {
		return fmt.Sprintf("A policy name is 1 to %d characters, got %d.", MaxNameLength, len(name))
	}
	if strings.HasPrefix(strings.ToLower(name), "pantech_") {
		return "Names starting with pantech_ are reserved for Pantech's managed policies."
	}
	if strings.TrimSpace(name) != name {
		return "A policy name cannot start or end with a space."
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !ok && (i == 0 || (r != ' ' && r != '.' && r != '_' && r != '-')) {
			return fmt.Sprintf("A policy name is letters, digits, spaces, '.', '_' or '-', starting with a letter or digit; got %q.", name)
		}
	}
	return ""
}

func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }
