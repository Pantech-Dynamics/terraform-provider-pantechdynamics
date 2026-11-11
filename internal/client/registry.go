package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the container registry endpoints return.
const (
	// CodeRegistryUnavailable is the 503 when the registry is not offered on
	// this deployment.
	CodeRegistryUnavailable = "REGISTRY_UNAVAILABLE"

	// CodeRegistryNamespaceNotSet is the 404 of GET /registry until the
	// organization claims a namespace. It is deliberately not ErrNotFound:
	// it means "claim one first", and only the namespace resource treats it
	// as gone.
	CodeRegistryNamespaceNotSet = "REGISTRY_NAMESPACE_NOT_SET"

	// CodeRegistryNamespaceTaken is the 409 when another organization holds
	// the namespace.
	CodeRegistryNamespaceTaken = "REGISTRY_NAMESPACE_TAKEN"

	// CodeRegistryNamespaceLocked is the 409 for a rename once a repository
	// exists.
	CodeRegistryNamespaceLocked = "REGISTRY_NAMESPACE_LOCKED"

	// CodeRepositoryNotFound is the 404 for a repository that is not in the
	// namespace. It counts as ErrNotFound.
	CodeRepositoryNotFound = "REPOSITORY_NOT_FOUND"

	// CodeRepositoryExists is the 409 for a repository name already in use.
	CodeRepositoryExists = "REPOSITORY_EXISTS"

	// CodeRegistryTokenNotFound is the 404 for an unknown registry token. It
	// counts as ErrNotFound.
	CodeRegistryTokenNotFound = "REGISTRY_TOKEN_NOT_FOUND"

	// codeTagNotFound is the 404 for an unknown tag. It counts as ErrNotFound.
	codeTagNotFound = "TAG_NOT_FOUND"
)

// Repository statuses.
const (
	RepositoryActive    = "active"
	RepositoryDeleting  = "deleting"
	RepositorySuspended = "suspended"
)

// RegistryNamespace is the organization's one namespace in the registry.
type RegistryNamespace struct {
	Name              string     `json:"name"`
	RegistryHost      string     `json:"registry_host"`
	CreateOnPush      bool       `json:"create_on_push"`
	RepositoryCount   int64      `json:"repository_count"`
	StorageBytes      int64      `json:"storage_bytes"`
	StorageLimitBytes int64      `json:"storage_limit_bytes"`
	RepositoryLimit   int64      `json:"repository_limit"`
	CreatedAt         *time.Time `json:"created_at"`
	UpdatedAt         *time.Time `json:"updated_at"`
	Version           int64      `json:"version"`
}

// SetRegistryNamespaceRequest claims or changes the namespace. CreateOnPush
// is a pointer so false is sent, not dropped.
type SetRegistryNamespaceRequest struct {
	Name         string `json:"name"`
	CreateOnPush *bool  `json:"create_on_push,omitempty"`
}

// RepositoryRetention is a repository's cleanup policy. A nil field is no
// rule. The fields have no omitempty on purpose: null is how a rule is
// removed, so every field is always sent.
type RepositoryRetention struct {
	KeepLast          *int64  `json:"keep_last"`
	UntaggedAfterDays *int64  `json:"untagged_after_days"`
	KeepPattern       *string `json:"keep_pattern"`
}

// IsEmpty reports whether the policy has no rule at all.
func (r *RepositoryRetention) IsEmpty() bool {
	return r == nil || (r.KeepLast == nil && r.UntaggedAfterDays == nil && r.KeepPattern == nil)
}

// Repository is one repository in the namespace.
type Repository struct {
	ID              string               `json:"id"`
	Namespace       string               `json:"namespace"`
	Name            string               `json:"name"`
	FullName        string               `json:"full_name"`
	Description     string               `json:"description"`
	Visibility      string               `json:"visibility"`
	ImmutableTags   bool                 `json:"immutable_tags"`
	Retention       *RepositoryRetention `json:"retention"`
	TagCount        int64                `json:"tag_count"`
	StorageBytes    int64                `json:"storage_bytes"`
	LastPushedAt    *time.Time           `json:"last_pushed_at"`
	Status          string               `json:"status"`
	SuspendedReason *string              `json:"suspended_reason"`
	CreatedAt       *time.Time           `json:"created_at"`
	UpdatedAt       *time.Time           `json:"updated_at"`
	Version         int64                `json:"version"`
}

// CreateRepositoryRequest adds a repository. Empty fields are left out and the
// backend applies its defaults (private, mutable tags, no retention).
type CreateRepositoryRequest struct {
	Name          string               `json:"name"`
	Description   string               `json:"description,omitempty"`
	Visibility    string               `json:"visibility,omitempty"`
	ImmutableTags *bool                `json:"immutable_tags,omitempty"`
	Retention     *RepositoryRetention `json:"retention,omitempty"`
}

// UpdateRepositoryRequest changes a repository. A nil field is left out and
// keeps its value. A Retention whose fields are all nil clears every rule,
// because each field is sent as null.
type UpdateRepositoryRequest struct {
	Description   *string              `json:"description,omitempty"`
	Visibility    *string              `json:"visibility,omitempty"`
	ImmutableTags *bool                `json:"immutable_tags,omitempty"`
	Retention     *RepositoryRetention `json:"retention,omitempty"`
}

