package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

func (c *Client) EntitySchema(ctx context.Context, entityCode string, opts ...RequestOption) (map[string]any, error) {
	if entityCode == "" {
		return nil, fmt.Errorf("entity schema requires an entity code")
	}
	path := "/api/schema/entities/" + url.PathEscape(entityCode)
	body, status, contentType, err := c.do(ctx, http.MethodGet, path, nil, nil, opts...)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, errorFromResponse(status, body, path, contentType)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("entity schema response: %w", err)
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf("entity schema response is missing data")
	}
	return envelope.Data, nil
}
