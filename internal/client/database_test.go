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

const databaseJSON = `{"id":"db_1","name":"orders","engine":"postgresql","version":"18","port":5432,"plan_id":"plan_1","data_volume_size_gb":20,"zone_id":"af-abj-2","subnet_id":"snet_1","hostname":"db-1.af-abj-2.db.pantechdynamics.com","private_ip":"10.0.1.5","admin_username":"app","desired_state":"running","observed_state":"running","generation":2,"observed_generation":2,"access_rules":[{"cidr":"10.0.0.0/16","protocol":"tcp","port":5432}],"security_group_ids":["sg_1"],"effective_access_rules":[{"cidr":"10.0.0.0/16","protocol":"tcp","port":5432,"source":"access_rule"},{"cidr":"192.168.0.0/24","protocol":"tcp","port":5432,"source":"security_group:sg_1"}],"ignored_security_group_rules":[{"security_group_id":"sg_1","direction":"ingress","protocol":"icmp","port_range":null,"cidr":"0.0.0.0/0","reason":"protocol_not_tcp"}],"failure_code":null,"created_at":"2026-10-05T10:00:00.123456Z","updated_at":"2026-10-05T10:01:00Z"}`

func TestGetDatabase(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		check  func(*testing.T, *Database, error)
	}{
		{"ok", 200, databaseJSON, func(t *testing.T, db *Database, err error) {
			if err != nil || db.ZoneID != "af-abj-2" || db.AdminUsername != "app" || len(db.AccessRules) != 1 || db.AccessRules[0].Port != 5432 || derefString(db.Hostname) == "" {
				t.Fatalf("db = %+v, err = %v", db, err)
			}
			if len(db.SecurityGroupIDs) != 1 || len(db.EffectiveAccessRules) != 2 || db.EffectiveAccessRules[1].Source != "security_group:sg_1" ||
				len(db.IgnoredSecurityGroupRules) != 1 || db.IgnoredSecurityGroupRules[0].PortRange != nil || db.IgnoredSecurityGroupRules[0].Reason != "protocol_not_tcp" {
				t.Fatalf("security group fields = %+v", db)
			}
		}},
		{"not found", 404, notFoundBody, func(t *testing.T, _ *Database, err error) {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v", err)
			}
		}},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, func(t *testing.T, _ *Database, err error) {
			if !HasCode(err, "UNAUTHENTICATED") {
				t.Fatalf("err = %v", err)
			}
		}},
		{"rate limited", 429, `{"status":429,"code":"RATE_LIMITED"}`, func(t *testing.T, _ *Database, err error) {
			if !HasCode(err, "RATE_LIMITED") {
				t.Fatalf("err = %v", err)
			}
		}},
		{"server error", 500, `{"status":500,"code":"INTERNAL"}`, func(t *testing.T, _ *Database, err error) {
			if !HasCode(err, "INTERNAL") {
				t.Fatalf("err = %v", err)
			}
		}},
		{"malformed json", 200, `{not json`, func(t *testing.T, _ *Database, err error) {
			if err == nil {
				t.Fatal("want an error")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/databases/db_1" {
					t.Errorf("path = %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			db, err := c.GetDatabase(context.Background(), "db_1")
			tt.check(t, db, err)
		})
	}
}