// RegistryToken is a scoped registry credential. The secret is never part of
// it, except in the create response (see RegistryTokenWithSecret).
type RegistryToken struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Permission   string     `json:"permission"`
	Repositories []string   `json:"repositories"`
	ExpiresAt    *time.Time `json:"expires_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	CreatedAt    *time.Time `json:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at"`
}

// RegistryTokenWithSecret is the create response. Secret is nil when the
// create was replayed on the same Idempotency-Key: the backend shows a secret
// only once.
type RegistryTokenWithSecret struct {
	RegistryToken
	Secret *string `json:"secret"`
}

// CreateRegistryTokenRequest adds a registry token. Empty Repositories means
// every repository in the namespace; a nil ExpiresAt means it never expires.
type CreateRegistryTokenRequest struct {
	Name         string     `json:"name"`
	Permission   string     `json:"permission"`
	Repositories []string   `json:"repositories,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// GetRegistryNamespace returns the namespace. Before one is claimed it
// returns an *APIError with CodeRegistryNamespaceNotSet.
func (c *Client) GetRegistryNamespace(ctx context.Context) (*RegistryNamespace, error) {
	var ns RegistryNamespace
	if err := c.do(ctx, http.MethodGet, "/registry", nil, &ns); err != nil {
		return nil, fmt.Errorf("getting registry namespace: %w", err)
	}
	return &ns, nil
}

// SetRegistryNamespace claims the namespace, or changes its name or
// create_on_push. The same PUT twice leaves it the same, so it is retried on
// gateway errors.
func (c *Client) SetRegistryNamespace(ctx context.Context, req SetRegistryNamespaceRequest) (*RegistryNamespace, error) {
	var ns RegistryNamespace
	if err := c.do(ctx, http.MethodPut, "/registry", req, &ns, replaySafe()); err != nil {
		return nil, fmt.Errorf("setting registry namespace: %w", err)
	}
	return &ns, nil
}

// CreateRepository adds a repository. A replayed create answers the original,
// so it is retried on gateway errors.
func (c *Client) CreateRepository(ctx context.Context, req CreateRepositoryRequest) (*Repository, error) {
	var r Repository
	if err := c.do(ctx, http.MethodPost, "/registry/repositories", req, &r, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating container repository: %w", err)
	}
	return &r, nil
}

// GetRepository returns one repository, or ErrNotFound.
func (c *Client) GetRepository(ctx context.Context, id string) (*Repository, error) {
	var r Repository
	if err := c.do(ctx, http.MethodGet, repositoryPath(id), nil, &r); err != nil {
		return nil, fmt.Errorf("getting container repository %s: %w", id, err)
	}
	return &r, nil
}

// ListRepositories returns the namespace's repositories. q, when set, is the
// backend's search filter; callers still match names exactly themselves.
func (c *Client) ListRepositories(ctx context.Context, q string) ([]Repository, error) {
	path := "/registry/repositories"
	if q != "" {
		path += "?q=" + url.QueryEscape(q)
	}
	repos, err := listAll[Repository](ctx, c, path)
	if err != nil {
		return nil, fmt.Errorf("listing container repositories: %w", err)
	}
	return repos, nil
}

// UpdateRepository changes the fields set in req.
func (c *Client) UpdateRepository(ctx context.Context, id string, req UpdateRepositoryRequest) (*Repository, error) {
	var r Repository
	if err := c.do(ctx, http.MethodPatch, repositoryPath(id), req, &r, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating container repository %s: %w", id, err)
	}
	return &r, nil
}

// DeleteRepository asks for the repository, and every image in it, to be
// removed. It returns ErrNotFound when it is already gone, which callers treat
// as success.
func (c *Client) DeleteRepository(ctx context.Context, id string) error {
	if err := c.do(ctx, http.MethodDelete, repositoryPath(id), nil, nil, replaySafe()); err != nil {
		return fmt.Errorf("deleting container repository %s: %w", id, err)
	}
	return nil
}

// ListRegistryTokens returns every registry token, without secrets.
func (c *Client) ListRegistryTokens(ctx context.Context) ([]RegistryToken, error) {
	tokens, err := listAll[RegistryToken](ctx, c, "/registry/tokens")
	if err != nil {
		return nil, fmt.Errorf("listing registry tokens: %w", err)
	}
	return tokens, nil
}

// CreateRegistryToken adds a registry token. It is retried on gateway errors
// because a replayed create answers the original; the replay carries Secret
// nil, which callers must handle.
func (c *Client) CreateRegistryToken(ctx context.Context, req CreateRegistryTokenRequest) (*RegistryTokenWithSecret, error) {
	var t RegistryTokenWithSecret
	if err := c.do(ctx, http.MethodPost, "/registry/tokens", req, &t, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating registry token: %w", err)
	}
	return &t, nil
}

// DeleteRegistryToken revokes a registry token. It returns ErrNotFound when
// the token is already gone, which callers treat as success.
func (c *Client) DeleteRegistryToken(ctx context.Context, id string) error {
	if err := c.do(ctx, http.MethodDelete, "/registry/tokens/"+url.PathEscape(id), nil, nil, replaySafe()); err != nil {
		return fmt.Errorf("deleting registry token %s: %w", id, err)
	}
	return nil
}

// repositoryPath escapes the id so a malformed value cannot alter the URL path.
func repositoryPath(id string) string {
	return "/registry/repositories/" + url.PathEscape(id)
}
