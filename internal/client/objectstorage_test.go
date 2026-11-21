package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

const bucketJSON = `{"id":"bkt_1","name":"uploads","region":"af-abj","endpoint":"https://s3.af-abj.pantechdynamics.com","quota_bytes":0,"versioning":false,"object_lock":false,"public_prefixes":[],"state":"active","created_at":"2026-11-20T09:00:00Z"}`

func TestGetBucket(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr func(error) bool
	}{
		{"found", 200, bucketJSON, nil},
		{"not found", 404, `{"status":404,"code":"BUCKET_NOT_FOUND"}`, func(e error) bool { return errors.Is(e, ErrNotFound) }},
		{"not in the beta", 403, `{"status":403,"code":"OBJECT_STORAGE_NOT_ENABLED"}`, func(e error) bool {
			return HasCode(e, CodeObjectStorageNotEnabled) && !errors.Is(e, ErrNotFound)
		}},
		{"unauthenticated", 401, `{"status":401,"code":"UNAUTHENTICATED"}`, func(e error) bool { return HasCode(e, "UNAUTHENTICATED") }},
		{"malformed json", 200, `{"id":`, func(e error) bool { return e != nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/buckets/bkt_1" {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			b, err := c.GetBucket(context.Background(), "bkt_1")
			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || b.Name != "uploads" || b.State != BucketActive || b.CreatedAt == nil {
				t.Fatalf("bucket = %+v, err = %v", b, err)
			}
		})
	}
}

func TestCreateBucketSendsTheBodyAndAKey(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/buckets" || r.Header.Get("Idempotency-Key") == "" ||
			got["name"] != "uploads" || got["quota_bytes"] != float64(1<<30) || got["versioning"] != true || got["region"] != nil {
			t.Errorf("got %s %s %s", r.Method, r.URL.Path, body)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(bucketJSON[:len(bucketJSON)-1] + `,"operation_id":"op_1"}`))
	})
	b, err := c.CreateBucket(context.Background(), CreateBucketRequest{Name: "uploads", QuotaBytes: 1 << 30, Versioning: true})
	if err != nil || b.OperationID == nil || *b.OperationID != "op_1" {
		t.Fatalf("bucket = %+v, err = %v", b, err)
	}
}

func TestDeleteBucketForce(t *testing.T) {
	for _, force := range []bool{false, true} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			want := ""
			if force {
				want = "force=true"
			}
			if r.Method != http.MethodDelete || r.URL.Path != "/v1/buckets/bkt_1" || r.URL.RawQuery != want {
				t.Errorf("got %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"operation_id":"op_2","resource_id":"bkt_1","status":"submitted"}`))
		})
		if err := c.DeleteBucket(context.Background(), "bkt_1", force); err != nil {
			t.Fatal(err)
		}
	}
}

func TestObjectStorageKeys(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/object-storage/keys":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"osk_1","name":"app","region":"af-abj","access_key_id":"AK1","read_only":true,"buckets":["uploads"],"created_at":"2026-11-20T09:00:00Z","secret_access_key":"s3cr3t","endpoint":"https://s3.af-abj.pantechdynamics.com"}`))
		case "GET /v1/object-storage/keys":
			_, _ = w.Write([]byte(`{"data":[{"id":"osk_1","name":"app","region":"af-abj","access_key_id":"AK1","read_only":true,"buckets":["uploads"],"created_at":"2026-11-20T09:00:00Z"}]}`))
		case "DELETE /v1/object-storage/keys/osk_1":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"status":404,"code":"OBJECT_STORAGE_KEY_NOT_FOUND"}`))
		default:
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()
	k, err := c.CreateObjectStorageKey(ctx, CreateObjectStorageKeyRequest{Name: "app", ReadOnly: true, Buckets: []string{"uploads"}})
	if err != nil || k.SecretAccessKey != "s3cr3t" || k.AccessKeyID != "AK1" {
		t.Fatalf("key = %+v, err = %v", k, err)
	}
	keys, err := c.ListObjectStorageKeys(ctx)
	if err != nil || len(keys) != 1 || !keys[0].ReadOnly {
		t.Fatalf("keys = %+v, err = %v", keys, err)
	}
	if err := c.DeleteObjectStorageKey(ctx, "osk_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
