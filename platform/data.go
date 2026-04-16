package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// DataClient wraps /api/data/{entity} endpoints.
type DataClient struct {
	c *Client
}

// ListParams configures a List request.
type ListParams struct {
	// Limit is the max number of records to return.
	Limit int
	// Offset is the pagination offset.
	Offset int
	// Sort is the sort field (prefix with - for descending).
	Sort string
	// Search is the full-text search query.
	Search string
	// Filters are field-level filters as query params (e.g. {"status": "active"}).
	Filters map[string]string
}

// ListResponse is the paginated response from /api/data/{entity}.
type ListResponse struct {
	Items      []map[string]any `json:"items"`
	Total      int              `json:"total"`
	Limit      int              `json:"limit"`
	Offset     int              `json:"offset"`
	TotalPages int              `json:"total_pages"`
}

// Get retrieves a single record by ID.
func (d *DataClient) Get(ctx context.Context, entity, id string) (map[string]any, error) {
	body, status, err := d.c.do(ctx, "GET", fmt.Sprintf("/api/data/%s/%s", entity, id), nil, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: string(body), Path: fmt.Sprintf("/api/data/%s/%s", entity, id)}
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal get response: %w", err)
	}
	return result, nil
}

// List retrieves records with pagination and filters.
func (d *DataClient) List(ctx context.Context, entity string, params ListParams) (*ListResponse, error) {
	q := url.Values{}
	if params.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", params.Limit))
	}
	if params.Offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", params.Offset))
	}
	if params.Sort != "" {
		q.Set("sort", params.Sort)
	}
	if params.Search != "" {
		q.Set("search", params.Search)
	}
	for k, v := range params.Filters {
		q.Set(k, v)
	}

	body, status, err := d.c.do(ctx, "GET", fmt.Sprintf("/api/data/%s", entity), q, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: string(body), Path: fmt.Sprintf("/api/data/%s", entity)}
	}
	var result ListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal list response: %w", err)
	}
	return &result, nil
}

// Create creates records. Accepts a slice of records.
func (d *DataClient) Create(ctx context.Context, entity string, records []map[string]any) ([]map[string]any, error) {
	return d.writeMany(ctx, "POST", entity, "", records)
}

// Update updates records. Accepts a slice of records with PK fields included.
// Uses PATCH (core route: PATCH /api/data/{entity}).
func (d *DataClient) Update(ctx context.Context, entity string, records []map[string]any) ([]map[string]any, error) {
	return d.writeMany(ctx, "PATCH", entity, "", records)
}

// Delete soft-deletes records by PK objects.
// Uses POST /api/data/{entity}/delete (core convention: not HTTP DELETE).
func (d *DataClient) Delete(ctx context.Context, entity string, pkObjects []map[string]any) error {
	path := fmt.Sprintf("/api/data/%s/delete", entity)
	body, status, err := d.c.do(ctx, "POST", path, nil, pkObjects)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &APIError{StatusCode: status, Body: string(body), Path: path}
	}
	return nil
}

// BulkUpsert creates or updates records using unique_fields for dedup.
// uniqueFields is a comma-separated list of fields (e.g. "id" or "email,tenant_id").
func (d *DataClient) BulkUpsert(ctx context.Context, entity string, uniqueFields string, records []map[string]any) ([]map[string]any, error) {
	q := url.Values{}
	if uniqueFields != "" {
		q.Set("unique_fields", uniqueFields)
	}

	path := fmt.Sprintf("/api/data/%s", entity)
	u := path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	body, status, err := d.c.do(ctx, "POST", path, q, records)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: truncate(string(body), 500), Path: u}
	}
	var result []map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal upsert response: %w", err)
	}
	return result, nil
}

func (d *DataClient) writeMany(ctx context.Context, method, entity, queryExtra string, records []map[string]any) ([]map[string]any, error) {
	path := fmt.Sprintf("/api/data/%s", entity)
	var q url.Values
	if queryExtra != "" {
		q = url.Values{}
		q.Set("extra", queryExtra)
	}
	body, status, err := d.c.do(ctx, method, path, q, records)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &APIError{StatusCode: status, Body: truncate(string(body), 500), Path: path}
	}
	var result []map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal write response: %w", err)
	}
	return result, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "..."
}
