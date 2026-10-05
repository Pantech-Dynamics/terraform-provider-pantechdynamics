package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the ssh-key endpoints return on a 409.
const (
	// CodeSSHKeyNameTaken means the account already has a key with this name.
	CodeSSHKeyNameTaken = "SSH_KEY_NAME_TAKEN"

	// CodeSSHKeyAlreadyExists means this public key is already registered,
	// under any name.
	CodeSSHKeyAlreadyExists = "SSH_KEY_ALREADY_EXISTS"
)

// SSHKey is an SSH public key registered to the account.
type SSHKey struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Fingerprint string     `json:"fingerprint"`
	PublicKey   string     `json:"public_key"`
	CreatedAt   *time.Time `json:"created_at"`

	// PrivateKey is set only in the response to a create that omitted
	// PublicKey. The backend never stores it, so it must be saved right away.
	PrivateKey string `json:"private_key,omitempty"`
}

// CreateSSHKeyRequest registers a key. Leave PublicKey empty to have the
// backend generate a keypair.
type CreateSSHKeyRequest struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key,omitempty"`
}

// CreateSSHKey registers a key. It is not retried on a 5xx or transport error:
// the backend does not replay this endpoint on an Idempotency-Key, so a retry
// after a lost response would return a 409 instead of the created key. Callers
// recover from an ambiguous failure by listing keys.
func (c *Client) CreateSSHKey(ctx context.Context, req CreateSSHKeyRequest) (*SSHKey, error) {
	var key SSHKey
	if err := c.do(ctx, http.MethodPost, "/ssh-keys", req, &key); err != nil {
		return nil, fmt.Errorf("creating ssh key: %w", err)
	}
	return &key, nil
}

// GetSSHKey returns one key. It returns ErrNotFound when the key does not exist.
func (c *Client) GetSSHKey(ctx context.Context, id string) (*SSHKey, error) {
	var key SSHKey
	if err := c.do(ctx, http.MethodGet, sshKeyPath(id), nil, &key); err != nil {
		return nil, fmt.Errorf("getting ssh key %s: %w", id, err)
	}
	return &key, nil
}

// ListSSHKeys returns every key on the account. The backend currently returns
// them all in one page, but the cursor is followed anyway so a future page size
// limit cannot silently truncate the result.
func (c *Client) ListSSHKeys(ctx context.Context) ([]SSHKey, error) {
	keys, err := listAll[SSHKey](ctx, c, "/ssh-keys")
	if err != nil {
		return nil, fmt.Errorf("listing ssh keys: %w", err)
	}
	return keys, nil
}

// DeleteSSHKey removes a key. It returns ErrNotFound when the key is already
// gone, which callers should treat as success because a repeated delete returns
// 404, not 204. Like create, it is not retried on a 5xx or transport error,
// because the spec says it does not replay on an Idempotency-Key yet.
func (c *Client) DeleteSSHKey(ctx context.Context, id string) error {
	if err := c.do(ctx, http.MethodDelete, sshKeyPath(id), nil, nil); err != nil {
		return fmt.Errorf("deleting ssh key %s: %w", id, err)
	}
	return nil
}

// sshKeyPath escapes the id so a malformed value cannot alter the URL path.
func sshKeyPath(id string) string {
	return "/ssh-keys/" + url.PathEscape(id)
}
