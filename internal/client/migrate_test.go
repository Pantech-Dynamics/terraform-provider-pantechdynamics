package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const cloudConnectionJSON = `{"id":"ccn_1","provider":"gcp","name":"Production","status":"%s","failure":null,"role_arn":null,"aws_account_id":null,
"external_account_id":"123456789012","gcp":{"project_number":"123456789012","pool_id":"pantech-migrate","provider_id":"ccn-1","service_account_email":"sa@web.iam.gserviceaccount.com","project_ids":["web"]},
"azure":null,"huawei":null,"agent":null,"verified_at":null,"created_at":"2026-11-25T10:00:00Z","updated_at":"2026-11-25T10:00:00Z",
"setup":{"access":"federated","issuer_url":"https://id.example.test","audience":"pantech-migrate","subject":"ccn_1","quick_create_url":null,"template":"gcloud ...","template_format":"shell","files":[{"name":"main.tf","content":"terraform {}"}],"permissions":["compute.instances.list"],"agent":null}}`

func connectionBody(status string) string {
	return strings.Replace(cloudConnectionJSON, "%s", status, 1)
}

func TestGetCloudConnection(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr func(error) bool
	}{
		{"found", 200, connectionBody("connected"), nil},
		{"not found", 404, `{"status":404,"code":"CLOUD_CONNECTION_NOT_FOUND"}`, func(e error) bool { return errors.Is(e, ErrNotFound) }},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, func(e error) bool { return HasCode(e, "UNAUTHENTICATED") }},
		{"migrate unavailable", 400, `{"status":400,"code":"MIGRATE_UNAVAILABLE"}`, func(e error) bool { return HasCode(e, CodeMigrateUnavailable) }},
		{"malformed json", 200, `{"id":`, func(e error) bool { return e != nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/cloud-connections/ccn_1" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			conn, err := c.GetCloudConnection(context.Background(), "ccn_1")
			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || conn.Provider != "gcp" || conn.GCP == nil || conn.GCP.ProjectIDs[0] != "web" || conn.Setup.Files[0].Name != "main.tf" || conn.CreatedAt == nil {
				t.Fatalf("connection = %+v, err = %v", conn, err)
			}
		})
	}
}

func TestCreateAndUpdateCloudConnectionSendTheBody(t *testing.T) {
	var bodies []map[string]any
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		bodies = append(bodies, got)
		if r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("%s %s without an Idempotency-Key", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(connectionBody("awaiting_role")))
	})
	if _, err := c.CreateCloudConnection(context.Background(), CreateCloudConnectionRequest{Provider: "gcp", Name: "Production"}); err != nil {
		t.Fatal(err)
	}
	fed := &CloudAzureFederation{TenantID: "t", ClientID: "c", SubscriptionIDs: []string{"s"}}
	if _, err := c.UpdateCloudConnection(context.Background(), "ccn_1", UpdateCloudConnectionRequest{Azure: fed}); err != nil {
		t.Fatal(err)
	}
	if bodies[0]["provider"] != "gcp" || bodies[0]["name"] != "Production" {
		t.Fatalf("create body %v", bodies[0])
	}
	az, ok := bodies[1]["azure"].(map[string]any)
	if len(bodies[1]) != 1 || !ok || az["tenant_id"] != "t" {
		t.Fatalf("update body %v", bodies[1])
	}
}

func TestWaitForCloudConnectionCheck(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		status := "verifying"
		if n.Add(1) >= 2 {
			status = "connected"
		}
		_, _ = w.Write([]byte(connectionBody(status)))
	})
	conn, err := c.WaitForCloudConnectionCheck(context.Background(), "ccn_1")
	if err != nil || conn.Status != CloudConnectionConnected || n.Load() != 2 {
		t.Fatalf("conn %+v err %v after %d polls", conn, err, n.Load())
	}
}

func TestListCloudProviders(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cloud-providers" {
			t.Errorf("got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"slug":"aws","name":"AWS","access":"federated","cost_readable":true,"people_readable":true,"available":true},{"slug":"vultr","name":"Vultr","access":"agent","cost_readable":true,"people_readable":false,"available":false}],"next_cursor":null}`))
	})
	list, err := c.ListCloudProviders(context.Background())
	if err != nil || len(list) != 2 || list[1].Access != CloudAccessAgent || list[1].Available {
		t.Fatalf("list %+v err %v", list, err)
	}
}
