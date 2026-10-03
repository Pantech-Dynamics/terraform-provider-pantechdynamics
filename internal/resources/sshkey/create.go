package sshkey

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// errGeneratedKeyLost means a create may have succeeded but its one-time
// private key can never be recovered.
var errGeneratedKeyLost = errors.New("the key may have been created, but its generated private key cannot be recovered")

// createKey registers the key. The backend does not replay ssh-key creates, so
// the client never retries them. When the outcome is unknown (the connection
// dropped, or the backend answered 5xx) it looks the key up instead of failing
// blindly, so a created key is not orphaned.
func createKey(ctx context.Context, api keyAPI, req client.CreateSSHKeyRequest) (*client.SSHKey, error) {
	key, err := api.CreateSSHKey(ctx, req)
	if err == nil {
		return key, nil
	}
	if !isAmbiguous(ctx, err) {
		return nil, err
	}

	tflog.Warn(ctx, "ssh key create outcome unknown, looking it up", map[string]any{"name": req.Name})
	return recoverCreated(ctx, api, req, err)
}

// recoverCreated adopts the key a lost create left behind. It adopts only when
// the user supplied the public key, because then name plus key prove it is
// ours. A generated pair cannot be adopted, since its private key is gone.
func recoverCreated(ctx context.Context, api keyAPI, req client.CreateSSHKeyRequest, cause error) (*client.SSHKey, error) {
	keys, listErr := api.ListSSHKeys(ctx)
	if listErr != nil {
		return nil, fmt.Errorf("%w; looking the key up afterwards also failed: %v", cause, listErr)
	}

	for i := range keys {
		if keys[i].Name != req.Name {
			continue
		}
		if req.PublicKey == "" {
			return nil, fmt.Errorf("%w (key %s): %w", errGeneratedKeyLost, keys[i].ID, cause)
		}
		if samePublicKey(req.PublicKey, keys[i].PublicKey) {
			return &keys[i], nil
		}
	}
	return nil, cause
}

// isAmbiguous reports whether a failed create may still have taken effect.
// A definite answer from the backend below 500 (409, 422, 401, ...) means it did
// not. A cancelled context is not ambiguous either, since the user stopped it.
func isAmbiguous(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	return true // transport error: no response, outcome unknown
}
