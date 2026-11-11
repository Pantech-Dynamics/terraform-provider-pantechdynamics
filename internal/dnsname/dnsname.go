// Package dnsname mirrors how the backend normalises DNS names and record
// values, so the provider can tell when a configured value and the value the
// backend stored are the same thing written differently ("Mail.Example.com"
// and "mail.example.com."). It also backs the plan-time checks on zone,
// domain and record input. The backend stays the authority: these checks only
// catch what it would refuse anyway.
package dnsname

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Limits from the DNS and domains API contract.
const (
	maxNameLength  = 253
	maxLabelLength = 63
	maxTXTBytes    = 4000
	txtChunkBytes  = 255
)

// RecordTypes are the record types a record set can have.
var RecordTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA", "NS", "PTR"}

var caaTags = []string{"issue", "issuewild", "iodef"}

// Normalize lower-cases a name, drops one trailing dot and checks its syntax.
// Underscores are allowed, as in "_dmarc". It reports false for an invalid
// name.
func Normalize(raw string) (string, bool) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if name == "" || len(name) > maxNameLength {
		return "", false
	}
	for _, label := range strings.Split(name, ".") {
		if !validLabel(label, true) {
			return "", false
		}
	}
	return name, true
}

// ZoneNameProblem returns why raw is not a zone or domain name as the
// backend stores it (lower case, no trailing dot, at least two labels, no
// underscores), or "" when it is one. Asking for the stored form, instead of
// accepting any spelling, keeps the name in state equal to the configuration.
func ZoneNameProblem(raw string) string {
	name, ok := Normalize(raw)
	if !ok {
		return "must be a domain name such as example.com"
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "must have at least two labels, such as example.com"
	}
	for _, label := range labels {
		if !validLabel(label, false) {
			return "must be a domain name such as example.com, without underscores"
		}
	}
	if allDigits(labels[len(labels)-1]) {
		return "must not end in an all-digit label"
	}
	if raw != name {
		return "must be written in lower case, without a trailing dot: " + strconv.Quote(name)
	}
	return ""
}

// CanonicalHost lower-cases a host name and adds the trailing dot the backend
// returns. It reports false for an invalid host name or one with one label.
func CanonicalHost(raw string) (string, bool) {
	name, ok := Normalize(raw)
	if !ok || !strings.Contains(name, ".") {
		return "", false
	}
	return name + ".", true
}

// RelativeName turns a record name as a user writes it ("@", "", "www",
// "WWW", "www.example.com.") into the relative form the backend returns
// ("@", "www"). It reports false for a name outside the zone or an invalid
// one.
func RelativeName(zone, raw string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" || name == "@" {
		return "@", true
	}
	if strings.HasSuffix(name, ".") {
		name = strings.TrimSuffix(name, ".")
		if !under(name, zone) {
			return "", false
		}
	} else if !under(name, zone) {
		name += "." + zone
	}
	if name == zone {
		return "@", true
	}
	relative := strings.TrimSuffix(name, "."+zone)
	for i, label := range strings.Split(relative, ".") {
		if label == "*" && i == 0 {
			continue
		}
		if !validLabel(label, true) {
			return "", false
		}
	}
	return relative, len(name) <= maxNameLength
}

// RelativeNameProblem checks a record name without knowing the zone: a
// relative name (the common case) must be valid labels. A name ending in a dot
// is fully qualified and is left to the backend, which knows the zone.
func RelativeNameProblem(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" || name == "@" || strings.HasSuffix(name, ".") {
		return ""
	}
	for i, label := range strings.Split(name, ".") {
		if label == "*" && i == 0 {
			continue
		}
		if !validLabel(label, true) {
			return "must be \"@\" for the zone apex, or labels such as \"www\", \"*.dev\" or \"_dmarc\""
		}
	}
	return ""
}

// CanonicalValue returns the form the backend stores a record value in, or a
// reason it would refuse it. Whether an address is private is checked
// separately (PrivateAddressProblem), because it depends on allow_private.
func CanonicalValue(recordType, raw string) (string, string) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", "is empty"
	}
	switch recordType {
	case "A", "AAAA":
		return canonicalAddress(recordType, v)
	case "CNAME", "NS", "PTR":
		host, ok := CanonicalHost(v)
		if !ok {
			return "", "must be a host name"
		}
		return host, ""
	case "MX":
		return canonicalMX(v)
	case "SRV":
		return canonicalSRV(v)
	case "CAA":
		return canonicalCAA(v)
	case "TXT":
		return canonicalTXT(v)
	}
	return "", "has an unsupported record type"
}

