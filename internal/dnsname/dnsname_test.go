package dnsname

import (
	"strings"
	"testing"
)

func TestCanonicalValue(t *testing.T) {
	tests := []struct {
		typ, in, want, problem string
	}{
		{"A", " 203.0.113.10 ", "203.0.113.10", ""},
		{"A", "2001:db8::1", "", "IPv4"},
		{"A", "127.0.0.1", "", "not a usable"},
		{"AAAA", "2001:0DB8:0:0::1", "2001:db8::1", ""},
		{"CNAME", "Target.Example.com", "target.example.com.", ""},
		{"CNAME", "target.example.com.", "target.example.com.", ""},
		{"CNAME", "localhost", "", "host name"},
		{"MX", "010  Mail.Example.com", "10 mail.example.com.", ""},
		{"MX", "0 .", "0 .", ""},
		{"MX", "mail.example.com", "", "priority host"},
		{"SRV", "10 5 5060 SIP.example.com", "10 5 5060 sip.example.com.", ""},
		{"CAA", `0 ISSUE letsencrypt.org`, `0 issue "letsencrypt.org"`, ""},
		{"CAA", `0 iodef "ftp://x"`, "", "iodef"},
		{"TXT", "v=spf1 -all", `"v=spf1 -all"`, ""},
		{"TXT", `"v=spf1" " -all"`, `"v=spf1 -all"`, ""},
		{"TXT", `"unbalanced`, "", "unbalanced"},
		{"NS", "NS1.Example.NET", "ns1.example.net.", ""},
		{"SOA", "x", "", "unsupported"},
	}
	for _, tt := range tests {
		t.Run(tt.typ+" "+tt.in, func(t *testing.T) {
			got, problem := CanonicalValue(tt.typ, tt.in)
			if got != tt.want || (tt.problem == "") != (problem == "") || !strings.Contains(problem, tt.problem) {
				t.Fatalf("CanonicalValue = %q, %q; want %q, %q", got, problem, tt.want, tt.problem)
			}
		})
	}
}

func TestLongTXTIsChunked(t *testing.T) {
	got, problem := CanonicalValue("TXT", strings.Repeat("a", 300))
	if problem != "" || got != `"`+strings.Repeat("a", 255)+`" "`+strings.Repeat("a", 45)+`"` {
		t.Fatalf("got %q, %q", got, problem)
	}
	// The stored chunked form, written back as config, is the same value.
	if !SameValues("TXT", []string{got}, []string{got}) {
		t.Fatal("the chunked form is not equal to itself")
	}
}

func TestSameValues(t *testing.T) {
	tests := []struct {
		name               string
		typ                string
		configured, stored []string
		want               bool
	}{
		{"case and dot", "CNAME", []string{"Target.Example.com"}, []string{"target.example.com."}, true},
		{"order", "A", []string{"203.0.113.11", "203.0.113.10"}, []string{"203.0.113.10", "203.0.113.11"}, true},
		{"different value", "A", []string{"203.0.113.12"}, []string{"203.0.113.10"}, false},
		{"one missing", "A", []string{"203.0.113.10"}, []string{"203.0.113.10", "203.0.113.11"}, false},
		{"invalid configured value", "A", []string{"nope"}, []string{"nope"}, false},
		{"txt quoted or not", "TXT", []string{"hello"}, []string{`"hello"`}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SameValues(tt.typ, tt.configured, tt.stored); got != tt.want {
				t.Fatalf("SameValues = %v", got)
			}
		})
	}
}

func TestRelativeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"@", "@"}, {"", "@"}, {"WWW", "www"}, {"www.example.com.", "www"}, {"example.com.", "@"},
		{"*.dev", "*.dev"}, {"_dmarc", "_dmarc"}, {"www.example.com", "www"},
	}
	for _, tt := range tests {
		if got, ok := RelativeName("example.com", tt.in); !ok || got != tt.want {
			t.Errorf("RelativeName(%q) = %q, %v; want %q", tt.in, got, ok, tt.want)
		}
	}
	if _, ok := RelativeName("example.com", "www.other.org."); ok {
		t.Error("a name outside the zone was accepted")
	}
}

func TestZoneNameProblem(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":   "",
		"a.example.com": "",
		"Example.com":   "lower case",
		"example.com.":  "lower case",
		"com":           "two labels",
		"ex_ample.com":  "underscores",
		"example.123":   "all-digit",
		"-x.com":        "domain name",
	} {
		got := ZoneNameProblem(in)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("ZoneNameProblem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrivateAddressProblem(t *testing.T) {
	if PrivateAddressProblem("A", "10.0.0.1") == "" || PrivateAddressProblem("A", "100.64.0.1") == "" || PrivateAddressProblem("AAAA", "fd00::1") == "" {
		t.Error("a private address was not reported")
	}
	if PrivateAddressProblem("A", "203.0.113.10") != "" || PrivateAddressProblem("TXT", "10.0.0.1") != "" {
		t.Error("a public address or a TXT value was reported")
	}
}

func TestSameHosts(t *testing.T) {
	if !SameHosts([]string{"NS1.example.net.", "ns2.example.net"}, []string{"ns2.example.net", "ns1.example.net"}) {
		t.Error("equivalent hosts differ")
	}
	if SameHosts([]string{"ns1.example.net"}, []string{"ns1.example.net", "ns2.example.net"}) {
		t.Error("different sets match")
	}
}
