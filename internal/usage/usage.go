// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

// Package usage fetches, normalizes, caches, and schedules polling of
// per-account rate-limit windows from the Claude OAuth usage endpoint.
package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/blairham/go-claude-swap/internal/oauth"
)

const (
	usageURL   = "https://api.anthropic.com/api/oauth/usage"
	profileURL = "https://api.anthropic.com/api/oauth/profile"
	betaHeader = "oauth-2025-04-20"

	fetchTimeout = 5 * time.Second
)

// Window is one normalized rate-limit window.
type Window struct {
	Name     string  `json:"name,omitempty"` // scoped windows only
	Pct      float64 `json:"pct"`
	ResetsAt string  `json:"resets_at,omitempty"` // ISO-8601
}

// Spend is the extra-usage (overage billing) block.
type Spend struct {
	Used     float64 `json:"used"`
	Limit    float64 `json:"limit,omitempty"` // 0 = unlimited
	Pct      float64 `json:"pct"`
	Currency string  `json:"currency"`
	ResetsAt string  `json:"resets_at,omitempty"`
}

// Usage is the normalized per-account usage snapshot that gets cached.
type Usage struct {
	FiveHour *Window  `json:"five_hour,omitempty"`
	SevenDay *Window  `json:"seven_day,omitempty"`
	Spend    *Spend   `json:"spend,omitempty"`
	Scoped   []Window `json:"scoped,omitempty"`
}

// FetchError classifies a failed usage fetch.
type FetchError struct {
	Kind       string // "http-429", "timeout", "network", "bad-response", ...
	RetryAfter float64
	HTTPStatus int
}

func (e *FetchError) Error() string { return "usage fetch failed: " + e.Kind }

// Fetch retrieves and normalizes usage for an access token.
func Fetch(client *http.Client, accessToken string) (*Usage, *FetchError) {
	req, _ := http.NewRequest(http.MethodGet, usageURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", betaHeader)
	req.Header.Set("User-Agent", oauth.UserAgent)

	c := *client
	c.Timeout = fetchTimeout
	resp, err := c.Do(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fe := &FetchError{Kind: "http-" + strconv.Itoa(resp.StatusCode), HTTPStatus: resp.StatusCode}
		// Retry-After is parsed for any HTTP error, seconds-form only.
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, perr := strconv.ParseFloat(ra, 64); perr == nil && secs > 0 {
				fe.RetryAfter = secs
			}
		}
		return nil, fe
	}

	return parseUsage(resp.Body)
}

// rawWindow is a 5h or 7d window as the usage endpoint sends it.
type rawWindow struct {
	ResetsAt    *string `json:"resets_at"`
	Utilization float64 `json:"utilization"`
}

func (w *rawWindow) normalize() *Window {
	if w == nil {
		return nil
	}
	return &Window{Pct: w.Utilization, ResetsAt: strOr(w.ResetsAt)}
}

// rawExtraUsage is the overage-billing block; credits are in cents.
type rawExtraUsage struct {
	UsedCredits  *float64 `json:"used_credits"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	Utilization  *float64 `json:"utilization"`
	ResetsAt     *string  `json:"resets_at"`
	Currency     string   `json:"currency"`
	IsEnabled    bool     `json:"is_enabled"`
}

// normalize returns nil unless the block is enabled and complete.
func (e *rawExtraUsage) normalize() *Spend {
	if e == nil || !e.IsEnabled || e.UsedCredits == nil || e.MonthlyLimit == nil || e.Utilization == nil {
		return nil
	}
	cur := e.Currency
	if cur == "" {
		cur = "USD"
	}
	return &Spend{
		Used: *e.UsedCredits / 100, Limit: *e.MonthlyLimit / 100,
		Pct: *e.Utilization, Currency: cur, ResetsAt: strOr(e.ResetsAt),
	}
}

// rawLimit is one scoped (per-model weekly) limit.
type rawLimit struct {
	Scope *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
	ResetsAt *string `json:"resets_at"`
	Percent  float64 `json:"percent"`
}

// normalize reports false for a limit with no model name to match on.
func (l *rawLimit) normalize() (Window, bool) {
	if l.Scope == nil || l.Scope.Model == nil || l.Scope.Model.DisplayName == "" {
		return Window{}, false
	}
	return Window{Name: l.Scope.Model.DisplayName, Pct: l.Percent, ResetsAt: strOr(l.ResetsAt)}, true
}

// parseUsage normalizes a 200 response body from the usage endpoint. A body
// that does not decode, or that carries no window at all, is a bad response.
func parseUsage(r io.Reader) (*Usage, *FetchError) {
	var raw struct {
		FiveHour   *rawWindow     `json:"five_hour"`
		SevenDay   *rawWindow     `json:"seven_day"`
		ExtraUsage *rawExtraUsage `json:"extra_usage"`
		Limits     []rawLimit     `json:"limits"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, &FetchError{Kind: "bad-response"}
	}

	u := &Usage{
		FiveHour: raw.FiveHour.normalize(),
		SevenDay: raw.SevenDay.normalize(),
		Spend:    raw.ExtraUsage.normalize(),
	}
	for i := range raw.Limits {
		if w, ok := raw.Limits[i].normalize(); ok {
			u.Scoped = append(u.Scoped, w)
		}
	}
	if u.FiveHour == nil && u.SevenDay == nil && u.Spend == nil && len(u.Scoped) == 0 {
		return nil, &FetchError{Kind: "bad-response"}
	}
	return u, nil
}

