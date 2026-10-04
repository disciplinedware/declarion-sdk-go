package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/disciplinedware/declarion-sdk-go/fileref"
)

const (
	referenceUploadPath  = "/api/files/references/upload"
	referenceRuntimePath = "/api/files/references/runtime"
)

// ErrFileReferenceLimit marks a response rejected by the caller's read cap.
var ErrFileReferenceLimit = errors.New("file reference read limit exceeded")

// FileReferenceReceipt identifies an uploaded immutable file reference.
type FileReferenceReceipt struct {
	Key       string     `json:"key"`
	SizeBytes int64      `json:"size_bytes"`
	SHA256    string     `json:"sha256"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// UploadFileReference stores content in the backend declared by entityCode and
// fieldCode. retryKey identifies a retry of the same logical upload.
func (c *Client) UploadFileReference(ctx context.Context, entityCode, fieldCode string, content []byte, retryKey string, opts ...RequestOption) (*FileReferenceReceipt, error) {
	query := url.Values{
		"entity_code": {entityCode},
		"field_code":  {fieldCode},
		"retry_key":   {retryKey},
	}
	path := referenceUploadPath
	req, err := c.newRequestWithBodyReader(ctx, http.MethodPost, path, query, bytes.NewReader(content), "application/octet-stream", opts...)
	if err != nil {
		return nil, err
	}
	resp, err := c.executeRequest(req, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize+1))
	if err != nil {
		return nil, errorFromUnreadable(resp.StatusCode, path, err)
	}
	if int64(len(body)) > MaxResponseSize {
		return nil, errorFromUnreadable(resp.StatusCode, path, fmt.Errorf("response exceeded %d bytes", MaxResponseSize))
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, errorFromResponse(resp.StatusCode, body, path, resp.Header.Get("Content-Type"))
	}
	var receipt FileReferenceReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		return nil, fmt.Errorf("decode file reference receipt: %w", err)
	}
	return &receipt, nil
}

// ReadFileReferenceRuntime reads bytes using the declared runtime_read
// authority. maxBytes is the caller's required positive limit; oversized
// references fail without returning a prefix.
func (c *Client) ReadFileReferenceRuntime(ctx context.Context, entityCode, fieldCode, key string, maxBytes int64, opts ...RequestOption) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("file reference read limit must be positive")
	}
	parsed, err := fileref.ParseKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse file reference key: %w", err)
	}
	query := url.Values{
		"entity_code": {entityCode},
		"field_code":  {fieldCode},
		"key":         {key},
	}
	path := referenceRuntimePath
	req, err := c.newRequestWithBodyReader(ctx, http.MethodGet, path, query, nil, "application/json", opts...)
	if err != nil {
		return nil, err
	}
	resp, err := c.executeRequest(req, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize+1))
		if readErr != nil {
			return nil, errorFromUnreadable(resp.StatusCode, path, readErr)
		}
		if int64(len(body)) > MaxResponseSize {
			return nil, errorFromUnreadable(resp.StatusCode, path, fmt.Errorf("response exceeded %d bytes", MaxResponseSize))
		}
		return nil, errorFromResponse(resp.StatusCode, body, path, resp.Header.Get("Content-Type"))
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, errorFromUnreadable(resp.StatusCode, path, err)
	}
	if int64(len(content)) == maxBytes {
		var extra [1]byte
		n, probeErr := io.ReadFull(resp.Body, extra[:])
		if n > 0 {
			return nil, fmt.Errorf("%w: caller limit is %d bytes", ErrFileReferenceLimit, maxBytes)
		}
		if probeErr != nil && probeErr != io.EOF {
			return nil, errorFromUnreadable(resp.StatusCode, path, probeErr)
		}
	}
	if err := fileref.VerifyContent(content, 0, parsed.SHA256); err != nil {
		return nil, fmt.Errorf("verify file reference content: %w", err)
	}
	return content, nil
}
