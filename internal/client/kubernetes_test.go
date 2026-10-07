package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

const k8sJSON = `{"id":"k8s_1","name":"prod","zone_id":"af-abj-2","kubernetes_version_id":"k8sv_1","kubernetes_version":"1.31.2","network_id":"net_1","subnet_id":"snet_1","node_plan_id":"plan_1","node":{"vcpu":2,"memory_mb":4096,"disk_gb":40},"control_nodes":1,"workers":2,"nodes":3,"desired_state":"running","observed_state":"running","in_sync":true,"failure_code":null,"failure_reason":null,"available_upgrades":[{"id":"k8sv_2","zone_id":"af-abj-2","version":"1.32.0","status":"available","min_cpu":2,"min_memory_mb":2048}],"autoscaling":{"enabled":true,"min_workers":2,"max_workers":5},"api_allowed_cidrs":["203.0.113.0/24"],"endpoint":"https://k8s-1.example.test:6443","volume_storage_gb":30,"created_at":"2026-10-07T10:00:00Z","updated_at":"2026-10-07T10:00:00Z"}`

func TestKubernetesWritesUseTheRightRequest(t *testing.T) {
	tests := []struct {
		name       string
		call       func(*Client) error
		wantMethod string
		wantPath   string
		wantBody   string // exact JSON; empty means no body check
	}{
		{"create in a standard zone omits subnet and control nodes", func(c *Client) error {
			_, err := c.CreateKubernetesCluster(context.Background(), CreateKubernetesClusterRequest{Name: "prod", ZoneID: "af-abj-1", KubernetesVersionID: "k8sv_1", NodePlan: "s-2vcpu-4gb", Workers: 2})
			return err
		}, http.MethodPost, "/v1/kubernetes-clusters", `{"name":"prod","zone_id":"af-abj-1","kubernetes_version_id":"k8sv_1","node_plan":"s-2vcpu-4gb","workers":2}`},
		{"create with everything", func(c *Client) error {
			_, err := c.CreateKubernetesCluster(context.Background(), CreateKubernetesClusterRequest{Name: "prod", ZoneID: "af-abj-2", KubernetesVersionID: "k8sv_1", SubnetID: "snet_1", NodePlan: "s-2vcpu-4gb", ControlNodes: 3, Workers: 2})
			return err
		}, http.MethodPost, "/v1/kubernetes-clusters", `{"name":"prod","zone_id":"af-abj-2","kubernetes_version_id":"k8sv_1","subnet_id":"snet_1","node_plan":"s-2vcpu-4gb","control_nodes":3,"workers":2}`},
		{"create with autoscaling and an allow-list leaves workers out", func(c *Client) error {
			_, err := c.CreateKubernetesCluster(context.Background(), CreateKubernetesClusterRequest{Name: "prod", ZoneID: "af-abj-2", KubernetesVersionID: "k8sv_1", SubnetID: "snet_1", NodePlan: "s-2vcpu-4gb",
				Autoscaling: &KubernetesAutoscaling{Enabled: true, MinWorkers: 2, MaxWorkers: 5}, APIAllowedCIDRs: []string{"203.0.113.0/24"}})
			return err
		}, http.MethodPost, "/v1/kubernetes-clusters", `{"name":"prod","zone_id":"af-abj-2","kubernetes_version_id":"k8sv_1","subnet_id":"snet_1","node_plan":"s-2vcpu-4gb","autoscaling":{"enabled":true,"min_workers":2,"max_workers":5},"api_allowed_cidrs":["203.0.113.0/24"]}`},
		{"update workers only", func(c *Client) error {
			workers := int64(3)
			_, err := c.UpdateKubernetesCluster(context.Background(), "k8s_1", UpdateKubernetesClusterRequest{Workers: &workers})
			return err
		}, http.MethodPatch, "/v1/kubernetes-clusters/k8s_1", `{"workers":3}`},
		{"update autoscaling", func(c *Client) error {
			_, err := c.UpdateKubernetesCluster(context.Background(), "k8s_1", UpdateKubernetesClusterRequest{Autoscaling: &KubernetesAutoscaling{Enabled: true, MinWorkers: 2, MaxWorkers: 5}})
			return err
		}, http.MethodPatch, "/v1/kubernetes-clusters/k8s_1", `{"autoscaling":{"enabled":true,"min_workers":2,"max_workers":5}}`},
		{"turn autoscaling off and fix the count in one request", func(c *Client) error {
			workers := int64(4)
			_, err := c.UpdateKubernetesCluster(context.Background(), "k8s_1", UpdateKubernetesClusterRequest{Workers: &workers, Autoscaling: &KubernetesAutoscaling{}})
			return err
		}, http.MethodPatch, "/v1/kubernetes-clusters/k8s_1", `{"workers":4,"autoscaling":{"enabled":false,"min_workers":0,"max_workers":0}}`},
		{"clearing the allow-list sends an empty list", func(c *Client) error {
			_, err := c.UpdateKubernetesCluster(context.Background(), "k8s_1", UpdateKubernetesClusterRequest{APIAllowedCIDRs: &[]string{}})
			return err
		}, http.MethodPatch, "/v1/kubernetes-clusters/k8s_1", `{"api_allowed_cidrs":[]}`},
		{"upgrade", func(c *Client) error {
			_, err := c.UpgradeKubernetesCluster(context.Background(), "k8s_1", UpgradeKubernetesClusterRequest{KubernetesVersionID: "k8sv_2"})
			return err
		}, http.MethodPost, "/v1/kubernetes-clusters/k8s_1/upgrade", `{"kubernetes_version_id":"k8sv_2"}`},
		{"start", func(c *Client) error {
			_, err := c.StartKubernetesCluster(context.Background(), "k8s_1")
			return err
		}, http.MethodPost, "/v1/kubernetes-clusters/k8s_1/start", ""},
		{"stop", func(c *Client) error {
			_, err := c.StopKubernetesCluster(context.Background(), "k8s_1")
			return err
		}, http.MethodPost, "/v1/kubernetes-clusters/k8s_1/stop", ""},
		{"delete", func(c *Client) error {
			_, err := c.DeleteKubernetesCluster(context.Background(), "k8s_1")
			return err
		}, http.MethodDelete, "/v1/kubernetes-clusters/k8s_1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if r.Method != tt.wantMethod || r.URL.Path != tt.wantPath {
					t.Errorf("got %s %s, want %s %s", r.Method, r.URL.Path, tt.wantMethod, tt.wantPath)
				}
				if r.Header.Get("Idempotency-Key") == "" {
					t.Error("missing Idempotency-Key")
				}
				body, _ := io.ReadAll(r.Body)
				if tt.wantBody != "" && string(body) != tt.wantBody {
					t.Errorf("body = %s, want %s", body, tt.wantBody)
				}
				if attempts == 1 {
					w.WriteHeader(http.StatusBadGateway) // replay-safe: retried once
					return
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(opRefJSON))
			})
			if err := tt.call(c); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 {
				t.Errorf("attempts = %d, want 2 (one 502, then success)", attempts)
			}
		})
	}
}

