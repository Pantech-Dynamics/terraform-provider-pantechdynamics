package sshkey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
)

// fakeAPI is an in-memory keyAPI. Each *Err field, when set, is returned by the
// matching method, and createLands makes a failing create still store the key,
// which simulates a lost response.
type fakeAPI struct {
	keys       []client.SSHKey
	createErr  error
	createLand bool
	getErr     error
	listErr    error
	deleteErr  error

	nextID  int
	creates int
	lists   int
	deleted []string
}

func (f *fakeAPI) CreateSSHKey(_ context.Context, req client.CreateSSHKeyRequest) (*client.SSHKey, error) {
	f.creates++
	if f.createErr != nil && !f.createLand {
		return nil, f.createErr
	}

	f.nextID++
	now := time.Date(2026, 10, 3, 22, 7, 37, 0, time.UTC)
	key := client.SSHKey{
		ID:          fmt.Sprintf("sshk_%d", f.nextID),
		Name:        req.Name,
		PublicKey:   req.PublicKey,
		Fingerprint: "SHA256:fp" + fmt.Sprint(f.nextID),
		CreatedAt:   &now,
	}
	if req.PublicKey == "" {
		key.PublicKey = "ssh-rsa GENERATED generated"
		key.PrivateKey = "PRIVATE-KEY"
	}
	stored := key
	stored.PrivateKey = "" // the backend never keeps it
	f.keys = append(f.keys, stored)

	if f.createErr != nil {
		return nil, f.createErr
	}
	return &key, nil
}

func (f *fakeAPI) GetSSHKey(_ context.Context, id string) (*client.SSHKey, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.keys {
		if f.keys[i].ID == id {
			k := f.keys[i]
			return &k, nil
		}
	}
	return nil, fmt.Errorf("getting ssh key: %w", client.ErrNotFound)
}

func (f *fakeAPI) ListSSHKeys(_ context.Context) ([]client.SSHKey, error) {
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]client.SSHKey(nil), f.keys...), nil
}

func (f *fakeAPI) DeleteSSHKey(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for i := range f.keys {
		if f.keys[i].ID == id {
			f.keys = append(f.keys[:i], f.keys[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("deleting ssh key: %w", client.ErrNotFound)
}

var errConnReset = errors.New("connection reset by peer")
