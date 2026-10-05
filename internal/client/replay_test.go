package client

import (
	"context"
	"net/http"
	"testing"
)

// The spec says every write replays on the same Idempotency-Key except ssh-key
// create and delete (and the console, which the provider does not call). So the
// replayable writes retry a gateway error with the same key, and the ssh-key
// ones never retry.
func TestWritesRetryOnlyWhenTheSpecSaysTheyReplay(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		call      func(*Client) error
		wantCalls int
	}{
		{"instance rename", func(c *Client) error { _, err := c.RenameInstance(ctx, "vm_1", "new"); return err }, 2},
		{"instance start", func(c *Client) error { _, err := c.StartInstance(ctx, "vm_1"); return err }, 2},
		{"instance stop", func(c *Client) error { _, err := c.StopInstance(ctx, "vm_1"); return err }, 2},
		{"instance resize", func(c *Client) error { _, err := c.ResizeInstance(ctx, "vm_1", "starter"); return err }, 2},
		{"volume resize", func(c *Client) error { _, err := c.ResizeVolume(ctx, "vol_1", "custom", 20); return err }, 2},
		{"volume delete", func(c *Client) error { _, err := c.DeleteVolume(ctx, "vol_1"); return err }, 2},
		{"snapshot delete", func(c *Client) error { _, err := c.DeleteSnapshot(ctx, "snap_1"); return err }, 2},
		{"database start", func(c *Client) error { _, err := c.StartDatabase(ctx, "db_1"); return err }, 2},
		{"database password", func(c *Client) error { _, err := c.ChangeDatabasePassword(ctx, "db_1", "x"); return err }, 2},
		{"ssh key create", func(c *Client) error { _, err := c.CreateSSHKey(ctx, CreateSSHKeyRequest{Name: "k"}); return err }, 1},
		{"ssh key delete", func(c *Client) error { return c.DeleteSSHKey(ctx, "sshk_1") }, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var keys []string
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				if len(keys) == 1 {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"operation_id":"op_1","resource_id":"x","status":"submitting"}`))
			})
			_ = tt.call(c)
			if len(keys) != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", len(keys), tt.wantCalls)
			}
			if len(keys) == 2 && (keys[0] == "" || keys[0] != keys[1]) {
				t.Fatalf("keys = %v, want one key reused", keys)
			}
		})
	}
}
