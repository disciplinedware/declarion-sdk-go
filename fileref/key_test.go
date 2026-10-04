package fileref

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKey(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 23, 45, 1, time.FixedZone("offset", -5*60*60))
	content := []byte(`{"value":123}`)
	key, err := NewKey("evidence", "00000000-0000-0000-0000-000000000000", now, time.Hour, content)
	require.NoError(t, err)
	parsed, err := ParseKey(key.String())
	require.NoError(t, err)
	require.Equal(t, key, parsed)
	require.Equal(t, time.Date(2026, 10, 3, 17, 0, 0, 0, time.UTC), parsed.PartitionStart)
	require.Equal(t, time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC), parsed.PartitionEnd)
	digest := sha256.Sum256(content)
	require.Equal(t, hex.EncodeToString(digest[:]), parsed.SHA256)
	retry, err := NewKey(key.Backend, key.TenantID, now.Add(time.Minute), time.Hour, content)
	require.NoError(t, err)
	require.Equal(t, key.String(), retry.String())
	newAttempt, err := NewKey(key.Backend, key.TenantID, now.Add(time.Hour), time.Hour, content)
	require.NoError(t, err)
	require.NotEqual(t, key.String(), newAttempt.String())
}

func TestKeyRejectsMalformedValues(t *testing.T) {
	key, err := NewKey("evidence", "00000000-0000-0000-0000-000000000000", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), time.Hour, []byte("source"))
	require.NoError(t, err)
	parts := strings.Split(key.String(), "/")
	cases := map[string]string{
		"empty":             "",
		"url":               "https://example.com/" + key.String(),
		"extra_segment":     key.String() + "/extra",
		"backend_traversal": "../" + strings.Join(parts[1:], "/"),
		"wrong_shard":       strings.Join([]string{parts[0], parts[1], "xx", parts[3], parts[4]}, "/"),
		"short_digest":      strings.Join(parts[:4], "/") + "/abcd",
		"uppercase_digest":  strings.Join(parts[:4], "/") + "/" + strings.ToUpper(parts[4]),
		"compact_uuid":      strings.ReplaceAll(key.String(), parts[3], strings.ReplaceAll(parts[3], "-", "")),
		"missing_end":       strings.ReplaceAll(key.String(), parts[1], strings.Split(parts[1], "_")[0]),
		"reversed_interval": strings.ReplaceAll(key.String(), parts[1], "20261003T130000Z_20261003T120000Z"),
		"invalid_date":      strings.ReplaceAll(key.String(), parts[1], "20260230T120000Z_20260301T120000Z"),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseKey(value)
			require.ErrorIs(t, err, ErrInvalidKey)
		})
	}
}

func TestVerifyContent(t *testing.T) {
	content := []byte("source")
	key, err := NewKey("evidence", "00000000-0000-0000-0000-000000000000", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), time.Hour, content)
	require.NoError(t, err)

	tests := []struct {
		name    string
		content []byte
		size    int64
		digest  string
		wantErr error
	}{
		{name: "content_and_size_match", content: content, size: int64(len(content)), digest: key.SHA256},
		{name: "key_digest_without_size", content: content, digest: key.SHA256},
		{name: "size_mismatch", content: content, size: int64(len(content) + 1), digest: key.SHA256, wantErr: ErrSizeMismatch},
		{name: "digest_mismatch", content: []byte("other"), digest: key.SHA256, wantErr: ErrDigestMismatch},
		{name: "malformed_digest", content: content, digest: "ABC", wantErr: ErrDigestMismatch},
		{name: "uppercase_digest", content: content, digest: strings.ToUpper(key.SHA256), wantErr: ErrDigestMismatch},
		{name: "negative_size", content: content, size: -1, digest: key.SHA256, wantErr: ErrSizeMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyContent(tc.content, tc.size, tc.digest)
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestKeyRejectsUnrepresentableIntervals(t *testing.T) {
	for _, duration := range []time.Duration{0, -time.Second, time.Millisecond, time.Second + time.Nanosecond} {
		_, err := NewKey("evidence", "00000000-0000-0000-0000-000000000000", time.Now(), duration, nil)
		require.ErrorIs(t, err, ErrInvalidKey)
	}
}

func TestZeroAndMalformedKeyStringDoNotPanic(t *testing.T) {
	require.Empty(t, (Key{}).String())
	key, err := NewKey("evidence", "00000000-0000-0000-0000-000000000000", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), time.Hour, []byte("source"))
	require.NoError(t, err)
	key.SHA256 = "x"
	require.Empty(t, key.String())
}
