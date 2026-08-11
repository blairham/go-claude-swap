package switcher

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/claudecfg"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// env pins every store to a temp HOME on the file backend so tests never
// touch a real login or the macOS Keychain.
func env(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CSWAP_DISABLE_KEYCHAIN", "1")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	return home
}

// login simulates Claude Code's /login: it updates oauthAccount and the
// credential file but preserves the rest of an existing ~/.claude.json,
// exactly like the real client.
func login(t *testing.T, home, email, org, cred string, extraCfg map[string]any) {
	t.Helper()
	cfgPath := filepath.Join(home, ".claude.json")
	cfg := map[string]any{}
	if raw, err := os.ReadFile(cfgPath); err == nil {
		json.Unmarshal(raw, &cfg)
	}
	cfg["oauthAccount"] = map[string]any{
		"emailAddress":     email,
		"accountUuid":      "uuid-" + email,
		"organizationUuid": org,
		"organizationName": nil,
	}
	maps.Copy(cfg, extraCfg)
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(cred), 0o600); err != nil {
		t.Fatal(err)
	}
}

func credJSON(access, refresh string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","expiresAt":9999999999999}}`
}

func TestAddCapturesLogin(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)

	slot, email, err := Add(0, "work")
	if err != nil {
		t.Fatal(err)
	}
	if slot != 1 || email != "a@b.co" {
		t.Fatalf("Add = %d %s", slot, email)
	}

	seq, _ := account.Load()
	a := seq.Get(1)
	if a == nil || a.Alias != "work" || a.UUID != "uuid-a@b.co" {
		t.Fatalf("roster: %+v", a)
	}
	if n, _ := seq.Active(); n != 1 {
		t.Error("add must set the active pointer")
	}
	cred, unreadable := credentials.ReadBackup(1, "a@b.co")
	if unreadable || cred != credJSON("at-a", "rt-a") {
		t.Fatalf("backup = %q %v", cred, unreadable)
	}
	if _, err := os.Stat(paths.AccountConfigBackup(1, "a@b.co")); err != nil {
		t.Error("config backup missing")
	}
}

func TestAddDedupsByIdentity(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "", credJSON("at-1", "rt-a"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	// Same identity, rotated token: refresh in place, no second slot.
	login(t, home, "a@b.co", "", credJSON("at-2", "rt-a"), nil)
	slot, _, err := Add(0, "")
	if err != nil {
		t.Fatal(err)
	}
	if slot != 1 {
		t.Fatalf("dedup slot = %d", slot)
	}
	seq, _ := account.Load()
	if len(seq.Accounts) != 1 {
		t.Fatalf("accounts = %d", len(seq.Accounts))
	}
	cred, _ := credentials.ReadBackup(1, "a@b.co")
	if cred != credJSON("at-2", "rt-a") {
		t.Error("in-place refresh did not update the backup")
	}
}

func TestSwitchPreservesMachineState(t *testing.T) {
	home := env(t)

	// Account A logged in with machine state in config and MCP creds.
	credA := `{"claudeAiOauth":{"accessToken":"at-a","refreshToken":"rt-a","expiresAt":9999999999999},"mcpOAuth":{"server":"live"}}`
	login(t, home, "a@b.co", "", credA, map[string]any{
		"projects":   map[string]any{"/repo": map[string]any{"history": []any{"x"}}},
		"mcpServers": map[string]any{"srv": map[string]any{"url": "http://x"}},
	})
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}

	// Account B logged in.
	login(t, home, "b@b.co", "", credJSON("at-b", "rt-b"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}

	// Switch back to A.
	res, err := SwitchTo("1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Switched || res.ToSlot != 1 {
		t.Fatalf("result: %+v", res)
	}

	// Live credential is A's, with the live machine-shared keys intact.
	live := credentials.ReadActive()
	var blob map[string]any
	json.Unmarshal([]byte(live.Value), &blob)
	oauthObj := blob["claudeAiOauth"].(map[string]any)
	if oauthObj["accessToken"] != "at-a" {
		t.Errorf("live token = %v", oauthObj["accessToken"])
	}

	// Config: oauthAccount switched, machine state preserved.
	raw, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	oa := cfg["oauthAccount"].(map[string]any)
	if oa["emailAddress"] != "a@b.co" {
		t.Errorf("oauthAccount = %v", oa["emailAddress"])
	}
	if cfg["projects"] == nil || cfg["mcpServers"] == nil {
		t.Error("machine state was lost in the switch")
	}

	// B's credentials were backed up before being displaced.
	credB, _ := credentials.ReadBackup(2, "b@b.co")
	if credB != credJSON("at-b", "rt-b") {
		t.Errorf("outgoing backup = %q", credB)
	}

	// Roster active pointer moved.
	seq, _ := account.Load()
	if n, _ := seq.Active(); n != 1 {
		t.Errorf("active = %d", n)
	}

	id, _ := claudecfg.ReadIdentity()
	if id == nil || id.Email != "a@b.co" {
		t.Errorf("live identity = %+v", id)
	}
}

func TestSwitchAlreadyActiveIsNoOp(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	res, err := SwitchTo("1", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Switched || res.Reason != "already-active" {
		t.Fatalf("result: %+v", res)
	}
}

func TestSwitchRefusesWithoutBackup(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	seq, _ := account.Load()
	seq.Upsert(2, &account.Account{Email: "ghost@b.co"})
	seq.Save()

	if _, err := SwitchTo("2", false); err == nil {
		t.Fatal("switching to a slot without backups must fail")
	}
	// And the live login must be untouched.
	id, _ := claudecfg.ReadIdentity()
	if id == nil || id.Email != "a@b.co" {
		t.Error("failed switch mutated the live login")
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	if _, _, err := Add(0, "work"); err != nil {
		t.Fatal(err)
	}

	exportPath := filepath.Join(home, "export.json")
	n, err := Export(exportPath, "test", false, "")
	if err != nil || n != 1 {
		t.Fatalf("Export = %d, %v", n, err)
	}

	// Wipe and re-import.
	os.RemoveAll(paths.BackupRoot())
	res, err := Import(exportPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 {
		t.Fatalf("Import = %+v", res)
	}
	seq, _ := account.Load()
	a := seq.Get(1)
	if a == nil || a.Email != "a@b.co" || a.Alias != "work" {
		t.Fatalf("imported roster: %+v", a)
	}
	cred, _ := credentials.ReadBackup(1, "a@b.co")
	var blob map[string]any
	if json.Unmarshal([]byte(cred), &blob) != nil || blob["claudeAiOauth"] == nil {
		t.Fatalf("imported credential: %q", cred)
	}

	// Second import skips the existing account.
	res, err = Import(exportPath, false)
	if err != nil || res.Skipped != 1 {
		t.Fatalf("re-import = %+v, %v", res, err)
	}
}

func TestRotate(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	Add(0, "")
	login(t, home, "b@b.co", "", credJSON("at-b", "rt-b"), nil)
	Add(0, "")

	res, err := Rotate()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Switched || res.ToSlot != 1 {
		t.Fatalf("rotate: %+v", res)
	}

	// Disabled accounts are skipped.
	if _, _, err := SetDisabled("2", true); err != nil {
		t.Fatal(err)
	}
	res, err = Rotate()
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != "no-valid-target" {
		t.Fatalf("rotate past disabled: %+v", res)
	}
}
