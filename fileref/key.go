package fileref

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var backendName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

var (
	ErrInvalidKey     = errors.New("invalid file reference")
	ErrSizeMismatch   = errors.New("file reference size mismatch")
	ErrDigestMismatch = errors.New("file reference digest mismatch")
)

const partitionFormat = "20060102T150405Z"

type Key struct {
	Backend        string
	PartitionStart time.Time
	PartitionEnd   time.Time
	TenantID       string
	SHA256         string
}

func ValidBackendName(value string) bool { return backendName.MatchString(value) }

func NewKey(backend, tenantID string, storedAt time.Time, duration time.Duration, content []byte) (Key, error) {
	if !ValidBackendName(backend) {
		return Key{}, fmt.Errorf("%w: malformed backend name", ErrInvalidKey)
	}
	if duration < time.Second || duration%time.Second != 0 {
		return Key{}, fmt.Errorf("%w: partition duration must be a positive whole number of seconds", ErrInvalidKey)
	}
	tenant, err := uuid.Parse(tenantID)
	if err != nil || tenant.String() != tenantID {
		return Key{}, fmt.Errorf("%w: tenant must be a canonical UUID", ErrInvalidKey)
	}
	start := storedAt.UTC().Truncate(duration)
	end := start.Add(duration)
	if start.Year() < 1 || end.Year() > 9999 {
		return Key{}, fmt.Errorf("%w: partition time is outside the canonical UTC format", ErrInvalidKey)
	}
	digest := sha256.Sum256(content)
	return Key{Backend: backend, PartitionStart: start, PartitionEnd: end, TenantID: tenantID, SHA256: hex.EncodeToString(digest[:])}, nil
}

func ParseKey(value string) (Key, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 5 || !ValidBackendName(parts[0]) {
		return Key{}, fmt.Errorf("%w: malformed key", ErrInvalidKey)
	}
	start, end, err := ParsePartition(parts[1])
	if err != nil {
		return Key{}, err
	}
	tenant, err := uuid.Parse(parts[3])
	if err != nil || tenant.String() != parts[3] {
		return Key{}, fmt.Errorf("%w: malformed tenant", ErrInvalidKey)
	}
	digest, err := hex.DecodeString(parts[4])
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != parts[4] || parts[2] != parts[4][:2] {
		return Key{}, fmt.Errorf("%w: malformed digest or shard", ErrInvalidKey)
	}
	return Key{Backend: parts[0], PartitionStart: start, PartitionEnd: end, TenantID: parts[3], SHA256: parts[4]}, nil
}

// VerifyContent checks an optional positive size and the declared SHA-256.
func VerifyContent(content []byte, expectedSize int64, expectedSHA256 string) error {
	if expectedSize < 0 {
		return fmt.Errorf("%w: expected size cannot be negative", ErrSizeMismatch)
	}
	if expectedSize > 0 && int64(len(content)) != expectedSize {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrSizeMismatch, len(content), expectedSize)
	}
	digest, err := hex.DecodeString(expectedSHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != expectedSHA256 {
		return fmt.Errorf("%w: malformed SHA-256", ErrDigestMismatch)
	}
	actual := sha256.Sum256(content)
	if hex.EncodeToString(actual[:]) != expectedSHA256 {
		return ErrDigestMismatch
	}
	return nil
}

func ParsePartition(value string) (time.Time, time.Time, error) {
	parts := strings.Split(value, "_")
	if len(parts) != 2 {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: malformed partition", ErrInvalidKey)
	}
	start, startErr := time.Parse(partitionFormat, parts[0])
	end, endErr := time.Parse(partitionFormat, parts[1])
	if startErr != nil || endErr != nil || start.Format(partitionFormat) != parts[0] || end.Format(partitionFormat) != parts[1] || !end.After(start) || start.Year() < 1 {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: malformed partition", ErrInvalidKey)
	}
	return start, end, nil
}

func (key Key) Partition() string {
	return key.PartitionStart.UTC().Format(partitionFormat) + "_" + key.PartitionEnd.UTC().Format(partitionFormat)
}

// String returns an empty string when the exported key fields are invalid.
func (key Key) String() string {
	if !validKeyValue(key) {
		return ""
	}
	return key.Backend + "/" + key.Partition() + "/" + key.SHA256[:2] + "/" + key.TenantID + "/" + key.SHA256
}

func validKeyValue(key Key) bool {
	if !ValidBackendName(key.Backend) {
		return false
	}
	tenant, err := uuid.Parse(key.TenantID)
	if err != nil || tenant.String() != key.TenantID {
		return false
	}
	digest, err := hex.DecodeString(key.SHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != key.SHA256 {
		return false
	}
	_, _, err = ParsePartition(key.Partition())
	return err == nil
}
