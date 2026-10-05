package client

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// page is the envelope every list endpoint returns.
type page[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

// listAll fetches every page of a list, passing next_cursor back as cursor until
// it is null. Some lists are documented as always returning one page, but the
// cursor is followed anyway so a future page size cannot silently truncate the
// result. A cursor that repeats ends the loop, so a backend bug cannot spin it
// forever. The generic removes the same loop from every list call.
func listAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var items []T
	cursor := ""
	for {
		var p page[T]
		if err := c.do(ctx, http.MethodGet, listPath(path, cursor), nil, &p); err != nil {
			return nil, err
		}
		items = append(items, p.Data...)

		next := derefString(p.NextCursor)
		if next == "" || next == cursor {
			return items, nil
		}
		cursor = next
	}
}

// listPath appends the cursor query parameter when there is one, after any
// query the path already carries (such as ?placement=vpc).
func listPath(path, cursor string) string {
	if cursor == "" {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "cursor=" + url.QueryEscape(cursor)
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
