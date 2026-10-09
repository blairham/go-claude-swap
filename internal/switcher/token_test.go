package switcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// Byte-exact output of claude-swap's add_account_from_token
// (json.dumps with default separators) for the same inputs.
const (
	pySetupTokenCred = `{"claudeAiOauth": {"accessToken": "sk-ant-oat01-abc", "scopes": ["user:inference"]}}`
	pySetupTokenCfg  = `{"oauthAccount": {"emailAddress": "setup-token-1@token.local", "accountUuid": "", "organizationUuid": null, "organizationName": null}}`
)

func TestAddTokenSetupTokenMatchesOriginalStorage(t *testing.T) {
	env(t)
	res, err := AddToken("  sk-ant-oat01-abc\n", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Slot != 1 || res.Email != "setup-token-1@token.local" || res.APIKey || res.Updated {
		t.Fatalf("result = %+v", res)
	}
	cred, unreadable := credentials.ReadBackup(1, res.Email)
	if unreadable || cred != pySetupTokenCred {
		t.Fatalf("credential backup = %q", cred)
	}
	cfg, err := os.ReadFile(paths.AccountConfigBackup(1, res.Email))
	if err != nil || string(cfg) != pySetupTokenCfg {
		t.Fatalf("config backup = %q (%v)", cfg, err)
	}
	seq, _ := account.Load()
	a := seq.Get(1)
	if a == nil || a.Kind != "" || a.OrganizationUUID != "" {
		t.Fatalf("roster row = %+v", a)
	}
	if seq.ActiveAccountNumber != nil {
		t.Fatal("add-token must not mark the account active: it never touches the live login")
	}
}

func TestAddTokenAPIKey(t *testing.T) {
	env(t)
	login(t, os.Getenv("HOME"), "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	res, err := AddToken("sk-ant-api03-xyz", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Slot != 2 || res.Email != "api-key-2@token.local" || !res.APIKey {
		t.Fatalf("result = %+v", res)
	}
	if cred, _ := credentials.ReadBackup(2, res.Email); cred != "sk-ant-api03-xyz" {
		t.Fatalf("API key must be stored raw, got %q", cred)
	}
	seq, _ := account.Load()
	if seq.Get(2).Kind != account.KindAPIKey {
		t.Fatalf("roster row = %+v", seq.Get(2))
	}
}

func TestAddTokenRefreshesInPlace(t *testing.T) {
	env(t)
	if _, err := AddToken("sk-ant-oat01-one", "me@example.com", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SetAlias("me@example.com", "ci"); err != nil {
		t.Fatal(err)
	}
	res, err := AddToken("sk-ant-oat01-two", "me@example.com", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.Slot != 1 {
		t.Fatalf("result = %+v", res)
	}
	cred, _ := credentials.ReadBackup(1, "me@example.com")
	if !strings.Contains(cred, "sk-ant-oat01-two") {
		t.Fatalf("token not refreshed: %q", cred)
	}
	seq, _ := account.Load()
	if len(seq.Accounts) != 1 || seq.Get(1).Alias != "ci" {
		t.Fatalf("roster = %+v", seq.Accounts)
	}
}

func TestAddTokenRejectsCrossKindCollision(t *testing.T) {
	env(t)
	if _, err := AddToken("sk-ant-oat01-one", "me@example.com", 0); err != nil {
		t.Fatal(err)
	}
	_, err := AddToken("sk-ant-api03-key", "me@example.com", 0)
	if err == nil || !strings.Contains(err.Error(), "already exists as an OAuth account") {
		t.Fatalf("err = %v", err)
	}
	if cred, _ := credentials.ReadBackup(1, "me@example.com"); !strings.Contains(cred, "sk-ant-oat01-one") {
		t.Fatalf("OAuth account was overwritten: %q", cred)
	}
}

func TestAddTokenSlotHandling(t *testing.T) {
	env(t)
	if _, err := AddToken("sk-ant-oat01-a", "a@example.com", 0); err != nil {
		t.Fatal(err)
	}
	// An occupied slot is refused, never overwritten.
	if _, err := AddToken("sk-ant-oat01-b", "b@example.com", 1); err == nil {
		t.Fatal("overwrote an occupied slot")
	}
	// --slot on an existing identity moves it there.
	res, err := AddToken("sk-ant-oat01-a2", "a@example.com", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Slot != 5 || res.MovedFrom != 1 {
		t.Fatalf("result = %+v", res)
	}
	seq, _ := account.Load()
	if seq.Get(1) != nil || seq.Get(5) == nil {
		t.Fatalf("roster = %+v", seq.Accounts)
	}
	if cred, _ := credentials.ReadBackup(1, "a@example.com"); cred != "" {
		t.Fatalf("old slot's backup left behind: %q", cred)
	}
	if _, err := AddToken("", "", 0); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := AddToken("sk-ant-oat01-x", "not-an-email", 0); err == nil {
		t.Fatal("bad email accepted")
	}
}

// TestSwitchToSetupTokenAccount: a setup-token has no refresh token and no
// expiry, so activation must not attempt a refresh, and the live credential
// must carry the token.
func TestSwitchToSetupTokenAccount(t *testing.T) {
	home := env(t)
	stub := stubTokenEndpoint(t, nil)
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	res, err := AddToken("sk-ant-oat01-abc", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	sw, err := SwitchTo("2", false)
	if err != nil || !sw.Switched {
		t.Fatalf("switch = %+v, %v", sw, err)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("activation tried to refresh a setup-token: %v", stub.calls)
	}
	if got := liveOAuth(t)["accessToken"]; got != "sk-ant-oat01-abc" {
		t.Fatalf("live access token = %v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !strings.Contains(string(raw), res.Email) {
		t.Fatalf("live identity not spliced: %s", raw)
	}
	// And back: the outgoing setup-token is preserved, the OAuth account restored.
	if _, err := SwitchTo("1", false); err != nil {
		t.Fatal(err)
	}
	if cred, _ := credentials.ReadBackup(2, res.Email); !strings.Contains(cred, "sk-ant-oat01-abc") {
		t.Fatalf("setup-token backup lost on switch-away: %q", cred)
	}
}