func TestKubernetesUpdateThatChangesNothingHasNoOperation(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"operation_id":"","resource_id":"k8s_1","status":"succeeded"}`))
	})
	workers := int64(2)
	ref, err := c.UpdateKubernetesCluster(context.Background(), "k8s_1", UpdateKubernetesClusterRequest{Workers: &workers})
	if err != nil || ref.OperationID != "" || ref.ResourceID != "k8s_1" {
		t.Fatalf("ref = %+v, err %v", ref, err)
	}
}

func TestKubernetesReads(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/kubernetes-clusters/k8s_1":
			_, _ = w.Write([]byte(k8sJSON))
		case r.URL.Path == "/v1/kubernetes-clusters" && r.URL.Query().Get("cursor") == "":
			_, _ = w.Write([]byte(`{"data":[` + k8sJSON + `],"next_cursor":"c2"}`))
		case r.URL.Path == "/v1/kubernetes-clusters":
			_, _ = w.Write([]byte(`{"data":[{"id":"k8s_2","name":"dev"}],"next_cursor":null}`))
		case r.URL.Path == "/v1/kubernetes-versions":
			if got := r.URL.Query().Get("zone_id"); got != "af-abj-2" {
				t.Errorf("zone_id = %q", got)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"k8sv_1","zone_id":"af-abj-2","version":"1.31.2","status":"available","min_cpu":2,"min_memory_mb":2048}],"ha_zone_ids":["af-abj-2"]}`))
		default:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
		}
	})
	ctx := context.Background()

	k, err := c.GetKubernetesCluster(ctx, "k8s_1")
	if err != nil {
		t.Fatal(err)
	}
	if k.NetworkID == nil || *k.NetworkID != "net_1" || k.Node.MemoryMB != 4096 || k.Nodes != 3 || !k.InSync ||
		len(k.AvailableUpgrades) != 1 || k.AvailableUpgrades[0].ID != "k8sv_2" || k.FailureCode != nil {
		t.Errorf("cluster = %+v", k)
	}
	if k.Autoscaling != (KubernetesAutoscaling{Enabled: true, MinWorkers: 2, MaxWorkers: 5}) || len(k.APIAllowedCIDRs) != 1 ||
		k.Endpoint == nil || *k.Endpoint != "https://k8s-1.example.test:6443" || k.VolumeStorageGB != 30 {
		t.Errorf("new fields = %+v %v %v %d", k.Autoscaling, k.APIAllowedCIDRs, k.Endpoint, k.VolumeStorageGB)
	}
	if _, err := c.GetKubernetesCluster(ctx, "k8s_404"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	all, err := c.ListKubernetesClusters(ctx)
	if err != nil || len(all) != 2 || all[1].ID != "k8s_2" {
		t.Fatalf("list = %+v, err %v", all, err)
	}
	versions, err := c.ListKubernetesVersions(ctx, "af-abj-2")
	if err != nil || len(versions.Data) != 1 || versions.Data[0].MinMemoryMB != 2048 || len(versions.HAZoneIDs) != 1 {
		t.Fatalf("versions = %+v, err %v", versions, err)
	}
}

