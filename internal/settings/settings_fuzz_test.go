// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/blairham/go-claude-swap/internal/paths"
)

// checkInSpec fails when v is not a value of spec's kind inside its bounds.
func checkInSpec(t *testing.T, spec *Spec, v any, from string) {
	t.Helper()
	switch spec.Kind {
	case KindFloat:
		f, ok := v.(float64)
		if !ok || math.IsNaN(f) || f < spec.Lo || f > spec.Hi {
			t.Fatalf("%s: %s = %#v, want a float in [%v, %v]", from, spec.Key, v, spec.Lo, spec.Hi)
		}
	case KindInt:
		n, ok := v.(int)
		if !ok || float64(n) < spec.Lo || float64(n) > spec.Hi {
			t.Fatalf("%s: %s = %#v, want an int in [%v, %v]", from, spec.Key, v, spec.Lo, spec.Hi)
		}
	case KindBool:
		if _, ok := v.(bool); !ok {
			t.Fatalf("%s: %s = %#v, want a bool", from, spec.Key, v)
		}
	case KindString:
		if s, ok := v.(string); !ok || s == "" {
			t.Fatalf("%s: %s = %#v, want a non-empty string", from, spec.Key, v)
		}
	case KindChoice:
		if s, ok := v.(string); !ok || !slices.Contains(spec.Choices, s) {
			t.Fatalf("%s: %s = %#v, want one of %v", from, spec.Key, v, spec.Choices)
		}
	}
}

// FuzzParseStrict checks `cswap config set`'s contract: a value ParseStrict
// accepts is in bounds for its key, and once written it is exactly what the
// forgiving loader — which every other command uses — reads back. A value
// the loader would clamp or discard must have been refused at the prompt.
func FuzzParseStrict(f *testing.F) {
	for i, v := range []string{"85.5", "50", "99.9", "100", "NaN", "-0", "1e1", "yes", "Fable,Opus", " ", "light", "inf"} {
		f.Add(uint8(i), v)
	}
	f.Add(uint8(0), "NaN")       // autoswitch.threshold: NaN passes both bound checks
	f.Add(uint8(7), "Fable\xff") // autoswitch.model: not UTF-8
	root := f.TempDir()
	f.Setenv("HOME", root)
	f.Setenv("XDG_DATA_HOME", filepath.Join(root, "share"))

	f.Fuzz(func(t *testing.T, idx uint8, value string) {
		spec := &Registry[int(idx)%len(Registry)]
		v, err := ParseStrict(spec.Key, value)
		if err != nil {
			return
		}
		checkInSpec(t, spec, v, "ParseStrict("+value+")")

		_ = os.Remove(paths.SettingsPath())
		if err := SetKey(spec.Key, v); err != nil {
			t.Fatalf("SetKey(%s, %#v) after ParseStrict accepted %q: %v", spec.Key, v, value, err)
		}
		got := Load()
		if !got.IsSet(spec.Key) {
			t.Fatalf("%s written but not read back as set", spec.Key)
		}
		if g := got.Get(spec.Key); g != v {
			t.Fatalf("config set %s %q stored %#v, Load reads %#v", spec.Key, value, v, g)
		}
	})
}

// FuzzLoad feeds arbitrary settings.json content to the forgiving loader:
// whatever is on disk, every registered key comes back as a value of its
// kind, inside its bounds.
func FuzzLoad(f *testing.F) {
	f.Add([]byte(`{"schemaVersion":1,"autoswitch":{"threshold":200,"unhealthyTicks":0.5,"strategy":"best"}}`))
	f.Add([]byte(`{"autoswitch":{"model":"","notify":"loud","includeApiKeyAccounts":"yes"},"ui":{"theme":"light"}}`))
	f.Add([]byte(`{"autoswitch":null,"ui":[]}`))
	f.Add([]byte(`{"autoswitch":{"intervalSeconds":-1e400}}`))
	f.Add([]byte(`not json`))
	root := f.TempDir()
	f.Setenv("HOME", root)
	f.Setenv("XDG_DATA_HOME", filepath.Join(root, "share"))
	if err := os.MkdirAll(filepath.Dir(paths.SettingsPath()), 0o700); err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, doc []byte) {
		if err := os.WriteFile(paths.SettingsPath(), doc, 0o600); err != nil {
			t.Fatal(err)
		}
		s := Load()
		for i := range Registry {
			checkInSpec(t, &Registry[i], s.Get(Registry[i].Key), "Load")
		}
	})
}
