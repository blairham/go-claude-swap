// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package autoswitch

import (
	"math"
	"strings"
	"testing"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/switcher"
	"github.com/blairham/go-claude-swap/internal/usage"
)

func TestUnknownCause(t *testing.T) {
	good := &usage.Usage{FiveHour: &usage.Window{Pct: 40}}
	inf := math.Inf(1)
	cases := []struct {
		name string
		snap switcher.Snapshot
		want string // substring; "" means the cause must be empty
	}{
		{"known", switcher.Snapshot{Status: switcher.StatusOK, Usage: good}, ""},
		{
			"429",
			switcher.Snapshot{Status: switcher.StatusUnavailable, LastErr: "http-429", Age: inf},
			"rate limited (HTTP 429)",
		},
		{
			"429 with stale data",
			switcher.Snapshot{Status: switcher.StatusUnavailable, LastErr: "http-429", LastGood: good, Age: 47 * 60},
			"rate limited (HTTP 429); last good data 47m old",
		},
		{"network", switcher.Snapshot{Status: switcher.StatusUnavailable, LastErr: "network", Age: inf}, "network error"},
		{"timeout", switcher.Snapshot{Status: switcher.StatusUnavailable, LastErr: "timeout", Age: inf}, "timed out"},
		{"http 503", switcher.Snapshot{Status: switcher.StatusUnavailable, LastErr: "http-503", Age: inf}, "HTTP 503"},
		{
			"stale cache",
			switcher.Snapshot{Status: switcher.StatusUnavailable, LastGood: good, Age: 3 * 3600},
			"cached usage too stale to decide on (last good data 3h old)",
		},
		{"never fetched", switcher.Snapshot{Status: switcher.StatusUnavailable, Age: inf}, "no usage data yet"},
		{"expired token", switcher.Snapshot{Status: switcher.StatusTokenExpired}, "expired"},
		{"relogin", switcher.Snapshot{Status: switcher.StatusReloginRequired}, "re-login"},
		{"keychain", switcher.Snapshot{Status: switcher.StatusKeychainUnavailable}, "unreadable"},
		{"no creds", switcher.Snapshot{Status: switcher.StatusNoCredentials}, "no stored credential"},
		{"no window", switcher.Snapshot{Status: switcher.StatusOK, Usage: &usage.Usage{}}, "no usage window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unknownCause(&tc.snap, nil)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("want no cause, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestPollAndNoSwitchCarryUnknownCause(t *testing.T) {
	rec := &recordSink{}
	e := &Engine{Config: Config{Threshold: 90, UnhealthyTicks: 3}, Sink: rec}
	good := &usage.Usage{FiveHour: &usage.Window{Pct: 40}}
	snaps := []switcher.Snapshot{
		{
			Slot:    1,
			Account: &account.Account{Email: "a@example.com"},
			Active:  true,
			Status:  switcher.StatusUnavailable,
			LastErr: "http-429",
			Age:     math.Inf(1),
		},
		{Slot: 2, Account: &account.Account{Email: "b@example.com"}, Status: switcher.StatusOK, Usage: good},
	}
	h := 60.0
	head := map[string]*float64{"1": nil, "2": &h}

	e.emitPoll(&snaps[0], snaps, head)
	if _, _, done := e.decideTrigger(nil, false); !done {
		t.Fatal("first unknown tick should not fail over")
	}
	if len(rec.events) != 2 {
		t.Fatalf("want poll + no-switch, got %d events", len(rec.events))
	}

	poll := rec.events[0]
	causes, _ := poll.Fields["usageUnknown"].(map[string]string)
	if causes["1"] != "rate limited (HTTP 429)" || causes["2"] != "" {
		t.Fatalf("usageUnknown = %v", causes)
	}
	if got := poll.Human(); !strings.Contains(got, "usage unknown (rate limited (HTTP 429))") {
		t.Fatalf("poll line lacks the cause: %q", got)
	}

	ns := rec.events[1].Human()
	if want := "no switch: active-usage-unknown (1/3 before failover; rate limited (HTTP 429))"; ns != want {
		t.Fatalf("got %q, want %q", ns, want)
	}
}
