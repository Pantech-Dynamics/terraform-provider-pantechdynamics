package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestKubernetesPoolAndAddonWrites(t *testing.T) {
	tests := []struct {
		name       string
		call       func(*Client) error
		wantMethod string
		wantPath   string
		wantBody   string
	}{
		{"create a fixed pool", func(c *Client) error {
			_, err := c.CreateKubernetesNodePool(context.Background(), "k8s_1", CreateKubernetesNodePoolRequest{Name: "db", PlanID: "s-4vcpu-8gb", Workers: 2,
				Labels: map[string]string{"role": "db"}, Taints: []KubernetesNodePoolTaint{{Key: "dedicated", Value: "db", Effect: "NoSchedule"}}})
			return err
		}, http.MethodPost, "/v1/kubernetes-clusters/k8s_1/node-pools", `{"name":"db","plan_id":"s-4vcpu-8gb","workers":2,"labels":{"role":"db"},"taints":[{"key":"dedicated","value":"db","effect":"NoSchedule"}]}`},
		{"scale sends the whole size", func(c *Client) error {
			_, err := c.ScaleKubernetesNodePool(context.Background(), "k8s_1", "k8spool_1", ScaleKubernetesNodePoolRequest{Workers: 3})
			return err
		}, http.MethodPatch, "/v1/kubernetes-clusters/k8s_1/node-pools/k8spool_1", `{"workers":3,"autoscaling_enabled":false,"min_workers":0,"max_workers":0}`},
		{"delete a pool", func(c *Client) error {
			_, err := c.DeleteKubernetesNodePool(context.Background(), "k8s_1", "k8spool_1")
			return err
		}, http.MethodDelete, "/v1/kubernetes-clusters/k8s_1/node-pools/k8spool_1", ""},
		{"turn an add-on off", func(c *Client) error {
			_, err := c.ToggleKubernetesAddon(context.Background(), "k8s_1", "metrics-server", ToggleKubernetesAddonRequest{Enabled: false})
			return err
		}, http.MethodPost, "/v1/kubernetes-clusters/k8s_1/addons/metrics-server/toggle", `{"enabled":false}`},
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
				t.Errorf("attempts = %d, want 2", attempts)
			}
		})
	}
}

func TestKubernetesPoolAndAddonReads(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/kubernetes-clusters/k8s_1/node-pools":
			_, _ = w.Write([]byte(`{"data":[{"id":"k8spool_1","cluster_id":"k8s_1","name":"db","node_plan_id":"plan_2","workers":2,"autoscaling_enabled":true,"min_workers":1,"max_workers":4,"labels":{"role":"db"},"taints":[{"key":"dedicated","value":"db","effect":"NoSchedule"}]}],"next_cursor":null}`))
		case "/v1/kubernetes-clusters/k8s_1/addons":
			_, _ = w.Write([]byte(`{"data":[{"id":"kad_1","cluster_id":"k8s_1","name":"metrics-server","version":"0.7.2","status":"active","enabled":true}],"next_cursor":null}`))
		default:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundBody))
		}
	})
	ctx := context.Background()
	pools, err := c.ListKubernetesNodePools(ctx, "k8s_1")
	if err != nil || len(pools) != 1 || pools[0].MaxWorkers != 4 || pools[0].Labels["role"] != "db" || pools[0].Taints[0].Effect != "NoSchedule" {
		t.Fatalf("pools = %+v, err %v", pools, err)
	}
	addons, err := c.ListKubernetesAddons(ctx, "k8s_1")
	if err != nil || len(addons) != 1 || !addons[0].Enabled || addons[0].Name != "metrics-server" {
		t.Fatalf("addons = %+v, err %v", addons, err)
	}
	if _, err := c.ListKubernetesNodePools(ctx, "k8s_gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("gone cluster: err = %v, want ErrNotFound", err)
	}
}
