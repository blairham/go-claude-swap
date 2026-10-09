package autoswitch

import (
	"fmt"
	"math"
	"strings"

	"github.com/blairham/go-claude-swap/internal/switcher"
)

// unknownCause explains why a snapshot has no decision-grade headroom, in a
// form short enough to sit inside a log line ("rate limited (HTTP 429)",
// "network error; last good data 47m old"). It returns "" when the
// snapshot's headroom is in fact known.
//
// Without this the loop could only say "usage unknown", and a failover a few
// ticks later was unexplained: an expired token, a 429, a dead network and a
// cache that aged out all read the same.
func unknownCause(s *switcher.Snapshot, models []string) string {
	if s == nil {
		return ""
	}
	switch s.Status {
	case switcher.StatusKeychainUnavailable:
		return "credential store unreadable (Keychain locked or unavailable)"
	case switcher.StatusNoCredentials:
		return "no stored credential"
	case switcher.StatusAPIKey:
		return "API-key account (no usage windows)"
	case switcher.StatusReloginRequired:
		return "token rejected; re-login required"
	case switcher.StatusTokenExpired:
		return "access token expired or rejected (HTTP 401); waiting for Claude Code to refresh it"
	case switcher.StatusOK:
		if s.Usage == nil {
			return "no usage data"
		}
		if _, ok := s.Usage.Headroom(models); ok {
			return ""
		}
		if len(models) > 0 {
			return "no usage window reported for " + strings.Join(models, ", ")
		}
		return "no usage window reported"
	}

	// StatusUnavailable: either this tick's fetch failed, or the fetch was
	// skipped (backoff, or not due yet) and the cached reading is too old
	// to decide on.
	var parts []string
	if s.LastErr != "" {
		parts = append(parts, describeFetchError(s.LastErr))
	}
	switch {
	case s.LastGood != nil && !math.IsInf(s.Age, 1):
		parts = append(parts, "last good data "+formatAge(s.Age)+" old")
	case len(parts) == 0:
		parts = append(parts, "no usage data yet")
	}
	if s.LastErr == "" && s.LastGood != nil {
		parts[0] = "cached usage too stale to decide on (" + parts[0] + ")"
	}
	return strings.Join(parts, "; ")
}

// describeFetchError turns a usage.FetchError kind into words.
func describeFetchError(kind string) string {
	switch kind {
	case "http-429":
		return "rate limited (HTTP 429)"
	case "http-401":
		return "token rejected (HTTP 401)"
	case "timeout":
		return "usage request timed out"
	case "network":
		return "network error"
	case "bad-response":
		return "unexpected response from the usage endpoint"
	}
	if code, ok := strings.CutPrefix(kind, "http-"); ok {
		return "usage endpoint returned HTTP " + code
	}
	return kind
}

// formatAge renders seconds as a coarse duration: 45s, 12m, 3h, 2d.
func formatAge(sec float64) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", int(sec))
	case sec < 3600:
		return fmt.Sprintf("%dm", int(sec/60))
	case sec < 86400:
		return fmt.Sprintf("%dh", int(sec/3600))
	}
	return fmt.Sprintf("%dd", int(sec/86400))
}
