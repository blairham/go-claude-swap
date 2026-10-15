// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package switcher

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/oauth"
	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/session"
)

// gateEndpoint is a concurrency-safe token endpoint stub. during runs while
// the POST is in flight, to model a racing writer.
type gateEndpoint struct {
	responses map[string]tokenResp
	during    func()
	calls     []string
	delay     time.Duration
	mu        sync.Mutex
}

func (g *gateEndpoint) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	var payload map[string]string
	json.Unmarshal(raw, &payload)
	rt := payload["refresh_token"]
	g.mu.Lock()
	g.calls = append(g.calls, rt)
	resp, ok := g.responses[rt]
	during := g.during
	g.mu.Unlock()
	time.Sleep(g.delay)
	if during != nil {
		during()
	}
	if !ok {
		resp = tokenResp{status: 400, body: `{"error":"invalid_grant"}`}
	}
	return &http.Response{
		StatusCode: resp.status,
		Body:       io.NopCloser(strings.NewReader(resp.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func (g *gateEndpoint) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *gateEndpoint) client() *http.Client { return &http.Client{Transport: g} }

const rotatedB = `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`

// gateSlot leaves a@b.co live and b@b.co in slot 1 with an expired backup.
func gateSlot(t *testing.T) string {
	t.Helper()
	home := env(t)
	login(t, home, "b@b.co", "", credJSON("at-b", "rt-b"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	expired := expiredCredJSON("at-old", "rt-b")
	if err := credentials.WriteBackup(1, "b@b.co", expired); err != nil {
		t.Fatal(err)
	}
	return expired
}

func backupOf(t *testing.T) string {
	t.Helper()
	b, unreadable := credentials.ReadBackup(1, "b@b.co")
	if unreadable {
		t.Fatal("backup unreadable")
	}
	return b
}

func stashRows(t *testing.T) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(stashManifestPath())
	if err != nil {
		return nil
	}
	var m struct {
		Entries map[string]map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m.Entries
}

func TestConsumeGrantPersistsSuccessor(t *testing.T) {
	snap := gateSlot(t)
	ep := &gateEndpoint{responses: map[string]tokenResp{"rt-b": {200, rotatedB}}}

	o := ConsumeGrant(ep.client(), 1, "b@b.co", snap)
	if o.Err != oauth.ErrNone || !strings.Contains(o.Cred, "at-new") {
		t.Fatalf("outcome = %+v", o)
	}
	if o.ConsumedFP != oauth.Fingerprint([]byte(snap)) {
		t.Errorf("ConsumedFP = %q, want the POSTed generation's", o.ConsumedFP)
	}
	if b := backupOf(t); b != o.Cred {
		t.Errorf("backup = %q, want the successor", b)
	}
	if len(stashRows(t)) != 0 {
		t.Errorf("a clean persist stashed: %v", stashRows(t))
	}
}

func TestConcurrentGatesConsumeOneGrant(t *testing.T) {
	snap := gateSlot(t)
	ep := &gateEndpoint{responses: map[string]tokenResp{"rt-b": {200, rotatedB}}, delay: 100 * time.Millisecond}

	var wg sync.WaitGroup
	outs := make([]GrantOutcome, 2)
	for i := range outs {
		wg.Go(func() { outs[i] = ConsumeGrant(ep.client(), 1, "b@b.co", snap) })
	}
	wg.Wait()

	if n := ep.callCount(); n != 1 {
		t.Fatalf("token endpoint POSTs = %d, want 1 (a refresh token is one-time-use)", n)
	}
	for i, o := range outs {
		if o.Err != oauth.ErrNone || !strings.Contains(o.Cred, "at-new") {
			t.Errorf("gate %d outcome = %+v, want the one successor", i, o)
		}
	}
}

func TestConsumeGrantCASConflictAdoptsStore(t *testing.T) {
	snap := gateSlot(t)
	racer := credJSON("at-racer", "rt-racer")
	ep := &gateEndpoint{responses: map[string]tokenResp{"rt-b": {200, rotatedB}}}
	ep.during = func() {
		if err := credentials.WriteBackup(1, "b@b.co", racer); err != nil {
			t.Error(err)
		}
	}

	o := ConsumeGrant(ep.client(), 1, "b@b.co", snap)
	if o.Err != oauth.ErrNone || o.Cred != racer {
		t.Fatalf("outcome = %+v, want the racing writer's lineage, not demoted", o)
	}
	if b := backupOf(t); b != racer {
		t.Errorf("backup = %q, the racing writer must not be clobbered", b)
	}
	rows := stashRows(t)
	if len(rows) != 1 {
		t.Fatalf("stash rows = %v, want the successor parked", rows)
	}
	for _, r := range rows {
		if r["reason"] != stashCASConflict || r["consumedFp"] != oauth.Fingerprint([]byte(snap)) {
			t.Errorf("row = %v", r)
		}
	}

	// The conflict row can never match a store that moved forward: the next
	// gate pass retires it.
	ep.during = nil
	ConsumeGrant(ep.client(), 1, "b@b.co", racer)
	if rows := stashRows(t); len(rows) != 0 {
		t.Errorf("CAS-conflict row not retired: %v", rows)
	}
}

func TestConsumeGrantSlotRemovedMidPOST(t *testing.T) {
	snap := gateSlot(t)
	ep := &gateEndpoint{responses: map[string]tokenResp{"rt-b": {200, rotatedB}}}
	ep.during = func() { credentials.DeleteBackup(1, "b@b.co") }

	o := ConsumeGrant(ep.client(), 1, "b@b.co", snap)
	if o.Err != oauth.ErrNone {
		t.Fatalf("a removed slot is a completed user action, not an error: %+v", o)
	}
	if b, _ := credentials.ReadBackup(1, "b@b.co"); b != "" {
		t.Errorf("removed slot resurrected: %q", b)
	}
	if len(stashRows(t)) != 1 {
		t.Error("the spent grant's successor must be stashed, not discarded")
	}
}

func TestConsumeGrantPersistLockFailureDemotesThenAdopts(t *testing.T) {
	snap := gateSlot(t)
	prev := gateLockTimeout
	gateLockTimeout = 200 * time.Millisecond
	t.Cleanup(func() { gateLockTimeout = prev })

	held := locks.NewFileLock(paths.LockPath())
	ep := &gateEndpoint{responses: map[string]tokenResp{"rt-b": {200, rotatedB}}}
	ep.during = func() {
		if err := held.Acquire(); err != nil {
			t.Error(err)
		}
	}
	o := ConsumeGrant(ep.client(), 1, "b@b.co", snap)
	held.Release()

	if o.Err != oauth.ErrTransient || !o.Stashed || !strings.Contains(o.Cred, "at-new") {
		t.Fatalf("outcome = %+v, want demoted to transient with the successor stashed", o)
	}
	if b := backupOf(t); b != snap {
		t.Fatalf("backup = %q, want the spent generation still in place", b)
	}

	// The next pass adopts the stashed successor instead of POSTing the
	// spent generation.
	ep.during = nil
	o = ConsumeGrant(ep.client(), 1, "b@b.co", snap)
	if o.Err != oauth.ErrNone || !strings.Contains(o.Cred, "at-new") {
		t.Fatalf("adopting pass = %+v", o)
	}
	if n := ep.callCount(); n != 1 {
		t.Errorf("POSTs = %d, want 1: the adoption must not spend another grant", n)
	}
	if b := backupOf(t); !strings.Contains(b, "at-new") {
		t.Errorf("backup = %q, want the adopted successor", b)
	}
	if rows := stashRows(t); len(rows) != 0 {
		t.Errorf("adopted row not retired: %v", rows)
	}
}

func TestConsumeGrantUnreadableStashDefers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	snap := gateSlot(t)
	if err := stashCredential(credJSON("at-s", "rt-s"), stashPersistFailed, 1,
		map[string]any{"consumedFp": oauth.Fingerprint([]byte(snap))}); err != nil {
		t.Fatal(err)
	}
	for id := range stashRows(t) {
		if err := os.Chmod(stashEntryPath(id), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(stashEntryPath(id), 0o600) })
	}
	ep := &gateEndpoint{responses: map[string]tokenResp{"rt-b": {200, rotatedB}}}

	o := ConsumeGrant(ep.client(), 1, "b@b.co", snap)
	if o.Err != ErrStashUnreadable {
		t.Fatalf("outcome = %+v, want stash-unreadable", o)
	}
	if n := ep.callCount(); n != 0 {
		t.Errorf("POSTs = %d: the stored generation is spent, never POST it", n)
	}
}

func TestConsumeGrantPermanentCarriesConsumedFP(t *testing.T) {
	snap := gateSlot(t)
	ep := &gateEndpoint{} // everything answers invalid_grant

	o := ConsumeGrant(ep.client(), 1, "b@b.co", snap)
	if o.Err != oauth.ErrInvalidGrant || o.ConsumedFP != oauth.Fingerprint([]byte(snap)) {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestBootstrapRefusesOnSpentGrant(t *testing.T) {
	sessionAccounts(t)
	probeAs(t, "a@b.co")
	prev := gateLockTimeout
	gateLockTimeout = 200 * time.Millisecond
	t.Cleanup(func() { gateLockTimeout = prev })

	held := locks.NewFileLock(paths.LockPath())
	stub := &gateEndpoint{responses: map[string]tokenResp{
		"rt-a": {200, `{"access_token":"at-a2","refresh_token":"rt-a2","expires_in":3600}`},
	}}
	stub.during = func() { held.Acquire() }
	prevClient := RefreshClient
	RefreshClient = stub.client()
	t.Cleanup(func() { RefreshClient = prevClient })

	_, err := PrepareSession("a@b.co", SessionOptions{})
	held.Release()
	if err == nil || !strings.Contains(err.Error(), "stashed") {
		t.Fatalf("err = %v, want a refusal that says the successor is stashed", err)
	}

	// Retrying adopts the stash and bootstraps on the new generation without
	// a second POST.
	stub.during = nil
	plan, err := PrepareSession("a@b.co", SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stub.callCount() != 1 {
		t.Errorf("POSTs = %d, want 1", stub.callCount())
	}
	if got := session.ReadCredentials(plan.Dir); !strings.Contains(got, "at-a2") {
		t.Errorf("profile seeded with %q, want the adopted successor", got)
	}
}

func TestBootstrapSpendsOneRefresh(t *testing.T) {
	sessionAccounts(t)
	probeAs(t, "a@b.co")
	stub := &gateEndpoint{responses: map[string]tokenResp{
		"rt-a": {200, `{"access_token":"at-a2","refresh_token":"rt-a2","expires_in":3600}`},
	}}
	prevClient := RefreshClient
	RefreshClient = stub.client()
	t.Cleanup(func() { RefreshClient = prevClient })

	plan, err := PrepareSession("a@b.co", SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stub.callCount() != 1 {
		t.Fatalf("POSTs = %d, want one refresh before the seed", stub.callCount())
	}
	if got := session.ReadCredentials(plan.Dir); !strings.Contains(got, "at-a2") {
		t.Errorf("profile seeded with %q, want the fresh generation", got)
	}
}
