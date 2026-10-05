package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const snapJSON = `{"id":"snap_1","name":"nightly","instance_id":"vm_1","volume_id":null,"source_instance_name":"web","volume_name":null,"region":"af-abj","trigger":"manual","size_bytes":1930166272,"desired_state":"present","observed_state":"active","completed_at":"2026-10-04T13:37:48.020565Z","created_at":"2026-10-04T13:36:50.51992Z","updated_at":"2026-10-04T13:37:48.020565Z"}`

const snapOpJSON = `{"operation_id":"op_1","resource_id":"snap_1","status":"submitting"}`

func TestCreateSnapshots(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) (*OperationReference, error)
		path string
	}{
		{"instance", func(c *Client) (*OperationReference, error) {
			return c.CreateInstanceSnapshot(context.Background(), "vm_1", "nightly")
		}, "/v1/instances/vm_1/snapshots"},
		{"volume", func(c *Client) (*OperationReference, error) {
			return c.CreateVolumeSnapshot(context.Background(), "vol_1", "nightly")
		}, "/v1/volumes/vol_1/snapshots"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var keys []string
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				if r.Method != http.MethodPost || r.URL.Path != tt.path {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				if body, _ := io.ReadAll(r.Body); !strings.Contains(string(body), `"name":"nightly"`) {
					t.Errorf("body = %s", body)
				}
				if len(keys) == 1 {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(snapOpJSON))
			})

			ref, err := tt.call(c)

			if err != nil || ref.ResourceID != "snap_1" || ref.OperationID != "op_1" {
				t.Fatalf("ref = %+v, err = %v", ref, err)
			}
			if len(keys) != 2 || keys[0] != keys[1] {
				t.Fatalf("keys = %v: a create replays, so a gateway error is retried with the same key", keys)
			}
		})
	}
}

func TestCreateSnapshotNameTaken(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"status":409,"code":"SNAPSHOT_NAME_TAKEN"}`))
	})
	if _, err := c.CreateVolumeSnapshot(context.Background(), "vol_1", "x"); !HasCode(err, CodeSnapshotNameTaken) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetSnapshot(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/snapshots/snap_1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(snapJSON))
	})

	snap, err := c.GetSnapshot(context.Background(), "snap_1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.ID != "snap_1" || snap.InstanceID == nil || *snap.InstanceID != "vm_1" || snap.VolumeID != nil || snap.SizeBytes != 1930166272 ||
		snap.Trigger != "manual" || snap.ObservedState != SnapshotActive || snap.CompletedAt == nil {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestGetSnapshotNotFoundAndDeleted(t *testing.T) {
	t.Run("404 is ErrNotFound", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
		})
		if _, err := c.GetSnapshot(context.Background(), "snap_x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a deleted snapshot is a normal 200", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.ReplaceAll(snapJSON, `"observed_state":"active"`, `"observed_state":"deleted"`)))
		})
		snap, err := c.GetSnapshot(context.Background(), "snap_1")
		if err != nil || snap.ObservedState != SnapshotDeleted {
			t.Fatalf("snap = %+v, err = %v", snap, err)
		}
	})
}

func TestListSnapshotsFollowsCursor(t *testing.T) {
	var cursors []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		if cursor == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"snap_1"}],"next_cursor":"p2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"snap_2"}],"next_cursor":null}`))
	})
	snaps, err := c.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 2 || snaps[1].ID != "snap_2" || len(cursors) != 2 || cursors[1] != "p2" {
		t.Fatalf("snaps = %+v, cursors = %v, err = %v", snaps, cursors, err)
	}
}

