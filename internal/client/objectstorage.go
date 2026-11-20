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
)

// Bucket is an object storage bucket. QuotaBytes 0 is no limit. State is
// provisioning, active, updating, deleting, failed or unknown.
type Bucket struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Region         string     `json:"region"`
	Endpoint       string     `json:"endpoint"`
	QuotaBytes     int64      `json:"quota_bytes"`
	Versioning     bool       `json:"versioning"`
	ObjectLock     bool       `json:"object_lock"`
	PublicPrefixes []string   `json:"public_prefixes"`
	State          string     `json:"state"`
	CreatedAt      *time.Time `json:"created_at"`
	// OperationID is set on a create or update the store still has to apply.
	OperationID *string `json:"operation_id,omitempty"`
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
