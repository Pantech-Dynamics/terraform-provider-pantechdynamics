package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const namespaceJSON = `{"name":"acme","registry_host":"registry.pantechdynamics.com","create_on_push":true,"repository_count":2,"storage_bytes":1024,"storage_limit_bytes":53687091200,"repository_limit":100,"created_at":"2026-11-10T09:00:00Z","updated_at":"2026-11-10T09:00:00Z","version":2}`

const repositoryJSON = `{"id":"crr_1","namespace":"acme","name":"api/server","full_name":"registry.pantechdynamics.com/acme/api/server","description":"","visibility":"private","immutable_tags":false,"retention":{"keep_last":50,"untagged_after_days":null,"keep_pattern":"v*"},"tag_count":3,"storage_bytes":2048,"last_pushed_at":null,"status":"active","suspended_reason":null,"created_at":"2026-11-01T09:00:00Z","updated_at":"2026-11-10T10:00:00Z","version":4}`

const tokenJSON = `{"id":"prt_1","name":"ci","permission":"push","repositories":["api/server"],"expires_at":"2027-11-10T00:00:00Z","last_used_at":null,"created_at":"2026-11-10T09:00:00Z","revoked_at":null}`

func TestGetRegistryNamespace(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr func(error) bool
	}{
		{"claimed", 200, namespaceJSON, nil},
		{"not claimed yet", 404, `{"status":404,"code":"REGISTRY_NAMESPACE_NOT_SET"}`, func(e error) bool {
			return HasCode(e, CodeRegistryNamespaceNotSet) && !errors.Is(e, ErrNotFound)
		}},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, func(e error) bool { return HasCode(e, "UNAUTHENTICATED") }},
		{"registry not offered", 503, `{"status":503,"code":"REGISTRY_UNAVAILABLE"}`, func(e error) bool { return HasCode(e, CodeRegistryUnavailable) }},
		{"malformed json", 200, `{"name":`, func(e error) bool { return e != nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/registry" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			ns, err := c.GetRegistryNamespace(context.Background())
			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || ns.Name != "acme" || ns.StorageLimitBytes != 53687091200 || !ns.CreateOnPush {
				t.Fatalf("namespace = %+v, err = %v", ns, err)
			}
		})
	}
}

func TestSetRegistryNamespace(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"claimed", 200, namespaceJSON, ""},
		{"locked", 409, `{"status":409,"code":"REGISTRY_NAMESPACE_LOCKED"}`, CodeRegistryNamespaceLocked},
		{"taken", 409, `{"status":409,"code":"REGISTRY_NAMESPACE_TAKEN"}`, CodeRegistryNamespaceTaken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPut || r.URL.Path != "/v1/registry" || string(body) != `{"name":"acme","create_on_push":false}` || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s %s", r.Method, r.URL.Path, body)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			off := false
			_, err := c.SetRegistryNamespace(context.Background(), SetRegistryNamespaceRequest{Name: "acme", CreateOnPush: &off})
			if (tt.wantErr == "") != (err == nil) || (tt.wantErr != "" && !HasCode(err, tt.wantErr)) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestCreateRepository(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateRepositoryRequest
		want    string
		status  int
		body    string
		wantErr string
	}{
		{"defaults are left out", CreateRepositoryRequest{Name: "api/server"}, `{"name":"api/server"}`, 201, repositoryJSON, ""},
		{"retention sends every field, null included", CreateRepositoryRequest{Name: "api/server", Retention: &RepositoryRetention{KeepLast: ptrInt(50)}},
			`{"name":"api/server","retention":{"keep_last":50,"untagged_after_days":null,"keep_pattern":null}}`, 201, repositoryJSON, ""},
		{"exists", CreateRepositoryRequest{Name: "api/server"}, `{"name":"api/server"}`, 409, `{"status":409,"code":"REPOSITORY_EXISTS"}`, CodeRepositoryExists},
		{"limit", CreateRepositoryRequest{Name: "api/server"}, `{"name":"api/server"}`, 403, `{"status":403,"code":"REPOSITORY_LIMIT_REACHED"}`, "REPOSITORY_LIMIT_REACHED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/registry/repositories" || string(body) != tt.want {
					t.Errorf("got %s %s %s, want body %s", r.Method, r.URL.Path, body, tt.want)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			repo, err := c.CreateRepository(context.Background(), tt.req)
			if tt.wantErr != "" {
				if !HasCode(err, tt.wantErr) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || repo.ID != "crr_1" || *repo.Retention.KeepLast != 50 || repo.Retention.UntaggedAfterDays != nil || repo.Retention.IsEmpty() {
				t.Fatalf("repo = %+v, err = %v", repo, err)
			}
		})
	}
}

func ptrInt(n int64) *int64 { return &n }

func TestGetRepository(t *testing.T) {
	tests := []struct {
		status       int
		body         string
		wantNotFound bool
		wantErr      bool
	}{
		{200, repositoryJSON, false, false},
		{404, `{"status":404,"code":"REPOSITORY_NOT_FOUND"}`, true, true},
		{401, `{"status":401,"code":"UNAUTHENTICATED"}`, false, true},
		{200, `[`, false, true},
	}
	for _, tt := range tests {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.EscapedPath() != "/v1/registry/repositories/crr_1" {
				t.Errorf("path = %s", r.URL.EscapedPath())
			}
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(tt.body))
		})
		_, err := c.GetRepository(context.Background(), "crr_1")
		if errors.Is(err, ErrNotFound) != tt.wantNotFound || (err != nil) != tt.wantErr {
			t.Errorf("status %d: err = %v", tt.status, err)
		}
	}
}

