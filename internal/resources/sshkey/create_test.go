package sshkey

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

const userKey = "ssh-ed25519 AAAAC3Nza user@laptop"

func TestCreateKey(t *testing.T) {
	tests := []struct {
		name        string
		req         client.CreateSSHKeyRequest
		createErr   error
		createLand  bool
		seed        []client.SSHKey
		listErr     error
		wantID      string
		wantErrIs   error
		wantErrText string
		wantCreates int
	}{
		{
			name:        "success",
			req:         client.CreateSSHKeyRequest{Name: "k", PublicKey: userKey},
			wantID:      "sshk_1",
			wantCreates: 1,
		},
		{
			name:        "definite 409 is not recovered",
			req:         client.CreateSSHKeyRequest{Name: "k", PublicKey: userKey},
			createErr:   &client.APIError{Status: 409, Code: client.CodeSSHKeyNameTaken},
			wantErrText: "SSH_KEY_NAME_TAKEN",
			wantCreates: 1,
		},
		{
			name:        "definite 422 is not recovered",
			req:         client.CreateSSHKeyRequest{Name: "k", PublicKey: "nonsense"},
			createErr:   &client.APIError{Status: 422, Code: "VALIDATION_FAILED"},
			wantErrText: "VALIDATION_FAILED",
			wantCreates: 1,
		},
		{
			name:        "lost response with supplied key is adopted",
			req:         client.CreateSSHKeyRequest{Name: "k", PublicKey: userKey},
			createErr:   errConnReset,
			createLand:  true,
			wantID:      "sshk_1",
			wantCreates: 1,
		},
		{
			name:        "5xx with supplied key is adopted",
			req:         client.CreateSSHKeyRequest{Name: "k", PublicKey: userKey},
			createErr:   &client.APIError{Status: 502},
			createLand:  true,
			wantID:      "sshk_1",
			wantCreates: 1,
		},
		{
			name:        "ambiguous failure but key never created",
			req:         client.CreateSSHKeyRequest{Name: "k", PublicKey: userKey},
			createErr:   errConnReset,
			wantErrText: "connection reset",
			wantCreates: 1,
		},
		{
			name:        "same name with a different key is not adopted",
			req:         client.CreateSSHKeyRequest{Name: "k", PublicKey: userKey},
			createErr:   errConnReset,
			seed:        []client.SSHKey{{ID: "sshk_other", Name: "k", PublicKey: "ssh-ed25519 DIFFERENT x"}},
			wantErrText: "connection reset",
			wantCreates: 1,
		},
		{
			name:        "generated key cannot be adopted",
			req:         client.CreateSSHKeyRequest{Name: "k"},
			createErr:   errConnReset,
			createLand:  true,
			wantErrIs:   errGeneratedKeyLost,
			wantErrText: "sshk_1",
			wantCreates: 1,
		},
		{
			name:        "lookup failure keeps the original cause",
			req:         client.CreateSSHKeyRequest{Name: "k", PublicKey: userKey},
			createErr:   errConnReset,
			listErr:     errors.New("list also failed"),
			wantErrText: "connection reset",
			wantCreates: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeAPI{createErr: tt.createErr, createLand: tt.createLand, listErr: tt.listErr, keys: tt.seed}

			key, err := createKey(context.Background(), api, tt.req)

			if api.creates != tt.wantCreates {
				t.Fatalf("creates = %d, want %d (a create must never be re-sent)", api.creates, tt.wantCreates)
			}
			if tt.wantErrText == "" && tt.wantErrIs == nil {
				if err != nil || key.ID != tt.wantID {
					t.Fatalf("key = %+v, err = %v", key, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error, got key %+v", key)
			}
			if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
				t.Errorf("err = %v, want it to wrap %v", err, tt.wantErrIs)
			}
			if !strings.Contains(err.Error(), tt.wantErrText) {
				t.Errorf("err = %q, want it to contain %q", err, tt.wantErrText)
			}
		})
	}
}

func TestCreateKeyCancelledContextIsNotRecovered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api := &fakeAPI{createErr: context.Canceled, createLand: true}

	if _, err := createKey(ctx, api, client.CreateSSHKeyRequest{Name: "k", PublicKey: userKey}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if api.lists != 0 {
		t.Fatal("no lookup should follow a cancelled create")
	}
}