func TestCreateDatabaseBody(t *testing.T) {
	empty := []string{}
	tests := []struct {
		name    string
		req     CreateDatabaseRequest
		has     []string
		hasNot  []string
		wantKey bool
	}{
		{
			name:   "defaults are omitted",
			req:    CreateDatabaseRequest{Name: "orders", Engine: "postgresql", Version: "18", PlanSlug: "starter", ZoneID: "af-abj-1", Password: "s3cret-s3cret-s3cret"},
			has:    []string{`"zone_id":"af-abj-1"`, `"password":"s3cret-s3cret-s3cret"`},
			hasNot: []string{"access_rules", "subnet_id", "admin_username", "storage_gb"},
		},
		{
			name: "an empty allow-list is sent as []",
			req:  CreateDatabaseRequest{Name: "orders", Engine: "mysql", Version: "8.4", PlanSlug: "starter", ZoneID: "af-abj-2", SubnetID: "snet_1", AccessRules: &empty, AdminUsername: "app"},
			has:  []string{`"access_rules":[]`, `"subnet_id":"snet_1"`, `"admin_username":"app"`},
		},
		{
			name: "a chosen storage size is sent",
			req:  CreateDatabaseRequest{Name: "orders", Engine: "postgresql", Version: "18", PlanSlug: "starter", ZoneID: "af-abj-1", StorageGB: ptrInt64(60)},
			has:  []string{`"storage_gb":60`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/databases" || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				body, _ := io.ReadAll(r.Body)
				for _, s := range tt.has {
					if !strings.Contains(string(body), s) {
						t.Errorf("body %s lacks %s", body, s)
					}
				}
				for _, s := range tt.hasNot {
					if strings.Contains(string(body), s) {
						t.Errorf("body %s has %s", body, s)
					}
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"order_id":"ord_1","database_id":"db_1","resource_id":"db_1","status":"awaiting_payment","operation_id":null,"failure_code":null,"amount_minor":100,"currency":"NGN","password_returned":false,"password":null,"admin_username":"dbadmin"}`))
			})
			ref, err := c.CreateDatabase(context.Background(), tt.req)
			if err != nil || ref.OrderID != "ord_1" || ref.DatabaseID != "db_1" || ref.Password != nil {
				t.Fatalf("ref = %+v, err = %v", ref, err)
			}
		})
	}
}

// A refused password must not appear in the error, which ends up in Terraform's
// output and logs.
func TestDatabasePasswordNeverInErrors(t *testing.T) {
	const pw = "Very-Secret-Password-123"
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":422,"code":"VALIDATION_FAILED","errors":[{"field":"password","code":"INVALID_PASSWORD","message":"too weak"}]}`))
	})
	_, err := c.ChangeDatabasePassword(context.Background(), "db_1", pw)
	if err == nil || strings.Contains(err.Error(), pw) {
		t.Fatalf("err = %v", err)
	}
	_, err = c.CreateDatabase(context.Background(), CreateDatabaseRequest{Name: "x", Password: pw})
	if err == nil || strings.Contains(err.Error(), pw) {
		t.Fatalf("err = %v", err)
	}
}

func TestDatabaseActions(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		call   func(*Client) (*OperationReference, error)
		method string
		path   string
		body   string
	}{
		{"start", func(c *Client) (*OperationReference, error) { return c.StartDatabase(ctx, "db_1") }, http.MethodPost, "/v1/databases/db_1/start", ""},
		{"stop", func(c *Client) (*OperationReference, error) { return c.StopDatabase(ctx, "db_1") }, http.MethodPost, "/v1/databases/db_1/stop", ""},
		{"delete", func(c *Client) (*OperationReference, error) { return c.DeleteDatabase(ctx, "db_1") }, http.MethodDelete, "/v1/databases/db_1", ""},
		{"access rules", func(c *Client) (*OperationReference, error) {
			return c.ReplaceDatabaseAccessRules(ctx, "db_1", []string{"10.0.0.0/16", "192.168.1.0/24"})
		}, http.MethodPut, "/v1/databases/db_1/access-rules", `{"rules":[{"cidr":"10.0.0.0/16"},{"cidr":"192.168.1.0/24"}]}`},
		{"no access rules", func(c *Client) (*OperationReference, error) {
			return c.ReplaceDatabaseAccessRules(ctx, "db_1", nil)
		}, http.MethodPut, "/v1/databases/db_1/access-rules", `{"rules":[]}`},
		{"security groups", func(c *Client) (*OperationReference, error) {
			return c.SetDatabaseSecurityGroups(ctx, "db_1", []string{"sg_1", "sg_2"})
		}, http.MethodPut, "/v1/databases/db_1/security-groups", `{"security_group_ids":["sg_1","sg_2"]}`},
		{"detach every security group", func(c *Client) (*OperationReference, error) {
			return c.SetDatabaseSecurityGroups(ctx, "db_1", nil)
		}, http.MethodPut, "/v1/databases/db_1/security-groups", `{"security_group_ids":[]}`},
		{"password", func(c *Client) (*OperationReference, error) {
			return c.ChangeDatabasePassword(ctx, "db_1", "a-new-password-of-16")
		}, http.MethodPut, "/v1/databases/db_1/password", `{"password":"a-new-password-of-16"}`},
		{"resize storage", func(c *Client) (*OperationReference, error) {
			return c.ResizeDatabaseStorage(ctx, "db_1", 40)
		}, http.MethodPost, "/v1/databases/db_1/resize-storage", `{"storage_gb":40}`},
		{"snapshot", func(c *Client) (*OperationReference, error) {
			return c.CreateDatabaseSnapshot(ctx, "db_1", "pre-upgrade")
		}, http.MethodPost, "/v1/databases/db_1/snapshots", `{"name":"pre-upgrade"}`},
		{"delete snapshot", func(c *Client) (*OperationReference, error) {
			return c.DeleteDatabaseSnapshot(ctx, "db_1", "snap_1")
		}, http.MethodDelete, "/v1/databases/db_1/snapshots/snap_1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.Path != tt.path || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != tt.body {
					t.Errorf("body = %s, want %s", body, tt.body)
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(opRefJSON))
			})
			ref, err := tt.call(c)
			if err != nil || ref.OperationID == "" {
				t.Fatalf("ref = %+v, err = %v", ref, err)
			}
		})
	}
}

