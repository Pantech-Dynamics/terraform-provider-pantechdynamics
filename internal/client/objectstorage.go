package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Object storage (closed beta): S3-compatible buckets and the access keys that
// reach them.

const (
	// CodeBucketNotFound is the 404 for an unknown bucket. It counts as
	// ErrNotFound.
	CodeBucketNotFound = "BUCKET_NOT_FOUND"
	// CodeObjectStorageKeyNotFound is the 404 for an unknown access key. It
	// counts as ErrNotFound.
	CodeObjectStorageKeyNotFound = "OBJECT_STORAGE_KEY_NOT_FOUND"
	// CodeObjectStorageNotEnabled refuses every call from an organization
	// outside the closed beta.
	CodeObjectStorageNotEnabled = "OBJECT_STORAGE_NOT_ENABLED"

	// BucketActive is a bucket the store holds with every change applied.
	BucketActive = "active"
	// BucketDeleting is a deleted bucket: it no longer serves or bills, and
	// its objects are removed after a grace period.
	BucketDeleting = "deleting"
	// BucketFailed is a bucket the store could not create.
	BucketFailed = "failed"

	// ObjectLockModeGovernance allows privileged users to override retention.
	ObjectLockModeGovernance = "GOVERNANCE"
	// ObjectLockModeCompliance forbids any override until retention expires.
	ObjectLockModeCompliance = "COMPLIANCE"
)

// Bucket is an object storage bucket. QuotaBytes 0 is no limit. State is
// provisioning, active, updating, deleting, failed or unknown.
type Bucket struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Region         string     `json:"region"`
	Endpoint       string     `json:"endpoint"`
	QuotaBytes     int64      `json:"quota_bytes"`
	SizeBytes      int64      `json:"size_bytes"`
	ObjectCount    int64      `json:"object_count"`
	Versioning     bool       `json:"versioning"`
	ObjectLock     bool       `json:"object_lock"`
	PublicPrefixes []string   `json:"public_prefixes"`
	State          string     `json:"state"`
	CreatedAt      *time.Time `json:"created_at"`
	// OperationID is set on a create or update the store still has to apply.
	OperationID *string `json:"operation_id,omitempty"`
}

// ObjectStorageLimits describes per-organization limits when set.
type ObjectStorageLimits struct {
	MaxBuckets      int64 `json:"max_buckets"`
	MaxStorageBytes int64 `json:"max_storage_bytes"`
	MaxObjects      int64 `json:"max_objects"`
}

// ObjectStorageStatus reports whether object storage is enabled for the
// organization and its default endpoint and capabilities.
type ObjectStorageStatus struct {
	Enabled             bool                 `json:"enabled"`
	Endpoint            string               `json:"endpoint"`
	Beta                bool                 `json:"beta"`
	DirectUploadEnabled bool                 `json:"direct_upload_enabled"`
	Limits              *ObjectStorageLimits `json:"limits"`
}

// BucketStats holds live stored bytes and object count for a bucket.
type BucketStats struct {
	SizeBytes   int64 `json:"size_bytes"`
	ObjectCount int64 `json:"object_count"`
}

// BucketCORSRule is one cross-origin resource sharing rule on a bucket.
type BucketCORSRule struct {
	ID             string   `json:"id"`
	AllowedOrigins []string `json:"allowed_origins"`
	AllowedMethods []string `json:"allowed_methods"`
	AllowedHeaders []string `json:"allowed_headers"`
	ExposeHeaders  []string `json:"expose_headers"`
	MaxAgeSeconds  int64    `json:"max_age_seconds"`
}

type bucketCORSEnvelope struct {
	Rules []BucketCORSRule `json:"rules"`
}

// BucketLifecycleRule is one expiration or cleanup rule on a bucket.
type BucketLifecycleRule struct {
	ID                           string `json:"id"`
	Prefix                       string `json:"prefix"`
	Enabled                      bool   `json:"enabled"`
	ExpirationDays               int64  `json:"expiration_days"`
	NoncurrentExpirationDays     int64  `json:"noncurrent_expiration_days"`
	AbortIncompleteMultipartDays int64  `json:"abort_incomplete_multipart_days"`
	ExpiredObjectDeleteMarker    bool   `json:"expired_object_delete_marker"`
}

type bucketLifecycleEnvelope struct {
	Rules []BucketLifecycleRule `json:"rules"`
}

// BucketTag is one key-value tag on a bucket.
type BucketTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type bucketTagsEnvelope struct {
	Tags []BucketTag `json:"tags"`
}

// BucketObjectLock holds default retention settings for an object-lock bucket.
type BucketObjectLock struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
	Days    int64  `json:"days"`
	Years   int64  `json:"years"`
}

// CreateBucketRequest adds a bucket. An empty Region is the default one.
type CreateBucketRequest struct {
	Name           string   `json:"name"`
	Region         string   `json:"region,omitempty"`
	QuotaBytes     int64    `json:"quota_bytes,omitempty"`
	Versioning     bool     `json:"versioning"`
	ObjectLock     bool     `json:"object_lock"`
	PublicPrefixes []string `json:"public_prefixes,omitempty"`
}