func classifyTransportError(err error) *FetchError {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &FetchError{Kind: "timeout"}
	}
	return &FetchError{Kind: "network"}
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// FetchProfile resolves an access token's identity; strictly advisory —
// any failure returns nil.
func FetchProfile(client *http.Client, accessToken string) *oauth.Identity {
	req, _ := http.NewRequest(http.MethodGet, profileURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", oauth.UserAgent)

	c := *client
	c.Timeout = fetchTimeout
	resp, err := c.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var payload struct {
		Account *struct {
			UUID  string `json:"uuid"`
			Email string `json:"email"` // note: "email" here, "email_address" on the token endpoint
		} `json:"account"`
		Organization *struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if json.NewDecoder(resp.Body).Decode(&payload) != nil || payload.Account == nil || payload.Account.UUID == "" {
		return nil
	}
	id := &oauth.Identity{UUID: payload.Account.UUID, Email: payload.Account.Email}
	if payload.Organization != nil {
		id.OrganizationUUID = payload.Organization.UUID
	}
	return id
}

// RelevantWindows yields the decision-relevant windows: always 5h and 7d,
// plus scoped weekly windows matching the configured model list. A window
// matches a list entry when the entry equals its display name or contains it
// (case-insensitive) — so a bare name ("Fable"), a full model ID
// ("claude-fable-5"), a suffixed selector ("claude-fable-5[1m]"), and an
// alias like "opusplan" all match, without a per-family mapping table.
// "all" matches every scoped window. Spend is excluded.
func (u *Usage) RelevantWindows(models []string) []Window {
	var out []Window
	if u.FiveHour != nil {
		out = append(out, Window{Name: "5h", Pct: u.FiveHour.Pct, ResetsAt: u.FiveHour.ResetsAt})
	}
	if u.SevenDay != nil {
		out = append(out, Window{Name: "7d", Pct: u.SevenDay.Pct, ResetsAt: u.SevenDay.ResetsAt})
	}
	for _, s := range u.Scoped {
		for _, m := range models {
			if strings.EqualFold(m, "all") || matchesModel(m, s.Name) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// matchesModel reports whether a configured model entry selects the scoped
// window with the given display name.
func matchesModel(entry, windowName string) bool {
	return strings.Contains(strings.ToLower(entry), strings.ToLower(windowName))
}

// Headroom is 100 − max(relevant window pcts); (0, false) when no window
// data exists (unknown, never auto-skipped).
func (u *Usage) Headroom(models []string) (float64, bool) {
	ws := u.RelevantWindows(models)
	if len(ws) == 0 {
		return 0, false
	}
	maxPct := ws[0].Pct
	for _, w := range ws[1:] {
		if w.Pct > maxPct {
			maxPct = w.Pct
		}
	}
	return 100 - maxPct, true
}

// ParseReset parses an ISO-8601 resets_at into epoch seconds; 0 on failure.
func ParseReset(iso string) int64 {
	if iso == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// EarliestFutureReset returns the soonest future reset among relevant
// windows, or 0.
func (u *Usage) EarliestFutureReset(models []string, now time.Time) int64 {
	var best int64
	for _, w := range u.RelevantWindows(models) {
		ts := ParseReset(w.ResetsAt)
		if ts > now.Unix() && (best == 0 || ts < best) {
			best = ts
		}
	}
	return best
}

// LatestExhaustedReset returns the latest reset among windows at ≥100%
// (when an account recovers fully), or 0.
func (u *Usage) LatestExhaustedReset(models []string) int64 {
	var best int64
	for _, w := range u.RelevantWindows(models) {
		if w.Pct < 100 {
			continue
		}
		if ts := ParseReset(w.ResetsAt); ts > best {
			best = ts
		}
	}
	return best
}

// FormatCountdown renders a duration as "3d 4h" / "2h 13m" / "12m" / "45s".
func FormatCountdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		days := int(d.Hours()) / 24
		hours := int(d.Hours()) % 24
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, hours)
	case d >= time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// FormatClock renders a reset instant as local "20:39" (same day) or
// "Aug 14 09:00".
func FormatClock(ts int64, now time.Time) string {
	if ts == 0 {
		return ""
	}
	t := time.Unix(ts, 0).Local()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
