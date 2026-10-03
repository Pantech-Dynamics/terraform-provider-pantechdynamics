package client

import (
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIdempotencyKey(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		key, err := newIdempotencyKey()
		if err != nil {
			t.Fatal(err)
		}
		if !uuidV4.MatchString(key) {
			t.Fatalf("key %q is not a UUIDv4", key)
		}
		if seen[key] {
			t.Fatalf("duplicate key %q", key)
		}
		seen[key] = true
	}
}