// UpdateBucketRequest changes the fields that are set. PublicPrefixes, when
// set, replaces them; an empty list makes the bucket wholly private.
type UpdateBucketRequest struct {
	QuotaBytes     *int64    `json:"quota_bytes,omitempty"`
	Versioning     *bool     `json:"versioning,omitempty"`
	PublicPrefixes *[]string `json:"public_prefixes,omitempty"`
}

// ObjectStorageKey is an S3 access key. The secret is never part of it,
// except in the create response (see ObjectStorageKeyWithSecret).
type ObjectStorageKey struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Region      string     `json:"region"`
	AccessKeyID string     `json:"access_key_id"`
	ReadOnly    bool       `json:"read_only"`
	Buckets     []string   `json:"buckets"`
	CreatedAt   *time.Time `json:"created_at"`
}

// ObjectStorageKeyWithSecret is the create response.
type ObjectStorageKeyWithSecret struct {
	ObjectStorageKey
	SecretAccessKey string `json:"secret_access_key"`
	Endpoint        string `json:"endpoint"`
}

// CreateObjectStorageKeyRequest adds an access key. Empty Buckets means every
// bucket in the organization.
type CreateObjectStorageKeyRequest struct {
	Name     string   `json:"name"`
	Region   string   `json:"region,omitempty"`
	ReadOnly bool     `json:"read_only"`
	Buckets  []string `json:"buckets,omitempty"`
}

func bucketPath(id string) string { return "/buckets/" + url.PathEscape(id) }

// GetObjectStorageStatus returns the organization's object storage status.
func (c *Client) GetObjectStorageStatus(ctx context.Context) (*ObjectStorageStatus, error) {
	var st ObjectStorageStatus
	if err := c.do(ctx, http.MethodGet, "/object-storage/status", nil, &st); err != nil {
		return nil, fmt.Errorf("getting object storage status: %w", err)
	}
	return &st, nil
}

// CreateBucket adds a bucket. A replayed create answers the original, so it is
// retried on gateway errors.
func (c *Client) CreateBucket(ctx context.Context, req CreateBucketRequest) (*Bucket, error) {
	var b Bucket
	if err := c.do(ctx, http.MethodPost, "/buckets", req, &b, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating bucket: %w", err)
	}
	return &b, nil
}

// GetBucket returns one bucket, or ErrNotFound.
func (c *Client) GetBucket(ctx context.Context, id string) (*Bucket, error) {
	var b Bucket
	if err := c.do(ctx, http.MethodGet, bucketPath(id), nil, &b); err != nil {
		return nil, fmt.Errorf("getting bucket %s: %w", id, err)
	}
	return &b, nil
}

// ListBuckets returns every bucket.
func (c *Client) ListBuckets(ctx context.Context) ([]Bucket, error) {
	buckets, err := listAll[Bucket](ctx, c, "/buckets")
	if err != nil {
		return nil, fmt.Errorf("listing buckets: %w", err)
	}
	return buckets, nil
}

// UpdateBucket changes the fields set in req.
func (c *Client) UpdateBucket(ctx context.Context, id string, req UpdateBucketRequest) (*Bucket, error) {
	var b Bucket
	if err := c.do(ctx, http.MethodPatch, bucketPath(id), req, &b, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating bucket %s: %w", id, err)
	}
	return &b, nil
}

// DeleteBucket deletes a bucket. force deletes its objects too; without it a
// bucket that holds objects is refused with BUCKET_NOT_EMPTY. It returns
// ErrNotFound when the bucket is already gone, which callers treat as success.
func (c *Client) DeleteBucket(ctx context.Context, id string, force bool) error {
	path := bucketPath(id)
	if force {
		path += "?force=true"
	}
	if err := c.do(ctx, http.MethodDelete, path, nil, nil, replaySafe()); err != nil {
		return fmt.Errorf("deleting bucket %s: %w", id, err)
	}
	return nil
}

// GetBucketStats returns live stored bytes and object count for a bucket.
func (c *Client) GetBucketStats(ctx context.Context, id string) (*BucketStats, error) {
	var st BucketStats
	if err := c.do(ctx, http.MethodGet, bucketPath(id)+"/stats", nil, &st); err != nil {
		return nil, fmt.Errorf("getting stats for bucket %s: %w", id, err)
	}
	return &st, nil
}

// GetBucketCORS returns the bucket's CORS rules.
func (c *Client) GetBucketCORS(ctx context.Context, id string) ([]BucketCORSRule, error) {
	var env bucketCORSEnvelope
	if err := c.do(ctx, http.MethodGet, bucketPath(id)+"/cors", nil, &env); err != nil {
		return nil, fmt.Errorf("getting CORS rules for bucket %s: %w", id, err)
	}
	return env.Rules, nil
}