func TestRestoreSnapshot(t *testing.T) {
	var bodies []string
	var keys []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if r.URL.Path != "/v1/snapshots/snap_1/restore" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if len(keys) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"operation_id":"op_2","resource_id":"vol_9","status":"submitting"}`))
	})

	ref, err := c.RestoreSnapshot(context.Background(), "snap_1", "restored", "small-5gb", 0)

	if err != nil || ref.ResourceID != "vol_9" {
		t.Fatalf("ref = %+v, err = %v", ref, err)
	}
	if strings.Contains(bodies[0], "size_gb") || !strings.Contains(bodies[0], `"disk_offering_slug":"small-5gb"`) {
		t.Fatalf("body = %s: size_gb is omitted when zero", bodies[0])
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("keys = %v: restore replays, so it is retried with the same key", keys)
	}
}

const schedJSON = `{"instance_id":"vm_1","volume_id":null,"frequency":"weekly","retention_count":5,"enabled":true,"next_run_at":"2026-10-04T13:27:30.254819Z"}`

func TestSnapshotSchedules(t *testing.T) {
	tests := []struct {
		name string
		path string
		get  func(*Client) (*SnapshotSchedule, error)
		put  func(*Client, PutSnapshotScheduleRequest) (*SnapshotSchedule, error)
	}{
		{"instance", "/v1/instances/vm_1/snapshot-schedule",
			func(c *Client) (*SnapshotSchedule, error) {
				return c.GetInstanceSnapshotSchedule(context.Background(), "vm_1")
			},
			func(c *Client, r PutSnapshotScheduleRequest) (*SnapshotSchedule, error) {
				return c.PutInstanceSnapshotSchedule(context.Background(), "vm_1", r)
			}},
		{"volume", "/v1/volumes/vol_1/snapshot-schedule",
			func(c *Client) (*SnapshotSchedule, error) {
				return c.GetVolumeSnapshotSchedule(context.Background(), "vol_1")
			},
			func(c *Client, r PutSnapshotScheduleRequest) (*SnapshotSchedule, error) {
				return c.PutVolumeSnapshotSchedule(context.Background(), "vol_1", r)
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name+" get", func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != tt.path {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(schedJSON))
			})
			sched, err := tt.get(c)
			if err != nil || sched.Frequency != FrequencyWeekly || sched.RetentionCount != 5 || !sched.Enabled || sched.NextRunAt == nil {
				t.Fatalf("sched = %+v, err = %v", sched, err)
			}
		})

		t.Run(tt.name+" get with no schedule is ErrNotFound", func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"status":404,"code":"SNAPSHOT_SCHEDULE_NOT_FOUND","detail":"No schedule exists for this resource."}`))
			})
			if _, err := tt.get(c); !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v: a missing schedule must count as not found", err)
			}
		})

		t.Run(tt.name+" put sends every field and is retried", func(t *testing.T) {
			var keys []string
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				b, _ := io.ReadAll(r.Body)
				for _, want := range []string{`"frequency":"daily"`, `"retention_count":3`, `"enabled":false`} {
					if !strings.Contains(string(b), want) {
						t.Errorf("body = %s, want %s: a false value must not be omitted", b, want)
					}
				}
				if r.Method != http.MethodPut || r.URL.Path != tt.path {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				if len(keys) == 1 {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				_, _ = w.Write([]byte(schedJSON))
			})
			sched, err := tt.put(c, PutSnapshotScheduleRequest{Frequency: "daily", RetentionCount: 3, Enabled: false})
			if err != nil || sched == nil || len(keys) != 2 || keys[0] != keys[1] {
				t.Fatalf("sched = %+v, err = %v, keys = %v", sched, err, keys)
			}
		})
	}
}

func TestPutScheduleValidationError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":422,"code":"VALIDATION_FAILED","detail":"frequency must be daily, weekly or monthly, and retention_count 1 to 168."}`))
	})
	_, err := c.PutVolumeSnapshotSchedule(context.Background(), "vol_1", PutSnapshotScheduleRequest{Frequency: "hourly", RetentionCount: 3, Enabled: true})
	if !HasCode(err, "VALIDATION_FAILED") || !strings.Contains(err.Error(), "retention_count 1 to 168") {
		t.Fatalf("err = %v: the text detail must be kept", err)
	}
}
