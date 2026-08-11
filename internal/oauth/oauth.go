// Package oauth implements the Claude OAuth token-refresh flow used when
// activating or polling inactive accounts. It operates on the same
// credential blob format Claude Code itself stores ({"claudeAiOauth": ...}).
package oauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// TokenURL is the OAuth token endpoint used by Claude Code.
	TokenURL = "https://platform.claude.com/v1/oauth/token"
	// ClientID is the OAuth client_id constant used by Claude Code.
	ClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// UserAgent identifies this tool; the usage endpoint budgets requests
	// per UA-class, so this string is part of the budget identity.
	UserAgent = "claude-swap/1.0"
	// ExpiryBuffer: a token within this margin of expiry counts as expired.
	ExpiryBuffer = 5 * time.Minute

	tokenTimeout = 10 * time.Second
)

// ErrorKind classifies refresh failures.
type ErrorKind string

const (
	// ErrNone means the refresh succeeded.
	ErrNone ErrorKind = ""
	// ErrTransient covers network errors, 5xx, and unparseable responses.
	ErrTransient ErrorKind = "transient"
	// ErrNoRefreshToken is permanent: the blob has no usable refresh token.
	ErrNoRefreshToken ErrorKind = "no_refresh_token"
	// ErrInvalidGrant is permanent: the refresh lineage is dead; re-login needed.
	ErrInvalidGrant ErrorKind = "invalid_grant"
	// ErrInvalidClient is systemic (client_id rejected); no per-account strike.
	ErrInvalidClient ErrorKind = "invalid_client"
)

// Permanent reports whether the error kind condemns the credential
// (as opposed to being retryable or systemic).
func (k ErrorKind) Permanent() bool {
	return k == ErrNoRefreshToken || k == ErrInvalidGrant
}

// Creds is the inner claudeAiOauth object of Claude Code's credential blob.
type Creds struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"` // epoch milliseconds
	Scopes       []string `json:"scopes,omitempty"`
	// Extra preserves fields we don't model so round-trips are lossless.
	Extra map[string]json.RawMessage `json:"-"`
}

// Blob is the top-level credential document.
type Blob struct {
	OAuth *Creds `json:"claudeAiOauth"`
	// Extra preserves unknown top-level keys (e.g. MCP OAuth state).
	Extra map[string]json.RawMessage `json:"-"`
}

// Identity is the optional account identity returned by the token endpoint.
type Identity struct {
	UUID             string
	Email            string
	OrganizationUUID string
}

// Outcome is the result of a refresh attempt.
type Outcome struct {
	Blob     *Blob // updated blob on success, nil otherwise
	Identity *Identity
	Err      ErrorKind
}

// ParseBlob decodes a credential document, preserving unknown keys.
func ParseBlob(raw []byte) (*Blob, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	b := &Blob{Extra: map[string]json.RawMessage{}}
	for k, v := range top {
		if k != "claudeAiOauth" {
			b.Extra[k] = v
			continue
		}
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(v, &inner); err != nil {
			return nil, fmt.Errorf("claudeAiOauth: %w", err)
		}
		c := &Creds{Extra: map[string]json.RawMessage{}}
		for ik, iv := range inner {
			switch ik {
			case "accessToken":
				_ = json.Unmarshal(iv, &c.AccessToken)
			case "refreshToken":
				_ = json.Unmarshal(iv, &c.RefreshToken)
			case "expiresAt":
				_ = json.Unmarshal(iv, &c.ExpiresAt)
			case "scopes":
				_ = json.Unmarshal(iv, &c.Scopes)
			default:
				c.Extra[ik] = iv
			}
		}
		b.OAuth = c
	}
	return b, nil
}

// MarshalBlob encodes a credential document, restoring preserved keys.
func MarshalBlob(b *Blob) ([]byte, error) {
	top := map[string]any{}
	for k, v := range b.Extra {
		top[k] = v
	}
	if b.OAuth != nil {
		inner := map[string]any{}
		for k, v := range b.OAuth.Extra {
			inner[k] = v
		}
		inner["accessToken"] = b.OAuth.AccessToken
		inner["refreshToken"] = b.OAuth.RefreshToken
		inner["expiresAt"] = b.OAuth.ExpiresAt
		if b.OAuth.Scopes != nil {
			inner["scopes"] = b.OAuth.Scopes
		}
		top["claudeAiOauth"] = inner
	}
	return json.Marshal(top)
}

// Expired reports whether the token is within ExpiryBuffer of expiry.
// A zero/absent expiresAt is treated as not expired, matching claude-swap.
func Expired(expiresAtMS int64, now time.Time) bool {
	if expiresAtMS == 0 {
		return false
	}
	return now.UnixMilli()+ExpiryBuffer.Milliseconds() >= expiresAtMS
}

// Fingerprint identifies a credential generation: the refresh token when
// present, otherwise the whole document. Strikes bind to this, not to slots.
func Fingerprint(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if b, err := ParseBlob(raw); err == nil && b.OAuth != nil && b.OAuth.RefreshToken != "" {
		sum := sha256.Sum256([]byte(b.OAuth.RefreshToken))
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(raw)
	return "sha256-full:" + hex.EncodeToString(sum[:])
}

// Refresh exchanges the blob's refresh token for a new access token.
// On success the returned Outcome carries an updated copy of the blob.
func Refresh(client *http.Client, raw []byte, now func() time.Time) Outcome {
	blob, err := ParseBlob(raw)
	if err != nil {
		return Outcome{Err: ErrTransient}
	}
	if blob.OAuth == nil || blob.OAuth.RefreshToken == "" {
		return Outcome{Err: ErrNoRefreshToken}
	}

	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": blob.OAuth.RefreshToken,
		"client_id":     ClientID,
	})
	req, err := http.NewRequest(http.MethodPost, TokenURL, bytes.NewReader(body))
	if err != nil {
		return Outcome{Err: ErrTransient}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	c := *client
	c.Timeout = tokenTimeout
	resp, err := c.Do(req)
	if err != nil {
		return Outcome{Err: ErrTransient}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Outcome{Err: classifyTokenError(resp)}
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		Account      *struct {
			UUID  string `json:"uuid"`
			Email string `json:"email_address"`
		} `json:"account"`
		Organization *struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil || payload.AccessToken == "" {
		return Outcome{Err: ErrTransient}
	}

	blob.OAuth.AccessToken = payload.AccessToken
	if payload.ExpiresIn > 0 {
		blob.OAuth.ExpiresAt = now().UnixMilli() + payload.ExpiresIn*1000
	}
	if payload.RefreshToken != "" {
		blob.OAuth.RefreshToken = payload.RefreshToken
	}
	if payload.Scope != "" {
		blob.OAuth.Scopes = strings.Fields(payload.Scope)
	}

	out := Outcome{Blob: blob}
	if payload.Account != nil && payload.Account.UUID != "" {
		out.Identity = &Identity{UUID: payload.Account.UUID, Email: payload.Account.Email}
		if payload.Organization != nil {
			out.Identity.OrganizationUUID = payload.Organization.UUID
		}
	}
	return out
}

// classifyTokenError follows RFC 6749 §5.2: read the top-level "error"
// member of the JSON body; never substring-scan.
func classifyTokenError(resp *http.Response) ErrorKind {
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
	default:
		return ErrTransient
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ErrTransient
	}
	switch body.Error {
	case "invalid_grant":
		return ErrInvalidGrant
	case "invalid_client":
		return ErrInvalidClient
	default:
		return ErrTransient
	}
}

// ErrPermanent wraps a permanent refresh failure for callers using errors.Is.
var ErrPermanent = errors.New("credential requires re-login")
