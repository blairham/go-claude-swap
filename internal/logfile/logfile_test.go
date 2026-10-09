package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestRotatesBySizeAndKeepsN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cswap-auto.log")
	lf, err := Open(path, 20, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()

	for _, line := range []string{"aaaaaaaaa\n", "bbbbbbbbb\n", "ccccccccc\n", "ddddddddd\n", "eeeeeeeee\n"} {
		if _, err := lf.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	// 10-byte lines, 20-byte cap: two per file, newest at path.
	if got := read(t, path); got != "eeeeeeeee\n" {
		t.Fatalf("current: %q", got)
	}
	if got := read(t, path+".1"); got != "ccccccccc\nddddddddd\n" {
		t.Fatalf(".1: %q", got)
	}
	if got := read(t, path+".2"); got != "aaaaaaaaa\nbbbbbbbbb\n" {
		t.Fatalf(".2: %q", got)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("keep=2 must not leave a .3 (err=%v)", err)
	}

	// One more rotation drops the oldest generation.
	lf.Write([]byte("fffffffff\n"))
	lf.Write([]byte("ggggggggg\n"))
	if got := read(t, path+".2"); got != "ccccccccc\nddddddddd\n" {
		t.Fatalf(".2 after shift: %q", got)
	}
}

func TestOpenRotatesOversizedExistingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cswap-auto.log")
	old := strings.Repeat("x", 100)
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	lf, err := Open(path, 50, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if got := read(t, path+".1"); got != old {
		t.Fatalf("oversized log should move to .1; got %d bytes", len(got))
	}
	if got := read(t, path); got != "" {
		t.Fatalf("fresh log should be empty, got %q", got)
	}
}

func TestAppendsToExistingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cswap-auto.log")
	os.WriteFile(path, []byte("before\n"), 0o644)
	lf, err := Open(path, 1<<20, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	lf.Write([]byte("after\n"))
	lf.Close()
	if got := read(t, path); got != "before\nafter\n" {
		t.Fatalf("got %q", got)
	}
}

func TestOnOpenRunsOnEveryReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cswap-auto.log")
	var opened []string
	lf, err := Open(path, 10, 3, func(f *os.File) error {
		opened = append(opened, f.Name())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	lf.Write([]byte("123456789\n"))
	lf.Write([]byte("123456789\n")) // rotates
	if len(opened) != 2 {
		t.Fatalf("OnOpen must run at open and after rotation; ran %d times", len(opened))
	}
}
