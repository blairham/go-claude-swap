// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package usage

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// FuzzParseUsage feeds arbitrary bodies to the usage-endpoint parser. The
// body comes from the network, and what the parser accepts is cached and
// read back by every other cswap process (the TUI goes store-only while the
// service runs), so an accepted snapshot must have a window to decide on and
// must survive the cache's JSON round trip unchanged.
func FuzzParseUsage(f *testing.F) {
	f.Add(`{"five_hour":{"utilization":42.5,"resets_at":"2026-10-09T05:00:00Z"},` +
		`"seven_day":{"utilization":12,"resets_at":null}}`)
	f.Add(`{"extra_usage":{"is_enabled":true,"used_credits":1250,"monthly_limit":5000,` +
		`"utilization":25,"currency":""}}`)
	f.Add(`{"limits":[{"scope":{"model":{"display_name":"Fable"}},"percent":99.5,` +
		`"resets_at":"2026-10-12T00:00:00Z"},{"scope":null,"percent":1}]}`)
	f.Add(`{"limits":[{"scope":{"model":{"display_name":""}},"percent":5}]}`)
	f.Add(`{}`)
	f.Add(`null`)
	f.Add(`{"five_hour":{"utilization":"high"}}`)
	f.Add(`[]`)

	f.Fuzz(func(t *testing.T, body string) {
		u, fe := parseUsage(strings.NewReader(body))
		if (u == nil) == (fe == nil) {
			t.Fatalf("parseUsage(%q) = %v, %v: want exactly one of usage and error", body, u, fe)
		}
		if fe != nil {
			if fe.Kind != "bad-response" {
				t.Fatalf("parse failure kind = %q, want bad-response", fe.Kind)
			}
			return
		}
		if u.FiveHour == nil && u.SevenDay == nil && u.Spend == nil && len(u.Scoped) == 0 {
			t.Fatalf("accepted a snapshot with no window: %q", body)
		}
		for _, w := range u.Scoped {
			if w.Name == "" {
				t.Fatalf("accepted a scoped window with no model name: %q", body)
			}
		}

		data, err := json.Marshal(u)
		if err != nil {
			t.Fatalf("cache encode of an accepted snapshot: %v", err)
		}
		var back Usage
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("cache decode: %v\n%s", err, data)
		}
		if !reflect.DeepEqual(u, &back) {
			t.Fatalf("cache round trip changed the snapshot\nparsed: %+v\ncached: %+v", u, &back)
		}

		// Headroom is 100 minus the worst relevant window.
		if h, ok := u.Headroom([]string{"all"}); ok {
			for _, w := range u.RelevantWindows([]string{"all"}) {
				if !math.IsNaN(h) && h > 100-w.Pct {
					t.Fatalf("headroom %v exceeds 100-%v for window %q", h, w.Pct, w.Name)
				}
			}
		}
	})
}