// A GET is retried on a 5xx; the third try answers.
func TestGetRepositoryRetries5xx(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(repositoryJSON))
	})
	if _, err := c.GetRepository(context.Background(), "crr_1"); err != nil || calls != 3 {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
}

func TestUpdateRepositoryClearsRetentionWithNulls(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPatch || string(body) != `{"visibility":"public","retention":{"keep_last":null,"untagged_after_days":null,"keep_pattern":null}}` {
			t.Errorf("got %s %s", r.Method, body)
		}
		_, _ = w.Write([]byte(repositoryJSON))
	})
	public := "public"
	if _, err := c.UpdateRepository(context.Background(), "crr_1", UpdateRepositoryRequest{Visibility: &public, Retention: &RepositoryRetention{}}); err != nil {
		t.Fatal(err)
	}
}

func TestListRepositoriesSendsQ(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/registry/repositories" || r.URL.Query().Get("q") != "api/server" {
			t.Errorf("got %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"data":[` + repositoryJSON + `],"next_cursor":null}`))
	})
	repos, err := c.ListRepositories(context.Background(), "api/server")
	if err != nil || len(repos) != 1 || repos[0].FullName != "registry.pantechdynamics.com/acme/api/server" {
		t.Fatalf("repos = %+v, err = %v", repos, err)
	}
}

func TestDeleteRepository(t *testing.T) {
	for status, wantNotFound := range map[int]bool{202: false, 404: true} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.Header.Get("Idempotency-Key") == "" {
				t.Errorf("got %s", r.Method)
			}
			w.WriteHeader(status)
			if status == 404 {
				_, _ = w.Write([]byte(`{"status":404,"code":"REPOSITORY_NOT_FOUND"}`))
			}
		})
		err := c.DeleteRepository(context.Background(), "crr_1")
		if errors.Is(err, ErrNotFound) != wantNotFound || (status == 202 && err != nil) {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
}

func TestCreateRegistryToken(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantSecret string
	}{
		{"with its secret", strings.Replace(tokenJSON, "}", `,"secret":"prt_1_abc"}`, 1), "prt_1_abc"},
		{"replay without secret", tokenJSON, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/registry/tokens" || string(body) != `{"name":"ci","permission":"push","repositories":["api/server"],"expires_at":"2027-11-10T00:00:00Z"}` {
					t.Errorf("got %s %s %s", r.Method, r.URL.Path, body)
				}
				w.WriteHeader(201)
				_, _ = w.Write([]byte(tt.body))
			})
			exp := time.Date(2027, 11, 10, 0, 0, 0, 0, time.UTC)
			tok, err := c.CreateRegistryToken(context.Background(), CreateRegistryTokenRequest{Name: "ci", Permission: "push", Repositories: []string{"api/server"}, ExpiresAt: &exp})
			if err != nil || tok.ID != "prt_1" || derefString(tok.Secret) != tt.wantSecret {
				t.Fatalf("token = %+v, err = %v", tok, err)
			}
		})
	}
}

func TestCreateRegistryTokenLeavesOptionalFieldsOut(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"name":"pull-all","permission":"pull"}` {
			t.Errorf("body = %s", body)
		}
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"status":403,"code":"REGISTRY_TOKEN_LIMIT_REACHED"}`))
	})
	if _, err := c.CreateRegistryToken(context.Background(), CreateRegistryTokenRequest{Name: "pull-all", Permission: "pull"}); !HasCode(err, "REGISTRY_TOKEN_LIMIT_REACHED") {
		t.Fatalf("err = %v", err)
	}
}

func TestListAndDeleteRegistryTokens(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[` + tokenJSON + `],"next_cursor":null}`))
		case http.MethodDelete:
			if r.URL.Path != "/v1/registry/tokens/prt_1" {
				t.Errorf("path = %s", r.URL.Path)
			}
			w.WriteHeader(204)
		}
	})
	tokens, err := c.ListRegistryTokens(context.Background())
	if err != nil || len(tokens) != 1 || tokens[0].Permission != "push" || tokens[0].ExpiresAt == nil {
		t.Fatalf("tokens = %+v, err = %v", tokens, err)
	}
	if err := c.DeleteRegistryToken(context.Background(), "prt_1"); err != nil {
		t.Fatal(err)
	}
}
