package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const teamJSON = `{"id":"team_1","name":"Platform","description":"","member_count":4,"policy_ids":["pol_pantech_developer"],"created_at":"2026-11-10T09:00:00Z","updated_at":"2026-11-10T09:00:00Z","version":2}`

const policyJSON = `{"id":"pol_1","name":"staging-operators","description":"","managed":false,"statements":[{"sid":"RestartStaging","effect":"allow","actions":["instances:Read","instances:Stop"],"resources":["name:staging-*"],"conditions":{"source_ip":["102.89.0.0/16"],"mfa":false}}],"attachment_count":3,"created_at":"2026-11-10T09:00:00Z","updated_at":"2026-11-10T09:00:00Z","version":5}`

func TestCreateIAMTeam(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"created", 201, teamJSON, ""},
		{"taken", 409, `{"status":409,"code":"IAM_TEAM_NAME_TAKEN"}`, "IAM_TEAM_NAME_TAKEN"},
		{"limit", 403, `{"status":403,"code":"IAM_TEAM_LIMIT_REACHED"}`, "IAM_TEAM_LIMIT_REACHED"},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, "UNAUTHENTICATED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/iam/teams" || string(body) != `{"name":"Platform"}` || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s %s", r.Method, r.URL.Path, body)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			team, err := c.CreateIAMTeam(context.Background(), CreateIAMTeamRequest{Name: "Platform"})
			if tt.wantErr != "" {
				if !HasCode(err, tt.wantErr) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || team.ID != "team_1" || team.MemberCount != 4 {
				t.Fatalf("team = %+v, err = %v", team, err)
			}
		})
	}
}

func TestGetIAMTeamNotFoundAndMalformed(t *testing.T) {
	for body, wantNotFound := range map[string]bool{`{"status":404,"code":"IAM_TEAM_NOT_FOUND"}`: true, `{"id":`: false} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			if strings.Contains(body, "404") {
				w.WriteHeader(404)
			}
			_, _ = w.Write([]byte(body))
		})
		_, err := c.GetIAMTeam(context.Background(), "team_1")
		if err == nil || errors.Is(err, ErrNotFound) != wantNotFound {
			t.Errorf("body %s: err = %v", body, err)
		}
	}
}

// Adding a member is a PUT with no body, retried with the same key on a
// gateway error.
func TestAddIAMTeamMemberSendsNoBody(t *testing.T) {
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPut || r.URL.Path != "/v1/iam/teams/team_1/members/usr_1" || len(body) != 0 || r.Header.Get("Content-Type") != "" {
			t.Errorf("got %s %s body=%q", r.Method, r.URL.Path, body)
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"user_id":"usr_1","email":"ada@example.com","name":"Ada","role":"member","added_at":"2026-11-10T09:00:00Z"}`))
	})
	m, err := c.AddIAMTeamMember(context.Background(), "team_1", "usr_1")
	if err != nil || m.Role != "member" || len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("member = %+v, err = %v, keys = %v", m, err, keys)
	}
}

func TestListIAMTeamMembersFollowsTheCursor(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"data":[{"user_id":"usr_1"}],"next_cursor":"c2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"user_id":"usr_2"}],"next_cursor":null}`))
	})
	members, err := c.ListIAMTeamMembers(context.Background(), "team_1")
	if err != nil || len(members) != 2 || members[1].UserID != "usr_2" {
		t.Fatalf("members = %+v, err = %v", members, err)
	}
}