// PutBucketCORS replaces the bucket's CORS rules. It replays on the same
// Idempotency-Key.
func (c *Client) PutBucketCORS(ctx context.Context, id string, rules []BucketCORSRule) ([]BucketCORSRule, error) {
	normalized := make([]BucketCORSRule, 0, len(rules))
	for _, r := range rules {
		normalized = append(normalized, BucketCORSRule{
			ID:             r.ID,
			AllowedOrigins: nonNilStrings(r.AllowedOrigins),
			AllowedMethods: nonNilStrings(r.AllowedMethods),
			AllowedHeaders: nonNilStrings(r.AllowedHeaders),
			ExposeHeaders:  nonNilStrings(r.ExposeHeaders),
			MaxAgeSeconds:  r.MaxAgeSeconds,
		})
	}
	var env bucketCORSEnvelope
	if err := c.do(ctx, http.MethodPut, bucketPath(id)+"/cors", bucketCORSEnvelope{Rules: normalized}, &env, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating CORS rules for bucket %s: %w", id, err)
	}
	return env.Rules, nil
}

// GetBucketLifecycle returns the bucket's lifecycle rules.
func (c *Client) GetBucketLifecycle(ctx context.Context, id string) ([]BucketLifecycleRule, error) {
	var env bucketLifecycleEnvelope
	if err := c.do(ctx, http.MethodGet, bucketPath(id)+"/lifecycle", nil, &env); err != nil {
		return nil, fmt.Errorf("getting lifecycle rules for bucket %s: %w", id, err)
	}
	return env.Rules, nil
}

// PutBucketLifecycle replaces the bucket's lifecycle rules. It replays on the
// same Idempotency-Key.
func (c *Client) PutBucketLifecycle(ctx context.Context, id string, rules []BucketLifecycleRule) ([]BucketLifecycleRule, error) {
	if rules == nil {
		rules = []BucketLifecycleRule{}
	}
	var env bucketLifecycleEnvelope
	if err := c.do(ctx, http.MethodPut, bucketPath(id)+"/lifecycle", bucketLifecycleEnvelope{Rules: rules}, &env, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating lifecycle rules for bucket %s: %w", id, err)
	}
	return env.Rules, nil
}

// GetBucketTags returns the key-value tags set on a bucket.
func (c *Client) GetBucketTags(ctx context.Context, id string) ([]BucketTag, error) {
	var env bucketTagsEnvelope
	if err := c.do(ctx, http.MethodGet, bucketPath(id)+"/tags", nil, &env); err != nil {
		return nil, fmt.Errorf("getting tags for bucket %s: %w", id, err)
	}
	return env.Tags, nil
}

// PutBucketTags replaces the key-value tags on a bucket. It replays on the same
// Idempotency-Key.
func (c *Client) PutBucketTags(ctx context.Context, id string, tags []BucketTag) ([]BucketTag, error) {
	if tags == nil {
		tags = []BucketTag{}
	}
	var env bucketTagsEnvelope
	if err := c.do(ctx, http.MethodPut, bucketPath(id)+"/tags", bucketTagsEnvelope{Tags: tags}, &env, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating tags for bucket %s: %w", id, err)
	}
	return env.Tags, nil
}

// GetBucketObjectLock returns default retention settings for an object-lock
// bucket.
func (c *Client) GetBucketObjectLock(ctx context.Context, id string) (*BucketObjectLock, error) {
	var cfg BucketObjectLock
	if err := c.do(ctx, http.MethodGet, bucketPath(id)+"/object-lock", nil, &cfg); err != nil {
		return nil, fmt.Errorf("getting object lock for bucket %s: %w", id, err)
	}
	return &cfg, nil
}

// PutBucketObjectLock sets default retention settings for an object-lock
// bucket. It replays on the same Idempotency-Key.
func (c *Client) PutBucketObjectLock(ctx context.Context, id string, cfg BucketObjectLock) (*BucketObjectLock, error) {
	var out BucketObjectLock
	if err := c.do(ctx, http.MethodPut, bucketPath(id)+"/object-lock", cfg, &out, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating object lock for bucket %s: %w", id, err)
	}
	return &out, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ListObjectStorageKeys returns every live access key, without secrets.
func (c *Client) ListObjectStorageKeys(ctx context.Context) ([]ObjectStorageKey, error) {
	keys, err := listAll[ObjectStorageKey](ctx, c, "/object-storage/keys")
	if err != nil {
		return nil, fmt.Errorf("listing object storage keys: %w", err)
	}
	return keys, nil
}

// CreateObjectStorageKey adds an access key. It is not retried: the secret is
// in the first response only.
func (c *Client) CreateObjectStorageKey(ctx context.Context, req CreateObjectStorageKeyRequest) (*ObjectStorageKeyWithSecret, error) {
	var k ObjectStorageKeyWithSecret
	if err := c.do(ctx, http.MethodPost, "/object-storage/keys", req, &k); err != nil {
		return nil, fmt.Errorf("creating object storage key: %w", err)
	}
	return &k, nil
}

// DeleteObjectStorageKey revokes an access key. It returns ErrNotFound when
// the key is already gone, which callers treat as success.
func (c *Client) DeleteObjectStorageKey(ctx context.Context, id string) error {
	if err := c.do(ctx, http.MethodDelete, "/object-storage/keys/"+url.PathEscape(id), nil, nil, replaySafe()); err != nil {
		return fmt.Errorf("revoking object storage key %s: %w", id, err)
	}
	return nil
}
