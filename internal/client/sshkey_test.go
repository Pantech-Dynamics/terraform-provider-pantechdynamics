package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const sshKeyJSON = `{"id":"sshk_1","name":"laptop","fingerprint":"SHA256:abc","public_key":"ssh-ed25519 AAAA","created_at":"2026-10-03T22:07:37.993992619Z"}`

func TestCreateSSHKey(t *testing.T) {
	tests := []struct {
		name        string
		req         CreateSSHKeyRequest
		respStatus  int
		respBody    string
		wantBodyHas string
		wantNoField string
		wantPrivate string
		wantErr     func(error) bool
	}{
		{
			name:        "with public key",
			req:         CreateSSHKeyRequest{Name: "laptop", PublicKey: "ssh-ed25519 AAAA"},
			respStatus:  201,
			respBody:    sshKeyJSON,
			wantBodyHas: `"public_key":"ssh-ed25519 AAAA"`,
		},
		{
			name:        "generated keypair returns private key once",
			req:         CreateSSHKeyRequest{Name: "laptop"},
			respStatus:  201,
			respBody:    strings.Replace(sshKeyJSON, "}", `,"private_key":"PRIVATE"}`, 1),
			wantNoField: "public_key",
			wantPrivate: "PRIVATE",
		},
		{
			name:       "name taken",
			req:        CreateSSHKeyRequest{Name: "laptop"},
			respStatus: 409,
			respBody:   `{"status":409,"code":"SSH_KEY_NAME_TAKEN"}`,
			wantErr:    func(e error) bool { return HasCode(e, CodeSSHKeyNameTaken) },
		},
		{
			name:       "already registered",
			req:        CreateSSHKeyRequest{Name: "laptop", PublicKey: "ssh-ed25519 AAAA"},
			respStatus: 409,
			respBody:   `{"status":409,"code":"SSH_KEY_ALREADY_EXISTS"}`,
			wantErr:    func(e error) bool { return HasCode(e, CodeSSHKeyAlreadyExists) },
		},
		{
			name:       "validation",
			req:        CreateSSHKeyRequest{Name: "laptop", PublicKey: "nonsense"},
			respStatus: 422,
			respBody:   `{"status":422,"code":"VALIDATION_FAILED","errors":[{"field":"public_key","code":"INVALID_SSH_PUBLIC_KEY","message":"bad"}]}`,
			wantErr: func(e error) bool {
				var apiErr *APIError
				return errors.As(e, &apiErr) && len(apiErr.Errors) == 1 && apiErr.Errors[0].Field == "public_key"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/ssh-keys" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Idempotency-Key") == "" {
					t.Error("missing Idempotency-Key")
				}
				body, _ := io.ReadAll(r.Body)
				if tt.wantBodyHas != "" && !strings.Contains(string(body), tt.wantBodyHas) {
					t.Errorf("body = %s, want it to contain %s", body, tt.wantBodyHas)
				}
				if tt.wantNoField != "" && strings.Contains(string(body), tt.wantNoField) {
					t.Errorf("body = %s, must not contain %s", body, tt.wantNoField)
				}
				w.WriteHeader(tt.respStatus)
				_, _ = w.Write([]byte(tt.respBody))
			})

			key, err := c.CreateSSHKey(context.Background(), tt.req)

			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if key.ID != "sshk_1" || key.Fingerprint != "SHA256:abc" || key.CreatedAt == nil || key.PrivateKey != tt.wantPrivate {
				t.Fatalf("key = %+v", key)
			}
		})
	}
}

func TestCreateSSHKeyNotRetriedOn5xx(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	})

	if _, err := c.CreateSSHKey(context.Background(), CreateSSHKeyRequest{Name: "laptop"}); err == nil {
		t.Fatal("want an error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1: ssh-key create does not replay", calls)
	}
}

func TestGetSSHKey(t *testing.T) {
	tests := []struct {
		name         string
		id           string
		status       int
		body         string
		wantNotFound bool
		wantPath     string
	}{
		{"found", "sshk_1", 200, sshKeyJSON, false, "/v1/ssh-keys/sshk_1"},
		{"not found", "sshk_gone", 404, notFoundBody, true, "/v1/ssh-keys/sshk_gone"},
		{"id is escaped", "a/b", 404, notFoundBody, true, "/v1/ssh-keys/a%2Fb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() != tt.wantPath {
					t.Errorf("path = %q, want %q", r.URL.EscapedPath(), tt.wantPath)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			key, err := c.GetSSHKey(context.Background(), tt.id)

			if tt.wantNotFound {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil || key.Name != "laptop" {
				t.Fatalf("key = %+v, err = %v", key, err)
			}
		})
	}
}

func TestListSSHKeysFollowsCursor(t *testing.T) {
	var cursors []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		switch cursor {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"sshk_1","name":"a"}],"next_cursor":"p2"}`))
		case "p2":
			_, _ = w.Write([]byte(`{"data":[{"id":"sshk_2","name":"b"}],"next_cursor":null}`))
		default:
			t.Errorf("unexpected cursor %q", cursor)
		}
	})

	keys, err := c.ListSSHKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ID != "sshk_1" || keys[1].ID != "sshk_2" {
		t.Fatalf("keys = %+v", keys)
	}
	if len(cursors) != 2 || cursors[1] != "p2" {
		t.Fatalf("cursors = %v", cursors)
	}
}

func TestListSSHKeysSinglePageAndEmpty(t *testing.T) {
	tests := map[string]struct {
		body string
		want int
	}{
		"one page":  {`{"data":[{"id":"sshk_1"},{"id":"sshk_2"}],"next_cursor":null}`, 2},
		"empty":     {`{"data":[],"next_cursor":null}`, 0},
		"no cursor": {`{"data":[{"id":"sshk_1"}]}`, 1},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			})
			keys, err := c.ListSSHKeys(context.Background())
			if err != nil || len(keys) != tt.want {
				t.Fatalf("keys = %+v, err = %v", keys, err)
			}
		})
	}
}

func TestListSSHKeysStopsOnRepeatedCursor(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":[{"id":"sshk_1"}],"next_cursor":"same"}`))
	})
	if _, err := c.ListSSHKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2: a repeated cursor must end the loop", calls)
	}
}

func TestListSSHKeysError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":401,"code":"UNAUTHENTICATED"}`))
	})
	if _, err := c.ListSSHKeys(context.Background()); !HasCode(err, "UNAUTHENTICATED") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteSSHKey(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantNotFound bool
		wantErr      bool
	}{
		{"deleted", 204, "", false, false},
		{"already gone", 404, notFoundBody, true, true},
		{"forbidden", 403, `{"status":403,"code":"INSUFFICIENT_SCOPE"}`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/v1/ssh-keys/sshk_1" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Idempotency-Key") == "" {
					t.Error("missing Idempotency-Key")
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.DeleteSSHKey(context.Background(), "sshk_1")

			if (err != nil) != tt.wantErr || errors.Is(err, ErrNotFound) != tt.wantNotFound {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestSSHKeyOmitsEmptyPrivateKeyWhenEncoded(t *testing.T) {
	b, err := json.Marshal(SSHKey{ID: "sshk_1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private_key") {
		t.Fatalf("encoded = %s, want no private_key", b)
	}
}

func TestSSHKeyCancelledContext(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sshKeyJSON))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetSSHKey(ctx, "sshk_1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
