package sshkey

import (
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

func TestSamePublicKey(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "ssh-ed25519 AAAA c", "ssh-ed25519 AAAA c", true},
		{"comment differs", "ssh-ed25519 AAAA one", "ssh-ed25519 AAAA two", true},
		{"no comment vs comment", "ssh-ed25519 AAAA", "ssh-ed25519 AAAA probe", true},
		{"trailing newline", "ssh-ed25519 AAAA c\n", "ssh-ed25519 AAAA c", true},
		{"different body", "ssh-ed25519 AAAA", "ssh-ed25519 BBBB", false},
		{"different type", "ssh-rsa AAAA", "ssh-ed25519 AAAA", false},
		{"garbage", "nonsense", "nonsense", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := samePublicKey(tt.a, tt.b); got != tt.want {
				t.Fatalf("samePublicKey = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToCreateRequest(t *testing.T) {
	tests := []struct {
		name    string
		in      model
		wantKey string
	}{
		{"with key", model{Name: types.StringValue("k"), PublicKey: types.StringValue(userKey)}, userKey},
		{"null key means generate", model{Name: types.StringValue("k"), PublicKey: types.StringNull()}, ""},
		{"unknown key means generate", model{Name: types.StringValue("k"), PublicKey: types.StringUnknown()}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := toCreateRequest(tt.in)
			if req.Name != "k" || req.PublicKey != tt.wantKey {
				t.Fatalf("req = %+v", req)
			}
		})
	}
}

func TestFromAPIResponse(t *testing.T) {
	created := time.Date(2026, 10, 3, 22, 7, 37, 993992619, time.UTC)
	apiKey := &client.SSHKey{ID: "sshk_1", Name: "k", Fingerprint: "SHA256:x", PublicKey: "ssh-ed25519 AAAA probe", CreatedAt: &created}

	t.Run("maps every field", func(t *testing.T) {
		m := fromAPIResponse(model{PublicKey: types.StringUnknown(), PrivateKey: types.StringUnknown()}, apiKey)
		if m.ID.ValueString() != "sshk_1" || m.Fingerprint.ValueString() != "SHA256:x" || m.CreatedAt.ValueString() != "2026-10-03T22:07:37Z" {
			t.Fatalf("m = %+v", m)
		}
		if !m.PrivateKey.IsNull() {
			t.Fatalf("unknown private_key must become null, got %v", m.PrivateKey)
		}
	})

	t.Run("keeps the user's own text for the same key", func(t *testing.T) {
		m := fromAPIResponse(model{PublicKey: types.StringValue("ssh-ed25519 AAAA mine\n")}, apiKey)
		if m.PublicKey.ValueString() != "ssh-ed25519 AAAA mine\n" {
			t.Fatalf("public_key = %q", m.PublicKey.ValueString())
		}
	})

	t.Run("takes the API key when it really differs", func(t *testing.T) {
		m := fromAPIResponse(model{PublicKey: types.StringValue("ssh-ed25519 OTHER mine")}, apiKey)
		if m.PublicKey.ValueString() != "ssh-ed25519 AAAA probe" {
			t.Fatalf("public_key = %q", m.PublicKey.ValueString())
		}
	})

	t.Run("carries the private key over when the API omits it", func(t *testing.T) {
		m := fromAPIResponse(model{PrivateKey: types.StringValue("SECRET")}, apiKey)
		if m.PrivateKey.ValueString() != "SECRET" {
			t.Fatalf("private_key = %v", m.PrivateKey)
		}
	})

	t.Run("takes the private key the API just returned", func(t *testing.T) {
		withPrivate := *apiKey
		withPrivate.PrivateKey = "NEW"
		m := fromAPIResponse(model{PrivateKey: types.StringUnknown()}, &withPrivate)
		if m.PrivateKey.ValueString() != "NEW" {
			t.Fatalf("private_key = %v", m.PrivateKey)
		}
	})

	t.Run("created_at is the same whatever precision the API sent", func(t *testing.T) {
		nanos := time.Date(2026, 10, 3, 22, 35, 3, 908399822, time.UTC)
		micros := time.Date(2026, 10, 3, 22, 35, 3, 908400000, time.UTC)
		a, b := *apiKey, *apiKey
		a.CreatedAt, b.CreatedAt = &nanos, &micros
		if fromAPIResponse(model{}, &a).CreatedAt != fromAPIResponse(model{}, &b).CreatedAt {
			t.Fatal("created_at must not depend on fractional seconds")
		}
	})

	t.Run("missing created_at is null", func(t *testing.T) {
		noTime := *apiKey
		noTime.CreatedAt = nil
		if m := fromAPIResponse(model{}, &noTime); !m.CreatedAt.IsNull() {
			t.Fatalf("created_at = %v", m.CreatedAt)
		}
	})
}
