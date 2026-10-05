package client

import (
	"context"
	"net/http"
	"testing"
)

func TestListAllFollowsTheCursor(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		pages     map[string]string // cursor -> body
		wantIDs   int
		wantQuery string // the placement filter must survive on every page
		wantErr   bool
	}{
		{
			name: "one page",
			path: "/plans",
			pages: map[string]string{
				"": `{"data":[{"id":"a"}],"next_cursor":null}`,
			},
			wantIDs: 1,
		},
		{
			name: "three pages keep the filter",
			path: "/plans?placement=vpc",
			pages: map[string]string{
				"":   `{"data":[{"id":"a"}],"next_cursor":"p2"}`,
				"p2": `{"data":[{"id":"b"}],"next_cursor":"p3"}`,
				"p3": `{"data":[{"id":"c"}],"next_cursor":""}`,
			},
			wantIDs:   3,
			wantQuery: "vpc",
		},
		{
			name: "a repeated cursor ends the loop",
			path: "/plans",
			pages: map[string]string{
				"":   `{"data":[{"id":"a"}],"next_cursor":"p2"}`,
				"p2": `{"data":[{"id":"b"}],"next_cursor":"p2"}`,
			},
			wantIDs: 2,
		},
		{
			name:    "malformed page",
			path:    "/plans",
			pages:   map[string]string{"": `{not json`},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tt.wantQuery != "" && r.URL.Query().Get("placement") != tt.wantQuery {
					t.Errorf("placement = %q on %s", r.URL.Query().Get("placement"), r.URL)
				}
				body, ok := tt.pages[r.URL.Query().Get("cursor")]
				if !ok {
					t.Errorf("unexpected cursor in %s", r.URL)
				}
				_, _ = w.Write([]byte(body))
			})
			got, err := listAll[Plan](context.Background(), c, tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if len(got) != tt.wantIDs {
				t.Errorf("got %d items, want %d", len(got), tt.wantIDs)
			}
		})
	}
}
