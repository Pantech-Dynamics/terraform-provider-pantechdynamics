package client

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func ptrInt64(n int64) *int64 { return &n }

const databaseSnapshotJSON = `{"id":"snap_1","name":"pre-upgrade","instance_id":null,"volume_id":null,"database_id":"db_1","trigger":"manual","size_bytes":0,"desired_state":"present","observed_state":"pending","created_at":"2026-10-05T10:00:00Z"}`

func TestDatabaseStorageAndSnapshotReads(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/databases/db_1":
			_, _ = w.Write([]byte(`{"id":"db_1","data_volume_size_gb":20,"pending_data_volume_size_gb":40}`))
		case "/v1/databases/db_1/snapshots/snap_1":
			_, _ = w.Write([]byte(databaseSnapshotJSON))
		case "/v1/databases/db_1/snapshots":
			if r.URL.Query().Get("cursor") == "" {
				_, _ = w.Write([]byte(`{"data":[` + databaseSnapshotJSON + `],"next_cursor":"p2"}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"snap_2","database_id":"db_1"}],"next_cursor":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
		}
	})
	ctx := context.Background()
	db, err := c.GetDatabase(ctx, "db_1")
	if err != nil || db.DataVolumeSizeGB != 20 || db.PendingDataVolumeSizeGB == nil || *db.PendingDataVolumeSizeGB != 40 {
		t.Fatalf("db = %+v, err = %v", db, err)
	}
	snap, err := c.GetDatabaseSnapshot(ctx, "db_1", "snap_1")
	if err != nil || snap.DatabaseID == nil || *snap.DatabaseID != "db_1" || snap.InstanceID != nil {
		t.Fatalf("snap = %+v, err = %v", snap, err)
	}
	snaps, err := c.ListDatabaseSnapshots(ctx, "db_1")
	if err != nil || len(snaps) != 2 {
		t.Fatalf("snaps = %+v, err = %v: every page must be read", snaps, err)
	}
	if _, err := c.GetDatabaseSnapshot(ctx, "db_1", "snap_gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// The API's own message must reach the user, with each field error.
func TestStorageResizeRefusalCarriesTheAPIMessage(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":422,"code":"VALIDATION_FAILED","detail":"The request is invalid.","request_id":"req_9","errors":[{"field":"storage_gb","code":"INVALID_DATABASE_STORAGE","message":"must be larger than 40 GB"}]}`))
	})
	_, err := c.ResizeDatabaseStorage(context.Background(), "db_1", 30)
	if !HasFieldCode(err, "storage_gb", "INVALID_DATABASE_STORAGE") {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"The request is invalid.", "must be larger than 40 GB", "req_9"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestInstancePrivateNetwork(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name, method string
		call         func(*Client) (*OperationReference, error)
	}{
		{"attach", http.MethodPost, func(c *Client) (*OperationReference, error) { return c.AttachInstancePrivateNetwork(ctx, "vm_1") }},
		{"detach", http.MethodDelete, func(c *Client) (*OperationReference, error) { return c.DetachInstancePrivateNetwork(ctx, "vm_1") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.Path != "/v1/instances/vm_1/private-network" || r.Header.Get("Idempotency-Key") == "" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(opRefJSON))
			})
			if ref, err := tt.call(c); err != nil || ref.OperationID == "" {
				t.Fatalf("ref = %+v, err = %v", ref, err)
			}
		})
	}

	t.Run("a permissive security group is refused with the API's message", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"status":409,"code":"SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK","detail":"Security group web allows 0.0.0.0/0 on tcp 22; narrow it to leave out 10.250.0.0/20.","request_id":"req_2"}`))
		})
		_, err := c.AttachInstancePrivateNetwork(ctx, "vm_1")
		if !HasCode(err, CodeSecurityGroupAllowsPrivateNetwork) || !contains(err.Error(), "narrow it to leave out 10.250.0.0/20") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("the instance carries the interface state and address", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":"vm_1","private_network_state":"attached","private_network_ip":"10.250.0.7"}`))
		})
		inst, err := c.GetInstance(ctx, "vm_1")
		if err != nil || inst.PrivateNetworkState != PrivateNetworkAttached || inst.PrivateNetworkIP == nil || *inst.PrivateNetworkIP != "10.250.0.7" {
			t.Fatalf("inst = %+v, err = %v", inst, err)
		}
	})
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
