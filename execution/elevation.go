package execution

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

const ElevationHeader = "X-Declarion-Elevation"

type Elevation struct {
	System bool     `json:"system,omitempty"`
	Grants []string `json:"grants,omitempty"`
}

type CallerContext struct {
	ImpersonationSessionID string `json:"impersonation_session_id,omitempty"`
	AgentSessionID         string `json:"agent_session_id,omitempty"`
	AgentConversationID    string `json:"agent_conversation_id,omitempty"`
	SessionID              string `json:"session_id,omitempty"`
	AuthMethod             string `json:"auth_method,omitempty"`
	APIKeyID               string `json:"api_key_id,omitempty"`
}

func Normalize(e Elevation) Elevation {
	e.Grants = slices.Clone(e.Grants)
	slices.Sort(e.Grants)
	e.Grants = slices.Compact(e.Grants)
	return e
}

func (e Elevation) Empty() bool { return !e.System && len(e.Grants) == 0 }

func (e Elevation) WithGrants(grants ...string) Elevation {
	e.Grants = append(slices.Clone(e.Grants), grants...)
	return Normalize(e)
}

func (e Elevation) Contains(parent Elevation) bool {
	if parent.System && !e.System {
		return false
	}
	for _, grant := range parent.Grants {
		if !slices.Contains(e.Grants, grant) {
			return false
		}
	}
	return true
}

func Encode(e Elevation) (string, error) {
	b, err := json.Marshal(Normalize(e))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func Decode(header string) (Elevation, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(header)
	if err != nil {
		return Elevation{}, fmt.Errorf("elevation encoding: %w", err)
	}
	if base64.RawURLEncoding.EncodeToString(b) != header {
		return Elevation{}, fmt.Errorf("elevation requires unpadded base64url")
	}
	var e Elevation
	if err := json.Unmarshal(b, &e); err != nil {
		return Elevation{}, err
	}
	return Normalize(e), nil
}

func (e *Elevation) UnmarshalJSON(b []byte) error {
	if !utf8.Valid(b) {
		return fmt.Errorf("elevation must be UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("elevation must be an object")
	}
	seen := map[string]bool{}
	var result Elevation
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return fmt.Errorf("duplicate elevation member")
		}
		seen[key] = true
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("null elevation member %q", key)
		}
		switch key {
		case "system":
			if err := json.Unmarshal(raw, &result.System); err != nil {
				return err
			}
		case "grants":
			var grants []json.RawMessage
			if err := json.Unmarshal(raw, &grants); err != nil {
				return err
			}
			for _, grant := range grants {
				if bytes.Equal(bytes.TrimSpace(grant), []byte("null")) {
					return fmt.Errorf("null elevation grant")
				}
				var value string
				if err := json.Unmarshal(grant, &value); err != nil {
					return err
				}
				result.Grants = append(result.Grants, value)
			}
		default:
			return fmt.Errorf("unknown elevation member %q", key)
		}
	}
	if _, err := d.Token(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing elevation data")
	}
	*e = Normalize(result)
	return nil
}

// InheritedSelection reads transport metadata only; Core authenticates the bearer.
func InheritedSelection(token string) (Elevation, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Elevation{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Elevation{}, fmt.Errorf("handler metadata: %w", err)
	}
	var claims struct {
		Scope     string          `json:"scope"`
		Elevation json.RawMessage `json:"elevation"`
	}
	if err := json.Unmarshal(b, &claims); err != nil {
		return Elevation{}, fmt.Errorf("handler metadata: %w", err)
	}
	if claims.Scope != "handler" {
		return Elevation{}, nil
	}
	if len(claims.Elevation) == 0 {
		return Elevation{}, nil
	}
	var selection Elevation
	if err := json.Unmarshal(claims.Elevation, &selection); err != nil {
		return Elevation{}, fmt.Errorf("handler metadata: %w", err)
	}
	return Normalize(selection), nil
}