func TestListDatabasesAndEngines(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/database-engines":
			_, _ = w.Write([]byte(`{"data":[{"engine":"postgresql","display_name":"PostgreSQL","port":5432,"versions":[{"version":"18","eol_date":"2030-11-14","zones":["af-abj-1","af-abj-2"]}],"storage":[{"zone_id":"af-abj-1","min_gb":0,"min_is_plan_disk":true,"max_gb":2000,"step_gb":10,"price_per_gb_month_minor":14600,"currency":"NGN"},{"zone_id":"af-abj-2","min_gb":0,"min_is_plan_disk":true,"max_gb":2000,"step_gb":10,"price_per_gb_month_minor":null,"currency":null}]}],"next_cursor":null}`))
		case r.URL.Query().Get("cursor") == "":
			_, _ = w.Write([]byte(`{"data":[` + databaseJSON + `],"next_cursor":"p2"}`))
		default:
			_, _ = w.Write([]byte(`{"data":[{"id":"db_2"}],"next_cursor":null}`))
		}
	})
	engines, err := c.ListDatabaseEngines(context.Background())
	if err != nil || len(engines) != 1 || engines[0].Port != 5432 || len(engines[0].Versions[0].Zones) != 2 {
		t.Fatalf("engines = %+v, err = %v", engines, err)
	}
	if st := engines[0].Storage; len(st) != 2 || st[0].StepGB != 10 || st[0].MaxGB != 2000 || !st[0].MinIsPlanDisk ||
		st[0].PricePerGBMonthMinor == nil || *st[0].PricePerGBMonthMinor != 14600 || st[1].PricePerGBMonthMinor != nil || st[1].Currency != nil {
		t.Fatalf("storage = %+v", st)
	}
	dbs, err := c.ListDatabases(context.Background())
	if err != nil || len(dbs) != 2 || dbs[1].ID != "db_2" {
		t.Fatalf("dbs = %+v, err = %v", dbs, err)
	}
}

func TestWaitForDatabaseOrder(t *testing.T) {
	order := func(status, failure string) string {
		o := DatabaseOrder{ID: "ord_1", DatabaseID: "db_1", Status: status, AdminUsername: "dbadmin"}
		if failure != "" {
			o.FailureCode = &failure
		}
		if status == OrderProvisioned {
			op := "op_9"
			o.OperationID = &op
		}
		b, _ := json.Marshal(o)
		return string(b)
	}
	tests := []struct {
		name     string
		statuses []string
		failure  string
		wantErr  string
	}{
		{"provisioned after payment", []string{"awaiting_payment", "paid", "provisioning", "provisioned"}, "", ""},
		{"payment failed", []string{"awaiting_payment", "payment_failed"}, "card_declined", "card_declined"},
		{"name taken", []string{"paid", "failed"}, "database_name_taken", "returned to your credit"},
		{"payment expired", []string{"awaiting_payment", "payment_failed"}, "payment_expired", "not paid within one hour"},
		{"organization deleted before payment", []string{"awaiting_payment", "payment_failed"}, "organization_deleted", "organization was deleted"},
		{"organization deleted after payment", []string{"paid", "failed"}, "organization_deleted", "returned to credit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			polls := 0
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/database-orders/ord_1" {
					t.Errorf("path = %s", r.URL.Path)
				}
				status := tt.statuses[min(polls, len(tt.statuses)-1)]
				polls++
				failure := ""
				if status == OrderFailed || status == OrderPaymentFailed {
					failure = tt.failure
				}
				_, _ = w.Write([]byte(order(status, failure)))
			})
			got, err := c.WaitForDatabaseOrder(context.Background(), "ord_1")
			if tt.wantErr == "" {
				if err != nil || derefString(got.OperationID) != "op_9" || polls != len(tt.statuses) {
					t.Fatalf("order = %+v, err = %v, polls = %d", got, err, polls)
				}
				return
			}
			var orderErr *DatabaseOrderError
			if !errors.As(err, &orderErr) || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "ord_1") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