func TestCreateIAMPolicyAndInvalidPolicyFields(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		want := `{"name":"p","description":"","statements":[{"effect":"allow","actions":["instances:Stop"],"resources":["*"]}]}`
		if string(body) != want {
			t.Errorf("body = %s\nwant   %s", body, want)
		}
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"status":422,"code":"INVALID_POLICY","errors":[{"field":"statements[0].actions[0]","code":"INVALID_ACTION","message":"Unknown action"}]}`))
	})
	_, err := c.CreateIAMPolicy(context.Background(), IAMPolicyRequest{Name: "p", Statements: []PolicyStatement{{Effect: "allow", Actions: []string{"instances:Stop"}, Resources: []string{"*"}}}})
	if !HasFieldCode(err, "statements[0].actions[0]", "INVALID_ACTION") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateIAMPolicySendsIfMatch(t *testing.T) {
	for status, wantErr := range map[int]string{200: "", 412: CodeVersionConflict, 409: CodeIAMManagedPolicy} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut || r.URL.Path != "/v1/iam/policies/pol_1" || r.Header.Get("If-Match") != "5" {
				t.Errorf("got %s %s If-Match=%q", r.Method, r.URL.Path, r.Header.Get("If-Match"))
			}
			w.WriteHeader(status)
			switch status {
			case 200:
				_, _ = w.Write([]byte(policyJSON))
			case 412:
				_, _ = w.Write([]byte(`{"status":412,"code":"VERSION_CONFLICT"}`))
			default:
				_, _ = w.Write([]byte(`{"status":409,"code":"IAM_MANAGED_POLICY"}`))
			}
		})
		p, err := c.UpdateIAMPolicy(context.Background(), "pol_1", 5, IAMPolicyRequest{Name: "p"})
		if wantErr == "" {
			if err != nil || p.Statements[0].Conditions == nil || *p.Statements[0].Conditions.MFA || p.Version != 5 {
				t.Fatalf("policy = %+v, err = %v", p, err)
			}
			continue
		}
		if !HasCode(err, wantErr) {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
}

func TestListIAMPoliciesManagedFilter(t *testing.T) {
	for _, tt := range []struct {
		managed *bool
		want    string
	}{{nil, ""}, {ptrBool(true), "true"}, {ptrBool(false), "false"}} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("managed") != tt.want {
				t.Errorf("managed = %q, want %q", r.URL.Query().Get("managed"), tt.want)
			}
			_, _ = w.Write([]byte(`{"data":[` + policyJSON + `],"next_cursor":null}`))
		})
		if ps, err := c.ListIAMPolicies(context.Background(), tt.managed); err != nil || len(ps) != 1 {
			t.Fatalf("policies = %+v, err = %v", ps, err)
		}
	}
}

func ptrBool(b bool) *bool { return &b }

func TestAttachAndDetachIAMPolicy(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			if r.URL.Path != "/v1/iam/policies/pol_pantech_developer/attachments" || string(body) != `{"principal_type":"team","principal_id":"team_1"}` {
				t.Errorf("got %s %s", r.URL.Path, body)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"policy_id":"pol_pantech_developer","principal_type":"team","principal_id":"team_1","attached_at":"2026-11-10T09:00:00Z"}`))
		case http.MethodDelete:
			if r.URL.Path != "/v1/iam/policies/pol_pantech_developer/attachments/team/team_1" {
				t.Errorf("path = %s", r.URL.Path)
			}
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"status":404,"code":"IAM_ATTACHMENT_NOT_FOUND"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"policy_id":"pol_pantech_developer","principal_type":"team","principal_id":"team_1"}]}`))
		}
	})
	if _, err := c.AttachIAMPolicy(context.Background(), "pol_pantech_developer", "team", "team_1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DetachIAMPolicy(context.Background(), "pol_pantech_developer", "team", "team_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if as, err := c.ListIAMPolicyAttachments(context.Background(), "pol_pantech_developer"); err != nil || len(as) != 1 {
		t.Fatalf("attachments = %+v, err = %v", as, err)
	}
}

func TestAccessSettingsAndActions(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/iam/actions":
			_, _ = w.Write([]byte(`{"data":[{"action":"instances:Stop","service":"instances","description":"Stop.","resource_types":["instance"],"read":false}]}`))
		case r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"member_baseline":"none"}` {
				t.Errorf("body = %s", body)
			}
			_, _ = w.Write([]byte(`{"member_baseline":"none","version":2,"updated_at":"2026-11-10T09:00:00Z"}`))
		default:
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"status":429,"code":"RATE_LIMITED"}`))
		}
	})
	if a, err := c.ListIAMActions(context.Background()); err != nil || len(a) != 1 || a[0].Read {
		t.Fatalf("actions = %+v, err = %v", a, err)
	}
	if s, err := c.SetAccessSettings(context.Background(), "none"); err != nil || s.MemberBaseline != "none" || s.Version != 2 {
		t.Fatalf("settings = %+v, err = %v", s, err)
	}
	if _, err := c.GetAccessSettings(context.Background()); !HasCode(err, "RATE_LIMITED") {
		t.Fatalf("err = %v", err)
	}
}
