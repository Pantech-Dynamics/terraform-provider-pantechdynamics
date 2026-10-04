package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const volumeJSON = `{"id":"vol_1","name":"data","size_gb":5,"disk_offering_slug":"small-5gb","storage_type":"shared","region":"af-abj","zone":"af-abj-1","attached_instance_id":null,"attached_instance_name":null,"desired_instance_id":null,"mount_point":null,"source_snapshot_id":null,"monthly_cost":{"currency":"NGN","amount_minor":116800},"desired_state":"present","observed_state":"active","created_at":"2026-10-04T12:10:12.37Z","updated_at":"2026-10-04T12:11:04.683466Z"}`

const volOpJSON = `{"operation_id":"op_1","resource_id":"vol_1","status":"submitting"}`

func TestCreateVolume(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/volumes" || r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("got %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		body, _ := io.ReadAll(r.Body)
		for _, absent := range []string{`"size_gb"`, `"region"`, `"instance_id"`, `"mount_point"`} {
			if strings.Contains(string(body), absent) {
				t.Errorf("body = %s: empty %s must be omitted", body, absent)
			}
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(volOpJSON))
	})

	ref, err := c.CreateVolume(context.Background(), CreateVolumeRequest{Name: "data", DiskOfferingSlug: "small-5gb"})
	if err != nil || ref.ResourceID != "vol_1" || ref.OperationID != "op_1" {
		t.Fatalf("ref = %+v, err = %v", ref, err)
	}
}

func TestCreateVolumeIsRetriedOnGatewayErrorWithTheSameKey(t *testing.T) {
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(volOpJSON))
	})
	if _, err := c.CreateVolume(context.Background(), CreateVolumeRequest{Name: "data", DiskOfferingSlug: "small-5gb", SizeGB: 10}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("keys = %v: create replays, so a gateway error is retried with the same key", keys)
	}
}

func TestCreateVolumeFieldErrors(t *testing.T) {
	tests := []struct{ field, code string }{
		{"disk_offering_slug", "DISK_OFFERING_NOT_FOUND"},
		{"size_gb", "SIZE_REQUIRED"},
		{"region", "REGION_NOT_AVAILABLE"},
		{"mount_point", "INVALID_MOUNT_POINT"},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"status":422,"code":"VALIDATION_FAILED","errors":[{"field":"` + tt.field + `","code":"` + tt.code + `","message":"x"}]}`))
			})
			_, err := c.CreateVolume(context.Background(), CreateVolumeRequest{Name: "data", DiskOfferingSlug: "x"})
			if !HasFieldCode(err, tt.field, tt.code) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestGetVolume(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/volumes/vol_1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(volumeJSON))
	})

	vol, err := c.GetVolume(context.Background(), "vol_1")
	if err != nil {
		t.Fatal(err)
	}
	if vol.ID != "vol_1" || vol.SizeGB != 5 || vol.DiskOfferingSlug != "small-5gb" || vol.StorageType == nil || *vol.StorageType != "shared" ||
		vol.AttachedInstanceID != nil || vol.MonthlyCost == nil || vol.MonthlyCost.AmountMinor != 116800 || vol.ObservedState != VolumeActive || vol.CreatedAt == nil {
		t.Fatalf("vol = %+v", vol)
	}
}

func TestGetVolumeNotFoundAndDeleted(t *testing.T) {
	t.Run("404 is ErrNotFound", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
		})
		if _, err := c.GetVolume(context.Background(), "vol_x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a deleted volume is a normal 200", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.ReplaceAll(volumeJSON, `"observed_state":"active"`, `"observed_state":"deleted"`)))
		})
		vol, err := c.GetVolume(context.Background(), "vol_1")
		if err != nil || vol.ObservedState != VolumeDeleted {
			t.Fatalf("vol = %+v, err = %v", vol, err)
		}
	})
}

func TestListVolumesFollowsCursor(t *testing.T) {
	var cursors []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		if cursor == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"vol_1"}],"next_cursor":"p2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"vol_2"}],"next_cursor":null}`))
	})
	vols, err := c.ListVolumes(context.Background())
	if err != nil || len(vols) != 2 || vols[1].ID != "vol_2" || len(cursors) != 2 || cursors[1] != "p2" {
		t.Fatalf("vols = %+v, cursors = %v, err = %v", vols, cursors, err)
	}
}

func TestVolumeActions(t *testing.T) {
	tests := []struct {
		name       string
		call       func(*Client) (*OperationReference, error)
		method     string
		path       string
		bodyHas    string
		wantKey    bool
		retriedOn5 bool // verified replay, so a gateway error is retried
	}{
		{"attach", func(c *Client) (*OperationReference, error) {
			return c.AttachVolume(context.Background(), "vol_1", "vm_1")
		}, http.MethodPost, "/v1/volumes/vol_1/attach", `"instance_id":"vm_1"`, true, true},
		{"detach", func(c *Client) (*OperationReference, error) {
			return c.DetachVolume(context.Background(), "vol_1")
		}, http.MethodPost, "/v1/volumes/vol_1/detach", "", true, true},
		{"resize", func(c *Client) (*OperationReference, error) {
			return c.ResizeVolume(context.Background(), "vol_1", "shared-10gb", 0)
		}, http.MethodPost, "/v1/volumes/vol_1/resize", `"disk_offering_slug":"shared-10gb"`, true, false},
		{"delete", func(c *Client) (*OperationReference, error) {
			return c.DeleteVolume(context.Background(), "vol_1")
		}, http.MethodDelete, "/v1/volumes/vol_1", "", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.Path != tt.path || (r.Header.Get("Idempotency-Key") != "") != tt.wantKey {
					t.Errorf("got %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
				}
				if body, _ := io.ReadAll(r.Body); tt.bodyHas != "" && !strings.Contains(string(body), tt.bodyHas) {
					t.Errorf("body = %s, want %s", body, tt.bodyHas)
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(volOpJSON))
			})
			ref, err := tt.call(c)
			if err != nil || ref.OperationID != "op_1" {
				t.Fatalf("ref = %+v, err = %v", ref, err)
			}
		})

		t.Run(tt.name+" gateway error handling", func(t *testing.T) {
			var keys []string
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				if len(keys) == 1 {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(volOpJSON))
			})
			_, _ = tt.call(c)
			want := 1
			if tt.retriedOn5 {
				want = 2
			}
			if len(keys) != want || (len(keys) == 2 && keys[0] != keys[1]) {
				t.Fatalf("keys = %v, want %d call(s) reusing one key", keys, want)
			}
		})
	}
}

func TestResizeVolumeSendsSizeOnlyWhenGiven(t *testing.T) {
	var bodies []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(volOpJSON))
	})
	_, _ = c.ResizeVolume(context.Background(), "vol_1", "shared-10gb", 0)
	_, _ = c.ResizeVolume(context.Background(), "vol_1", "custom", 50)
	if strings.Contains(bodies[0], "size_gb") || !strings.Contains(bodies[1], `"size_gb":50`) {
		t.Fatalf("bodies = %v", bodies)
	}
}

func TestVolumeInvalidStateIs409(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"status":409,"code":"INVALID_RESOURCE_STATE"}`))
	})
	if _, err := c.DeleteVolume(context.Background(), "vol_1"); !HasCode(err, CodeInvalidResourceState) {
		t.Fatalf("err = %v", err)
	}
}
