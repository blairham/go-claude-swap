package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// AuthorizeURL is the OAuth authorization endpoint used by Claude Code.
	AuthorizeURL = "https://claude.ai/oauth/authorize"
	// RedirectURI is the paste-a-code callback page: the browser lands there
	// and shows the user a code to paste back into the terminal.
	RedirectURI = "https://platform.claude.com/oauth/code/callback"
	// AuthorizeScopes are the scopes Claude Code requests at login.
	AuthorizeScopes = "user:inference user:profile user:sessions:claude_code user:mcp_servers"

	// ErrStateMismatch is permanent for this attempt: the pasted code came
	// from a different login attempt than the one that built the URL.
	ErrStateMismatch ErrorKind = "state_mismatch"
)

// PKCE holds the per-attempt secrets of an authorization-code flow.
type PKCE struct {
	Verifier  string
	Challenge string
	State     string
}

// NewPKCE generates a fresh verifier/challenge pair and state value.
func NewPKCE() (*PKCE, error) {
	verifier, err := randomToken()
	if err != nil {
		return nil, err
	}
	state, err := randomToken()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	return &PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
		State:     state,
	}, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// AuthorizeRequestURL builds the browser URL for this login attempt.
// code=true selects the paste-a-code flow (no local callback server).
func (p *PKCE) AuthorizeRequestURL() string {
	q := url.Values{
		"code":                  {"true"},
		"client_id":             {ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {RedirectURI},
		"scope":                 {AuthorizeScopes},
		"code_challenge":        {p.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {p.State},
	}
	return AuthorizeURL + "?" + q.Encode()
}

// ExchangeCode swaps a pasted authorization code (formatted "CODE#STATE",
// bare code tolerated) for tokens. The body is form-encoded — the endpoint
// stalls on JSON bodies for this grant type.
func ExchangeCode(client *http.Client, pasted string, p *PKCE, now func() time.Time) Outcome {
	code, returnedState, _ := strings.Cut(strings.TrimSpace(pasted), "#")
	if code == "" {
		return Outcome{Err: ErrStateMismatch}
	}
	if returnedState != "" && returnedState != p.State {
		return Outcome{Err: ErrStateMismatch}
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {RedirectURI},
		"client_id":     {ClientID},
		"code_verifier": {p.Verifier},
		"state":         {p.State},
	}
	req, err := http.NewRequest(http.MethodPost, TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Outcome{Err: ErrTransient}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	payload, ok := decodeTokenPayload(resp.Body)
	if !ok {
		return Outcome{Err: ErrTransient}
	}

	blob := &Blob{
		Extra: map[string]json.RawMessage{},
		OAuth: &Creds{Extra: map[string]json.RawMessage{}},
	}
	blob.OAuth.AccessToken = payload.AccessToken
	blob.OAuth.RefreshToken = payload.RefreshToken
	if payload.ExpiresIn > 0 {
		blob.OAuth.ExpiresAt = now().UnixMilli() + payload.ExpiresIn*1000
	}
	if payload.Scope != "" {
		blob.OAuth.Scopes = strings.Fields(payload.Scope)
	}
	return Outcome{Blob: blob, Identity: payload.identity()}
}
