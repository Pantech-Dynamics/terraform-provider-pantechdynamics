package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestProviderMetadata(t *testing.T) {
	var resp provider.MetadataResponse
	New("test")().Metadata(context.Background(), provider.MetadataRequest{}, &resp)

	if resp.TypeName != "pantechdynamics" {
		t.Fatalf("TypeName = %q, want pantechdynamics", resp.TypeName)
	}
}

func TestNewClient(t *testing.T) {
	const goodURL = "https://api-dev.example.com/public/v1"

	tests := []struct {
		name      string
		cfg       providerModel
		envURL    string
		envKey    string
		wantAttrs []string // attribute paths expected in error diagnostics
	}{
		{
			name: "from config",
			cfg:  providerModel{BaseURL: types.StringValue(goodURL), APIKey: types.StringValue("PAN_x")},
		},
		{
			name:   "from environment",
			cfg:    providerModel{BaseURL: types.StringNull(), APIKey: types.StringNull()},
			envURL: goodURL, envKey: "PAN_x",
		},
		{
			name:   "config wins over environment",
			cfg:    providerModel{BaseURL: types.StringValue(goodURL), APIKey: types.StringValue("PAN_x")},
			envURL: "not a url", envKey: "PAN_env",
		},
		{
			name:      "nothing set",
			cfg:       providerModel{BaseURL: types.StringNull(), APIKey: types.StringNull()},
			wantAttrs: []string{"base_url", "api_key"},
		},
		{
			name:      "missing key only",
			cfg:       providerModel{BaseURL: types.StringValue(goodURL), APIKey: types.StringNull()},
			wantAttrs: []string{"api_key"},
		},
		{
			name:      "invalid url",
			cfg:       providerModel{BaseURL: types.StringValue("api.example.com"), APIKey: types.StringValue("PAN_x")},
			wantAttrs: []string{"base_url"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envBaseURL, tt.envURL)
			t.Setenv(envAPIKey, tt.envKey)

			c, diags := newClient(tt.cfg, "test")

			if len(tt.wantAttrs) == 0 {
				if diags.HasError() || c == nil {
					t.Fatalf("diags = %v, client = %v", diags, c)
				}
				return
			}
			if c != nil || !diags.HasError() {
				t.Fatalf("want an error, got client %v and diags %v", c, diags)
			}
			var got []string
			for _, d := range diags.Errors() {
				if withPath, ok := d.(diag.DiagnosticWithPath); ok {
					got = append(got, withPath.Path().String())
				}
			}
			if !slices.Equal(got, tt.wantAttrs) {
				t.Fatalf("error attributes = %v, want %v", got, tt.wantAttrs)
			}
		})
	}
}

func TestNewClientErrorsNeverContainAPIKey(t *testing.T) {
	const secret = "PAN_super_secret_value"
	_, diags := newClient(providerModel{BaseURL: types.StringValue("bad url"), APIKey: types.StringValue(secret)}, "test")

	for _, d := range diags {
		if strings.Contains(d.Summary()+d.Detail(), secret) {
			t.Fatalf("diagnostic leaks the API key: %s", d.Detail())
		}
	}
}

func TestParseRequestTimeout(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr string
	}{
		{"90s", 90 * time.Second, ""},
		{"2m", 2 * time.Minute, ""},
		{"1m30s", 90 * time.Second, ""},
		{"500ms", 500 * time.Millisecond, ""},
		{"90", 0, "not a duration"}, // a bare number has no unit
		{"fast", 0, "not a duration"},
		{"0s", 0, "greater than zero"},
		{"-5s", 0, "greater than zero"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseRequestTimeout(tt.in)
			if tt.wantErr == "" {
				if err != nil || got != tt.want {
					t.Fatalf("got %v, %v; want %v", got, err, tt.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewClientRequestTimeout(t *testing.T) {
	const goodURL = "https://api-dev.example.com/public/v1"
	base := providerModel{BaseURL: types.StringValue(goodURL), APIKey: types.StringValue("PAN_x")}

	tests := []struct {
		name    string
		timeout types.String
		env     string
		wantErr bool
	}{
		{"unset keeps the client default", types.StringNull(), "", false},
		{"from config", types.StringValue("2m"), "", false},
		{"from environment", types.StringNull(), "90s", false},
		{"config wins over a bad environment value", types.StringValue("2m"), "nonsense", false},
		{"bad config value", types.StringValue("soon"), "", true},
		{"zero", types.StringValue("0s"), "", true},
		{"bad environment value", types.StringNull(), "nonsense", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envBaseURL, "")
			t.Setenv(envAPIKey, "")
			t.Setenv(envRequestTimeout, tt.env)
			cfg := base
			cfg.RequestTimeout = tt.timeout

			c, diags := newClient(cfg, "test")

			if diags.HasError() != tt.wantErr {
				t.Fatalf("error = %v, want %v (%v)", diags.HasError(), tt.wantErr, diags)
			}
			if tt.wantErr {
				if c != nil {
					t.Error("a bad timeout must not produce a client")
				}
				d, ok := diags.Errors()[0].(diag.DiagnosticWithPath)
				if !ok || d.Path().String() != "request_timeout" {
					t.Errorf("error should point at request_timeout, got %v", diags.Errors()[0])
				}
			}
		})
	}
}

// The timeout from the provider block must reach the HTTP client, not just parse.
func TestRequestTimeoutReachesTheClient(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`))
	}))
	t.Cleanup(slow.Close)
	t.Setenv(envBaseURL, "")
	t.Setenv(envAPIKey, "")
	t.Setenv(envRequestTimeout, "")
	cfg := providerModel{BaseURL: types.StringValue(slow.URL + "/v1"), APIKey: types.StringValue("PAN_x")}

	cfg.RequestTimeout = types.StringValue("50ms")
	short, diags := newClient(cfg, "test")
	if diags.HasError() {
		t.Fatal(diags)
	}
	if _, err := short.ListSSHKeys(context.Background()); err == nil {
		t.Error("request_timeout = 50ms should fail against a 400ms server")
	}

	cfg.RequestTimeout = types.StringValue("10s")
	long, diags := newClient(cfg, "test")
	if diags.HasError() {
		t.Fatal(diags)
	}
	if _, err := long.ListSSHKeys(context.Background()); err != nil {
		t.Errorf("request_timeout = 10s should succeed: %v", err)
	}
}
