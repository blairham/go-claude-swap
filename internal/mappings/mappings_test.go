package mappings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// env pins the backup root to a temp HOME.
func env(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSetGetExact(t *testing.T) {
	home := env(t)
	repo := mkdir(t, home, "work", "app")
	s := Open()
	if _, prev, err := s.Set(repo, "work@co.com", "org-1"); err != nil || prev != nil {
		t.Fatalf("Set: prev=%v err=%v", prev, err)
	}
	e, err := s.Get(repo)
	if err != nil || e == nil {
		t.Fatalf("Get: %v %v", e, err)
	}
	if e.Email != "work@co.com" || e.OrganizationUUID != "org-1" || e.Added == "" {
		t.Fatalf("entry = %+v", e)
	}
	if e, _ := s.Get(filepath.Join(home, "nope")); e != nil {
		t.Fatalf("unmapped Get = %+v", e)
	}
	_, prev, _ := s.Set(repo, "other@co.com", "")
	if prev == nil || prev.Email != "work@co.com" {
		t.Fatalf("remap prev = %+v", prev)
	}
}

func TestResolveNearestAncestor(t *testing.T) {
	home := env(t)
	outer := mkdir(t, home, "work")
	inner := mkdir(t, outer, "client")
	deep := mkdir(t, inner, "src", "deep")
	sibling := mkdir(t, home, "workshop")
	s := Open()
	s.Set(outer, "outer@x.com", "")
	s.Set(inner, "inner@x.com", "")

	cases := map[string]string{
		outer:   "outer@x.com",
		inner:   "inner@x.com",
		deep:    "inner@x.com",
		sibling: "", // /work must not claim /workshop
		home:    "",
	}
	for dir, want := range cases {
		_, e, ok, err := s.Resolve(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := map[bool]string{true: e.Email}[ok]; got != want {
			t.Errorf("Resolve(%s) = %q, want %q", dir, got, want)
		}
	}
}

func TestResolveFollowsSymlinks(t *testing.T) {
	home := env(t)
	real := mkdir(t, home, "real")
	link := filepath.Join(home, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	s := Open()
	s.Set(link, "a@x.com", "")
	if _, e, ok, _ := s.Resolve(filepath.Join(real)); !ok || e.Email != "a@x.com" {
		t.Fatalf("mapping made via a symlink does not cover its target")
	}
}

func TestNormalizePathRelativeAndTilde(t *testing.T) {
	home := env(t)
	work := mkdir(t, home, "w")
	t.Chdir(work)
	dot, err := NormalizePath(".")
	if err != nil {
		t.Fatal(err)
	}
	tilde, _ := NormalizePath("~/w")
	abs, _ := NormalizePath(work)
	if dot != abs || tilde != abs {
		t.Fatalf(". = %q, ~/w = %q, abs = %q", dot, tilde, abs)
	}
}

func TestRemoveAndPrune(t *testing.T) {
	home := env(t)
	a := mkdir(t, home, "a")
	b := mkdir(t, home, "b")
	c := mkdir(t, home, "c")
	s := Open()
	s.Set(a, "x@x.com", "org")
	s.Set(b, "x@x.com", "org")
	s.Set(c, "x@x.com", "") // same email, other org: a different identity

	if _, ok, _ := s.Remove(filepath.Join(home, "none")); ok {
		t.Fatal("removed a mapping that does not exist")
	}
	if _, ok, _ := s.Remove(a); !ok {
		t.Fatal("Remove(a) found nothing")
	}
	n, err := s.PruneAccount("x@x.com", "org")
	if err != nil || n != 1 {
		t.Fatalf("PruneAccount = %d, %v", n, err)
	}
	all, _ := s.All()
	if len(all) != 1 {
		t.Fatalf("left %v, want only the other-org mapping", all)
	}
}

// TestReadsPythonFormat loads a file byte-for-byte as claude-swap's
// MappingStore writes it (json.dumps indent=2, ASCII-escaped).
func TestReadsPythonFormat(t *testing.T) {
	home := env(t)
	fixture, err := os.ReadFile(filepath.Join("testdata", "python-mappings.json"))
	if err != nil {
		t.Fatal(err)
	}
	dst := Open().path
	if rel, err := filepath.Rel(home, dst); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("store path %s escapes the temp HOME", dst)
	}
	os.MkdirAll(filepath.Dir(dst), 0o700)
	if err := os.WriteFile(dst, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	all, err := Open().All()
	if err != nil {
		t.Fatal(err)
	}
	if e := all["/home/u/work"]; e.Email != "work@co.com" || e.OrganizationUUID != "org-1" || e.Added != "2026-01-02T03:04:05Z" {
		t.Fatalf("work entry = %+v", e)
	}
	if e := all["/home/u/work/client"]; e.Email != "josé@x.com" {
		t.Fatalf("client entry = %+v", e)
	}

	// A prune rewrites the file; the result must stay in the shape
	// claude-swap reads.
	if n, err := Open().PruneAccount("work@co.com", "org-1"); err != nil || n != 1 {
		t.Fatalf("prune = %d %v", n, err)
	}
	raw, _ := os.ReadFile(dst)
	var doc struct {
		SchemaVersion int                          `json:"schemaVersion"`
		Mappings      map[string]map[string]string `json:"mappings"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 1 || len(doc.Mappings) != 1 || doc.Mappings["/home/u/work/client"]["email"] != "josé@x.com" {
		t.Fatalf("rewritten doc = %s", raw)
	}
}

// TestRewritePreservesUnknownFields keeps fields a newer claude-swap might
// add to entries this tool did not touch.
func TestRewritePreservesUnknownFields(t *testing.T) {
	home := env(t)
	s := Open()
	os.MkdirAll(filepath.Dir(s.path), 0o700)
	os.WriteFile(s.path, []byte(`{"schemaVersion":1,"mappings":{"/keep":{"email":"k@x.com","organizationUuid":"","added":"t","future":42}}}`), 0o600)
	if _, _, err := s.Set(mkdir(t, home, "new"), "n@x.com", ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.path)
	var doc document
	json.Unmarshal(raw, &doc)
	if string(doc.Mappings["/keep"]["future"]) != "42" {
		t.Fatalf("unknown field lost: %s", raw)
	}
}

// TestCorruptFileIsNeverOverwritten: an unreadable table must not be
// treated as empty, or the next write would silently discard it.
func TestCorruptFileIsNeverOverwritten(t *testing.T) {
	home := env(t)
	s := Open()
	os.MkdirAll(filepath.Dir(s.path), 0o700)
	corrupt := []byte(`{"schemaVersion":1,"mappings":{`)
	os.WriteFile(s.path, corrupt, 0o600)

	if _, _, err := s.Set(mkdir(t, home, "d"), "a@x.com", ""); err == nil {
		t.Fatal("Set over a corrupt file succeeded")
	}
	if _, err := s.PruneAccount("a@x.com", ""); err == nil {
		t.Fatal("PruneAccount over a corrupt file succeeded")
	}
	if got, _ := os.ReadFile(s.path); string(got) != string(corrupt) {
		t.Fatalf("corrupt file rewritten: %s", got)
	}
}
