package switcher

import (
	"fmt"
	"time"

	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/oauth"
	"github.com/blairham/go-claude-swap/internal/session"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// TokenStatusLines are the source-labeled OAuth token diagnostics `cswap
// list --token-status` shows under an account, as claude-swap does. The
// active account's token is the live login's ("active profile"). Any other
// account shows its `cswap run` profile's token first when one is readable
// ("session profile" — after a session ran, that is the newest generation
// of the account's token family; "ignored" when an in-session /login
// re-pointed the profile at another account), then the "stored backup".
// API-key accounts have no OAuth token and show nothing.
func TokenStatusLines(s Snapshot, now time.Time) []string {
	a := s.Account
	if s.Active {
		cred := credentials.ReadActive().Value
		if credentials.IsAPIKey(cred) {
			return nil
		}
		if line, ok := labelTokenStatus("active profile", cred, now); ok {
			return []string{line}
		}
		return nil
	}
	backup, _ := credentials.ReadBackup(s.Slot, a.Email)
	if credentials.IsAPIKey(backup) {
		return nil
	}
	var lines []string
	dir := session.Dir(s.Slot, a.Email)
	if cred := session.ReadCredentials(dir); cred != "" {
		if session.Drifted(dir, a.Email, a.OrganizationUUID) {
			lines = append(lines, "session profile: ignored (different account)")
		} else if line, ok := labelTokenStatus("session profile", cred, now); ok {
			lines = append(lines, line)
		}
	}
	if line, ok := labelTokenStatus("stored backup", backup, now); ok {
		lines = append(lines, line)
	}
	return lines
}

// labelTokenStatus summarizes a credential's OAuth token: freshness, whether
// it can be refreshed, and when it expires. ok is false when the credential
// carries no claudeAiOauth object.
func labelTokenStatus(source, cred string, now time.Time) (string, bool) {
	if cred == "" {
		return "", false
	}
	b, err := oauth.ParseBlob([]byte(cred))
	if err != nil || b.OAuth == nil {
		return "", false
	}
	refresh := "no"
	if b.OAuth.RefreshToken != "" {
		refresh = "yes"
	}
	if b.OAuth.ExpiresAt == 0 {
		return fmt.Sprintf("%s: unknown expiry, refresh token %s", source, refresh), true
	}
	state := "fresh"
	if oauth.Expired(b.OAuth.ExpiresAt, now) {
		state = "expired"
	}
	exp := time.UnixMilli(b.OAuth.ExpiresAt)
	return fmt.Sprintf("%s: %s, refresh token %s, expires %s in %s",
		source, state, refresh, usage.FormatClock(exp.Unix(), now), usage.FormatCountdown(exp.Sub(now))), true
}
