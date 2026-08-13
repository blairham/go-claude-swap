package oauth

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPKCEAuthorizeURL(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if p.Verifier == "" || p.Challenge == "" || p.State == "" {
		t.Fatalf("incomplete PKCE: %+v", p)
	}
	p2, _ := NewPKCE()
	if p.Verifier == p2.Verifier || p.State == p2.State {
		t.Fatal("PKCE values must be unique per attempt")
	}

	u, err := url.Parse(p.AuthorizeRequestURL())
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for key, want := range map[string]string{
		"code":                  "true",
		"client_id":             ClientID,
		"response_type":         "code",
		"redirect_uri":          RedirectURI,
		"scope":                 AuthorizeScopes,
		"code_challenge":        p.Challenge,
		"code_challenge_method": "S256",
		"state":                 p.State,
	} {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

type exchangeStub struct {
	status  string
	gotForm url.Values
	gotCT   string
}

func (s *exchangeStub) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	s.gotForm, _ = url.ParseQuery(string(raw))
	s.gotCT = req.Header.Get("Content-Type")
	body := `{"access_token":"sk-ant-oat01-x","refresh_token":"sk-ant-ort01-x","expires_in":28800,` +
		`"scope":"user:inference user:profile",` +
		`"account":{"uuid":"acct-1","email_address":"a@b.co"},` +
		`"organization":{"uuid":"org-1","name":"Org"}}`
	if s.status != "" {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(s.status))}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestExchangeCode(t *testing.T) {
	p := &PKCE{Verifier: "ver", Challenge: "chal", State: "st"}
	stub := &exchangeStub{}
	client := &http.Client{Transport: stub}

	now := func() time.Time { return time.UnixMilli(1_000_000) }
	out := ExchangeCode(client, "  the-code#st  ", p, now)
	if out.Err != ErrNone {
		t.Fatalf("Err = %q", out.Err)
	}
	if stub.gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", stub.gotCT)
	}
	for key, want := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          "the-code",
		"redirect_uri":  RedirectURI,
		"client_id":     ClientID,
		"code_verifier": "ver",
		"state":         "st",
	} {
		if got := stub.gotForm.Get(key); got != want {
			t.Errorf("form %s = %q, want %q", key, got, want)
		}
	}
	if out.Blob.OAuth.AccessToken != "sk-ant-oat01-x" ||
		out.Blob.OAuth.RefreshToken != "sk-ant-ort01-x" ||
		out.Blob.OAuth.ExpiresAt != 1_000_000+28800*1000 {
		t.Errorf("blob = %+v", out.Blob.OAuth)
	}
	if out.Identity == nil || out.Identity.Email != "a@b.co" ||
		out.Identity.OrganizationUUID != "org-1" || out.Identity.OrganizationName != "Org" {
		t.Errorf("identity = %+v", out.Identity)
	}
}

func TestExchangeCodeStateMismatch(t *testing.T) {
	p := &PKCE{Verifier: "ver", State: "st"}
	client := &http.Client{Transport: &exchangeStub{}}
	if out := ExchangeCode(client, "code#other", p, time.Now); out.Err != ErrStateMismatch {
		t.Errorf("Err = %q, want state_mismatch", out.Err)
	}
	if out := ExchangeCode(client, "", p, time.Now); out.Err != ErrStateMismatch {
		t.Errorf("empty paste: Err = %q", out.Err)
	}
}

func TestExchangeCodeRejected(t *testing.T) {
	p := &PKCE{Verifier: "ver", State: "st"}
	client := &http.Client{Transport: &exchangeStub{status: `{"error":"invalid_grant"}`}}
	if out := ExchangeCode(client, "code#st", p, time.Now); out.Err != ErrInvalidGrant {
		t.Errorf("Err = %q, want invalid_grant", out.Err)
	}
}
