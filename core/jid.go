// Package core's JID types: typed wire-form values that identify one
// resource on one platform. Wire form is `<platform>:<rest>` (RFC 3986
// opaque-path URI). The first segment of `<rest>` is the kind
// discriminator (e.g. `telegram:group/123`, `web:user/sub`); per-kind
// structure is the adapter's contract.
package core

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// JID wraps *url.URL for free RFC 3986 compliance and percent-encoding.
// The zero value is the empty JID; IsZero reports it.
type JID struct{ u *url.URL }

// ChatJID identifies a chat / destination resource.
type ChatJID struct{ JID }

// UserJID identifies a user identity resource.
type UserJID struct{ JID }

// ParseJID parses s as `<platform>:<rest>` and validates non-empty
// scheme + non-empty opaque path.
func ParseJID(s string) (JID, error) {
	if s == "" {
		return JID{}, errors.New("jid: empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return JID{}, fmt.Errorf("jid: %w", err)
	}
	if u.Scheme == "" {
		return JID{}, fmt.Errorf("jid: empty platform in %q", s)
	}
	if u.Opaque == "" {
		// `scheme://...` form — not what we want; require opaque path.
		return JID{}, fmt.Errorf("jid: %q is not opaque-path form (use scheme:rest, no //)", s)
	}
	return JID{u: u}, nil
}

// ParseChatJID parses s and validates the kind-discriminator segment.
func ParseChatJID(s string) (ChatJID, error) {
	j, err := ParseJID(s)
	if err != nil {
		return ChatJID{}, err
	}
	if err := j.validateKind(); err != nil {
		return ChatJID{}, err
	}
	return ChatJID{JID: j}, nil
}

// ParseUserJID parses s and validates the kind-discriminator segment.
func ParseUserJID(s string) (UserJID, error) {
	j, err := ParseJID(s)
	if err != nil {
		return UserJID{}, err
	}
	if err := j.validateKind(); err != nil {
		return UserJID{}, err
	}
	return UserJID{JID: j}, nil
}

// validateKind ensures the first path segment is non-empty.
func (j JID) validateKind() error {
	if j.u == nil {
		return errors.New("jid: empty")
	}
	if j.Kind() == "" {
		return fmt.Errorf("jid: %q has empty first segment", j.u.String())
	}
	return nil
}

// Platform returns the scheme (lowercase adapter name).
func (j JID) Platform() string {
	if j.u == nil {
		return ""
	}
	return j.u.Scheme
}

// Path returns the opaque rest, the adapter-private payload. Adapters
// split on "/" per their declared schema.
func (j JID) Path() string {
	if j.u == nil {
		return ""
	}
	return j.u.Opaque
}

// Kind returns the first path segment — the kind discriminator. Empty
// on a zero JID.
func (j JID) Kind() string {
	if j.u == nil {
		return ""
	}
	first, _, _ := strings.Cut(j.u.Opaque, "/")
	return first
}

// String returns the canonical wire form `<platform>:<rest>`.
func (j JID) String() string {
	if j.u == nil {
		return ""
	}
	return j.u.String()
}

// IsZero reports whether j is the zero JID (no URL parsed).
func (j JID) IsZero() bool { return j.u == nil }

// Scan implements database/sql.Scanner. Empty string scans to zero JID.
func (j *JID) Scan(src any) error {
	if src == nil {
		*j = JID{}
		return nil
	}
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("jid: unsupported scan type %T", src)
	}
	if s == "" {
		*j = JID{}
		return nil
	}
	parsed, err := ParseJID(s)
	if err != nil {
		return err
	}
	*j = parsed
	return nil
}

// Value implements database/sql/driver.Valuer. Zero JID stores as "".
func (j JID) Value() (driver.Value, error) {
	if j.u == nil {
		return "", nil
	}
	return j.u.String(), nil
}

// MarshalJSON emits the wire string. Zero JID emits "".
func (j JID) MarshalJSON() ([]byte, error) {
	return json.Marshal(j.String())
}

// UnmarshalJSON parses a wire string. Empty/null becomes zero JID.
func (j *JID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		*j = JID{}
		return nil
	}
	parsed, err := ParseJID(s)
	if err != nil {
		return err
	}
	*j = parsed
	return nil
}

// scanKindJID scans src into a base JID and validates the kind segment.
// Returns zero JID for empty input.
func scanKindJID(src any) (JID, error) {
	var j JID
	if err := j.Scan(src); err != nil {
		return JID{}, err
	}
	if !j.IsZero() {
		if err := j.validateKind(); err != nil {
			return JID{}, err
		}
	}
	return j, nil
}

// unmarshalKindJID unmarshals JSON into a base JID and validates the kind segment.
func unmarshalKindJID(data []byte) (JID, error) {
	var j JID
	if err := j.UnmarshalJSON(data); err != nil {
		return JID{}, err
	}
	if !j.IsZero() {
		if err := j.validateKind(); err != nil {
			return JID{}, err
		}
	}
	return j, nil
}

// Scan/Value/JSON for ChatJID + UserJID — same wire string, validate kind on parse.
func (c *ChatJID) Scan(src any) error {
	j, err := scanKindJID(src)
	if err != nil {
		return err
	}
	c.JID = j
	return nil
}

func (c ChatJID) Value() (driver.Value, error)  { return c.JID.Value() }
func (c ChatJID) MarshalJSON() ([]byte, error)   { return c.JID.MarshalJSON() }
func (c *ChatJID) UnmarshalJSON(data []byte) error {
	j, err := unmarshalKindJID(data)
	if err != nil {
		return err
	}
	c.JID = j
	return nil
}

func (u *UserJID) Scan(src any) error {
	j, err := scanKindJID(src)
	if err != nil {
		return err
	}
	u.JID = j
	return nil
}

func (u UserJID) Value() (driver.Value, error)  { return u.JID.Value() }
func (u UserJID) MarshalJSON() ([]byte, error)   { return u.JID.MarshalJSON() }
func (u *UserJID) UnmarshalJSON(data []byte) error {
	j, err := unmarshalKindJID(data)
	if err != nil {
		return err
	}
	u.JID = j
	return nil
}

// MatchJID reports whether value matches pattern under path.Match
// glob semantics (`*` does not cross `/`). Both pattern and value
// are full canonical strings (`<scheme>:<rest>`).
func MatchJID(pattern, value string) bool {
	ok, err := path.Match(pattern, value)
	if err != nil {
		return false
	}
	return ok
}