// PrivateAddressProblem returns a reason when an A or AAAA value is a private
// or carrier-grade NAT address, which the backend publishes only with
// allow_private.
func PrivateAddressProblem(recordType, raw string) string {
	if recordType != "A" && recordType != "AAAA" {
		return ""
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if addr.IsPrivate() || cgnat.Contains(addr) {
		return "is a private address; set allow_private = true to publish it"
	}
	return ""
}

// SameValues reports whether the configured values and the stored values are
// the same set once each configured value is put in canonical form. A value
// the backend would refuse never matches.
func SameValues(recordType string, configured, stored []string) bool {
	canon := make([]string, 0, len(configured))
	for _, v := range configured {
		c, problem := CanonicalValue(recordType, v)
		if problem != "" {
			return false
		}
		canon = append(canon, c)
	}
	return sameSet(canon, stored)
}

// SameHosts reports whether two lists of host names are the same set, ignoring
// case and a trailing dot.
func SameHosts(a, b []string) bool {
	norm := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, h := range in {
			out = append(out, strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), "."))
		}
		return out
	}
	return sameSet(norm(a), norm(b))
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func canonicalAddress(recordType, v string) (string, string) {
	addr, err := netip.ParseAddr(v)
	if recordType == "A" && (err != nil || !addr.Is4()) {
		return "", "must be an IPv4 address"
	}
	if recordType == "AAAA" && (err != nil || !addr.Is6() || addr.Is4In6()) {
		return "", "must be an IPv6 address"
	}
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() {
		return "", "is not a usable address"
	}
	return addr.String(), ""
}

func canonicalMX(v string) (string, string) {
	fields := strings.Fields(v)
	if len(fields) != 2 {
		return "", `must be "priority host", e.g. "10 mail.example.com"`
	}
	priority, ok := uint16Field(fields[0])
	if !ok {
		return "", "priority must be 0 to 65535"
	}
	if fields[1] == "." {
		return strconv.Itoa(priority) + " .", ""
	}
	host, ok := CanonicalHost(fields[1])
	if !ok {
		return "", "must end in a host name"
	}
	return strconv.Itoa(priority) + " " + host, ""
}

func canonicalSRV(v string) (string, string) {
	fields := strings.Fields(v)
	if len(fields) != 4 {
		return "", `must be "priority weight port target", e.g. "10 5 5060 sip.example.com"`
	}
	parts := make([]string, 0, 4)
	for _, f := range fields[:3] {
		n, ok := uint16Field(f)
		if !ok {
			return "", "priority, weight and port must each be 0 to 65535"
		}
		parts = append(parts, strconv.Itoa(n))
	}
	if fields[3] == "." {
		return strings.Join(parts, " ") + " .", ""
	}
	host, ok := CanonicalHost(fields[3])
	if !ok {
		return "", "target must be a host name"
	}
	return strings.Join(append(parts, host), " "), ""
}

func canonicalCAA(v string) (string, string) {
	fields := strings.SplitN(v, " ", 3)
	if len(fields) != 3 {
		return "", `must be "flags tag value", e.g. 0 issue "ca.example.net"`
	}
	flags, err := strconv.Atoi(fields[0])
	if err != nil || (flags != 0 && flags != 128) {
		return "", "flags must be 0 or 128"
	}
	tag := strings.ToLower(fields[1])
	if !slices.Contains(caaTags, tag) {
		return "", "tag must be issue, issuewild or iodef"
	}
	value := strings.TrimSpace(fields[2])
	value = strings.TrimSuffix(strings.TrimPrefix(value, `"`), `"`)
	if len(value) > 255 || strings.ContainsAny(value, "\"\\\r\n") {
		return "", "value must be at most 255 characters, without quotes or backslashes"
	}
	if tag == "iodef" && !strings.HasPrefix(value, "mailto:") && !strings.HasPrefix(value, "https://") {
		return "", "an iodef value must be a mailto: or https:// URL"
	}
	return strconv.Itoa(flags) + " " + tag + ` "` + value + `"`, ""
}

// canonicalTXT accepts one unquoted string or one or more quoted strings and
// returns quoted 255-byte chunks, the form the backend stores.
func canonicalTXT(v string) (string, string) {
	text := v
	if strings.HasPrefix(v, `"`) {
		parts, ok := parseQuoted(v)
		if !ok {
			return "", "has unbalanced quotes"
		}
		text = strings.Join(parts, "")
	}
	if len(text) > maxTXTBytes {
		return "", "must be at most 4000 bytes"
	}
	for i := 0; i < len(text); i++ {
		if text[i] < 0x20 || text[i] > 0x7e {
			return "", "must be printable ASCII"
		}
	}
	var b strings.Builder
	for start := 0; ; start += txtChunkBytes {
		end := min(start+txtChunkBytes, len(text))
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		for _, c := range []byte(text[start:end]) {
			if c == '"' || c == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
		}
		b.WriteByte('"')
		if end == len(text) {
			break
		}
	}
	return b.String(), ""
}

// parseQuoted reads `"a" "b\"c"` into its strings.
func parseQuoted(v string) ([]string, bool) {
	var out []string
	i := 0
	for i < len(v) {
		for i < len(v) && v[i] == ' ' {
			i++
		}
		if i >= len(v) {
			break
		}
		if v[i] != '"' {
			return nil, false
		}
		i++
		var b strings.Builder
		closed := false
		for i < len(v) {
			c := v[i]
			if c == '\\' && i+1 < len(v) {
				b.WriteByte(v[i+1])
				i += 2
				continue
			}
			i++
			if c == '"' {
				closed = true
				break
			}
			b.WriteByte(c)
		}
		if !closed {
			return nil, false
		}
		out = append(out, b.String())
	}
	return out, len(out) > 0
}

func uint16Field(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0 && n <= 65535
}

func under(name, suffix string) bool {
	return name == suffix || strings.HasSuffix(name, "."+suffix)
}

func validLabel(label string, allowUnderscore bool) bool {
	if label == "" || len(label) > maxLabelLength || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		case c == '_' && allowUnderscore:
		default:
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