func TestKubeconfigIsAWriteThatReturnsTheFile(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/kubernetes-clusters/k8s_1/kubeconfig" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing Idempotency-Key")
		}
		_, _ = w.Write([]byte(`{"kubeconfig":"apiVersion: v1\nkind: Config\n"}`))
	})
	kc, err := c.GetKubeconfig(context.Background(), "k8s_1")
	if err != nil || !strings.HasPrefix(kc, "apiVersion: v1") {
		t.Fatalf("kubeconfig = %q, err %v", kc, err)
	}
}

func TestKubernetesRefusalsKeepTheirCode(t *testing.T) {
	tests := []struct {
		code   string
		status int
		call   func(*Client) error
		want   string
	}{
		{CodeKubernetesClusterNameTaken, 409, func(c *Client) error {
			_, err := c.CreateKubernetesCluster(context.Background(), CreateKubernetesClusterRequest{Name: "prod"})
			return err
		}, "creating Kubernetes cluster"},
		{CodeKubernetesClusterLimit, 409, func(c *Client) error {
			_, err := c.CreateKubernetesCluster(context.Background(), CreateKubernetesClusterRequest{Name: "prod"})
			return err
		}, "creating Kubernetes cluster"},
		{CodeInsufficientCredit, 402, func(c *Client) error {
			_, err := c.CreateKubernetesCluster(context.Background(), CreateKubernetesClusterRequest{Name: "prod"})
			return err
		}, "creating Kubernetes cluster"},
		{CodeKubernetesClusterBusy, 409, func(c *Client) error {
			workers := int64(3)
			_, err := c.UpdateKubernetesCluster(context.Background(), "k8s_1", UpdateKubernetesClusterRequest{Workers: &workers})
			return err
		}, "changing Kubernetes cluster k8s_1"},
		{CodeKubernetesClusterNotRunning, 409, func(c *Client) error {
			_, err := c.UpgradeKubernetesCluster(context.Background(), "k8s_1", UpgradeKubernetesClusterRequest{KubernetesVersionID: "k8sv_2"})
			return err
		}, "upgrading Kubernetes cluster k8s_1"},
		{CodeKubeconfigUnavailable, 409, func(c *Client) error {
			_, err := c.GetKubeconfig(context.Background(), "k8s_1")
			return err
		}, "kubeconfig"},
		{CodeKubernetesUnavailable, 503, func(c *Client) error {
			_, err := c.ListKubernetesVersions(context.Background(), "")
			return err
		}, "listing Kubernetes versions"},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"status":` + strconv.Itoa(tt.status) + `,"code":"` + tt.code + `","detail":"refused"}`))
			})
			err := tt.call(c)
			if !HasCode(err, tt.code) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want code %s", err, tt.code)
			}
		})
	}
}

func TestKubernetesClusterNotFoundCountsAsNotFound(t *testing.T) {
	// A deleted cluster answers this code during its 12-hour retire window, so
	// Read drops it from state and a delete wait ends at once.
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"KUBERNETES_CLUSTER_NOT_FOUND","detail":"No cluster with this id."}`))
	})
	if _, err := c.GetKubernetesCluster(context.Background(), "k8s_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
