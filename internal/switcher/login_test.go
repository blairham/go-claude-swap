package switcher

import (
	"strings"
	"testing"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/claudecfg"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/oauth"
)

func identity(email string) *oauth.Identity {
	return &oauth.Identity{UUID: "uuid-" + email, Email: email}
}

func TestStoreLoginRepairsExistingSlot(t *testing.T) {
	home := env(t)
	login(t, home, "b@b.co", "", credJSON("at-b", "rt-b"), nil)
	Add(0, "")
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	Add(0, "")

	res, err := StoreLogin("1", credJSON("at-new", "rt-new"), identity("b@b.co"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Slot != 1 || res.Created || res.Activated {
		t.Fatalf("result: %+v", res)
	}
	backup, _ := credentials.ReadBackup(1, "b@b.co")
	if backup != credJSON("at-new", "rt-new") {
		t.Errorf("backup = %q", backup)
	}
	// The live login (a@b.co) must be untouched.
	id, _ := claudecfg.ReadIdentity()
	if id == nil || id.Email != "a@b.co" {
		t.Error("re-login of an inactive account mutated the live login")
	}
}

func TestStoreLoginActivatesWhenAccountIsLive(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	Add(0, "")

	res, err := StoreLogin("", credJSON("at-new", "rt-new"), identity("a@b.co"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Slot != 1 || !res.Activated || res.Created {
		t.Fatalf("result: %+v", res)
	}
	live := credentials.ReadActive()
	if !strings.Contains(live.Value, "at-new") {
		t.Errorf("live credential not replaced: %q", live.Value)
	}
}

func TestStoreLoginAddsNewAccount(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	Add(0, "")

	res, err := StoreLogin("", credJSON("at-c", "rt-c"), identity("c@b.co"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Slot != 2 || !res.Created || res.Activated {
		t.Fatalf("result: %+v", res)
	}
	seq, _ := account.Load()
	rec := seq.Get(2)
	if rec == nil || rec.Email != "c@b.co" || rec.UUID != "uuid-c@b.co" {
		t.Fatalf("roster: %+v", rec)
	}
	// The new slot must be a complete switch target.
	if !Switchable(2, rec) {
		t.Error("new login is not switchable")
	}
	// And the live login must be untouched.
	id, _ := claudecfg.ReadIdentity()
	if id == nil || id.Email != "a@b.co" {
		t.Error("adding an account mutated the live login")
	}
}

func TestStoreLoginRejectsWrongAccount(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	Add(0, "")
	login(t, home, "b@b.co", "", credJSON("at-b", "rt-b"), nil)
	Add(0, "")

	// Asked to repair Account-1 but logged in as b@b.co (Account-2).
	if _, err := StoreLogin("1", credJSON("at-x", "rt-x"), identity("b@b.co")); err == nil {
		t.Fatal("a login for the wrong account must be rejected")
	}
	// Asked for Account-1 with an identity that is not managed at all.
	if _, err := StoreLogin("1", credJSON("at-x", "rt-x"), identity("stranger@b.co")); err == nil {
		t.Fatal("a login for an unmanaged identity must not overwrite the slot")
	}
	backup, _ := credentials.ReadBackup(1, "a@b.co")
	if backup != credJSON("at-a", "rt-a") {
		t.Errorf("Account-1's backup was touched: %q", backup)
	}
}
