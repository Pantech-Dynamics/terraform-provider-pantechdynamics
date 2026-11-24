package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

const bucketJSON = `{"id":"bkt_1","name":"uploads","region":"af-abj","endpoint":"https://s3.af-abj.pantechdynamics.com","quota_bytes":0,"size_bytes":4096,"object_count":12,"versioning":false,"object_lock":false,"public_prefixes":[],"state":"active","created_at":"2026-11-20T09:00:00Z"}`

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
			if err != nil || b.Name != "uploads" || b.State != BucketActive || b.CreatedAt == nil || b.SizeBytes != 4096 || b.ObjectCount != 12 {
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

func TestObjectStorageStatusAndBucketRules(t *testing.T) {
	ctx := context.Background()

	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/object-storage/status":
			_, _ = w.Write([]byte(`{"enabled":true,"endpoint":"https://s3.af-abj.pantechdynamics.com","beta":true,"direct_upload_enabled":true,"limits":{"max_buckets":20,"max_storage_bytes":1099511627776,"max_objects":100000}}`))
		case "GET /v1/buckets/bkt_1/stats":
			_, _ = w.Write([]byte(`{"size_bytes":8192,"object_count":4}`))
		case "GET /v1/buckets/bkt_1/cors":
			_, _ = w.Write([]byte(`{"rules":[{"id":"web","allowed_origins":["https://app.example.com"],"allowed_methods":["GET","PUT"],"allowed_headers":["*"],"expose_headers":["ETag"],"max_age_seconds":3600}]}`))
		case "PUT /v1/buckets/bkt_1/cors":
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("missing Idempotency-Key on PUT cors")
			}
			body, _ := io.ReadAll(r.Body)
			_, _ = w.Write(body)
		case "GET /v1/buckets/bkt_1/lifecycle":
			_, _ = w.Write([]byte(`{"rules":[{"id":"logs","prefix":"logs/","enabled":true,"expiration_days":30,"noncurrent_expiration_days":7,"abort_incomplete_multipart_days":3,"expired_object_delete_marker":false}]}`))
		case "PUT /v1/buckets/bkt_1/lifecycle":
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("missing Idempotency-Key on PUT lifecycle")
			}
			body, _ := io.ReadAll(r.Body)
			_, _ = w.Write(body)
		case "GET /v1/buckets/bkt_1/tags":
			_, _ = w.Write([]byte(`{"tags":[{"key":"env","value":"prod"}]}`))
		case "PUT /v1/buckets/bkt_1/tags":
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("missing Idempotency-Key on PUT tags")
			}
			body, _ := io.ReadAll(r.Body)
			_, _ = w.Write(body)
		case "GET /v1/buckets/bkt_1/object-lock":
			_, _ = w.Write([]byte(`{"enabled":true,"mode":"GOVERNANCE","days":30,"years":0}`))
		case "PUT /v1/buckets/bkt_1/object-lock":
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("missing Idempotency-Key on PUT object-lock")
			}
			body, _ := io.ReadAll(r.Body)
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	st, err := c.GetObjectStorageStatus(ctx)
	if err != nil || !st.Enabled || !st.DirectUploadEnabled || st.Limits == nil || st.Limits.MaxBuckets != 20 {
		t.Fatalf("status = %+v, err = %v", st, err)
	}

	stats, err := c.GetBucketStats(ctx, "bkt_1")
	if err != nil || stats.SizeBytes != 8192 || stats.ObjectCount != 4 {
		t.Fatalf("stats = %+v, err = %v", stats, err)
	}

	corsRules, err := c.GetBucketCORS(ctx, "bkt_1")
	if err != nil || len(corsRules) != 1 || corsRules[0].ID != "web" || corsRules[0].MaxAgeSeconds != 3600 {
		t.Fatalf("cors = %+v, err = %v", corsRules, err)
	}
	putCORS, err := c.PutBucketCORS(ctx, "bkt_1", []BucketCORSRule{{ID: "r1", AllowedOrigins: []string{"*"}, AllowedMethods: []string{"GET"}}})
	if err != nil || len(putCORS) != 1 || putCORS[0].AllowedHeaders == nil || putCORS[0].ExposeHeaders == nil {
		t.Fatalf("putCORS = %+v, err = %v", putCORS, err)
	}

	lcRules, err := c.GetBucketLifecycle(ctx, "bkt_1")
	if err != nil || len(lcRules) != 1 || lcRules[0].ID != "logs" || lcRules[0].ExpirationDays != 30 {
		t.Fatalf("lifecycle = %+v, err = %v", lcRules, err)
	}
	putLC, err := c.PutBucketLifecycle(ctx, "bkt_1", nil)
	if err != nil || putLC == nil || len(putLC) != 0 {
		t.Fatalf("putLC = %+v, err = %v", putLC, err)
	}

	tags, err := c.GetBucketTags(ctx, "bkt_1")
	if err != nil || len(tags) != 1 || tags[0].Key != "env" || tags[0].Value != "prod" {
		t.Fatalf("tags = %+v, err = %v", tags, err)
	}
	putTags, err := c.PutBucketTags(ctx, "bkt_1", []BucketTag{{Key: "team", Value: "platform"}})
	if err != nil || len(putTags) != 1 || putTags[0].Key != "team" {
		t.Fatalf("putTags = %+v, err = %v", putTags, err)
	}

	ol, err := c.GetBucketObjectLock(ctx, "bkt_1")
	if err != nil || !ol.Enabled || ol.Mode != ObjectLockModeGovernance || ol.Days != 30 {
		t.Fatalf("object-lock = %+v, err = %v", ol, err)
	}
	putOL, err := c.PutBucketObjectLock(ctx, "bkt_1", BucketObjectLock{Enabled: true, Mode: ObjectLockModeCompliance, Years: 1})
	if err != nil || !putOL.Enabled || putOL.Mode != ObjectLockModeCompliance || putOL.Years != 1 {
		t.Fatalf("putOL = %+v, err = %v", putOL, err)
	}
}
