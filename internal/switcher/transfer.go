package switcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/claudecfg"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/oauth"
	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// TransferVersion is the export envelope format version.
const TransferVersion = 1

// Envelope is the export file format. Never encrypted by cswap itself —
// compose gpg externally.
type Envelope struct {
	Version             int               `json:"version"`
	ExportedAt          string            `json:"exportedAt"`
	ExportedFrom        string            `json:"exportedFrom"`
	SwapVersion         string            `json:"swapVersion"`
	Encrypted           bool              `json:"encrypted"`
	ActiveAccountNumber *int              `json:"activeAccountNumber,omitempty"`
	Accounts            []ExportedAccount `json:"accounts"`
}

// ExportedAccount is one account in an export envelope.
type ExportedAccount struct {
	Number           int             `json:"number"`
	Email            string          `json:"email"`
	UUID             string          `json:"uuid"`
	OrganizationUUID string          `json:"organizationUuid"`
	OrganizationName string          `json:"organizationName"`
	Added            string          `json:"added"`
	Credentials      json.RawMessage `json:"credentials"`
	Config           json.RawMessage `json:"config"`
	Kind             string          `json:"kind,omitempty"`
	Alias            string          `json:"alias,omitempty"`
}

var emailRe = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)

func platformName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "linux":
		if os.Getenv("WSL_DISTRO_NAME") != "" {
			return "wsl"
		}
		return "linux"
	case "windows":
		return "windows"
	default:
		return "unknown"
	}
}

// Export writes accounts to path ("-" = stdout). full keeps whole credential
// and config blobs; the default slims them to the account-specific parts
// (drops machine-shared MCP state and device-bound tokens).
func Export(path, version string, full bool, only string) (int, error) {
	seq, err := account.Load()
	if err != nil {
		return 0, err
	}

	var slots []int
	if only != "" {
		slot, rerr := seq.Resolve(only)
		if rerr != nil {
			return 0, rerr
		}
		slots = []int{slot}
	} else {
		slots = seq.Order
	}

	activeSlot := 0
	if id, ierr := claudecfg.ReadIdentity(); ierr == nil && id != nil {
		activeSlot = seq.FindByIdentity(id.Email, id.OrganizationUUID)
	}

	env := Envelope{
		Version:      TransferVersion,
		ExportedAt:   time.Now().UTC().Format(account.TimeFormat),
		ExportedFrom: platformName(),
		SwapVersion:  version,
	}

	for _, slot := range slots {
		a := seq.Get(slot)
		if a == nil {
			continue
		}
		var cred string
		var unreadable bool
		if slot == activeSlot {
			// The live credential is fresher than the backup.
			act := credentials.ReadActive()
			cred, unreadable = act.Value, act.Unreadable
		} else {
			cred, unreadable = credentials.ReadBackup(slot, a.Email)
		}
		if unreadable || cred == "" {
			if only != "" {
				return 0, fmt.Errorf("no readable stored credentials for slot %d", slot)
			}
			fmt.Fprintf(os.Stderr, "skipping Account-%d (%s): no readable credentials\n", slot, a.Email)
			continue
		}

		var cfgRaw []byte
		if slot == activeSlot {
			cfgRaw, err = claudecfg.ReadRaw()
		} else {
			cfgRaw, err = os.ReadFile(paths.AccountConfigBackup(slot, a.Email))
		}
		if err != nil {
			if only != "" {
				return 0, fmt.Errorf("no config backup for slot %d: %w", slot, err)
			}
			fmt.Fprintf(os.Stderr, "skipping Account-%d (%s): no config backup\n", slot, a.Email)
			continue
		}

		ea := ExportedAccount{
			Number: slot, Email: a.Email, UUID: a.UUID,
			OrganizationUUID: a.OrganizationUUID, OrganizationName: a.OrganizationName,
			Added: a.Added, Alias: a.Alias,
		}
		if credentials.IsAPIKey(cred) {
			ea.Kind = "api_key"
			ea.Credentials, _ = json.Marshal(cred)
		} else if full {
			ea.Credentials = json.RawMessage(cred)
		} else {
			ea.Credentials = slimJSON(cred, "claudeAiOauth")
		}
		if full {
			ea.Config = json.RawMessage(cfgRaw)
		} else {
			ea.Config = slimJSON(string(cfgRaw), "oauthAccount")
		}
		env.Accounts = append(env.Accounts, ea)

		if slot == activeSlot {
			env.ActiveAccountNumber = &slot
		}
	}

	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return 0, err
	}
	if path == "-" {
		fmt.Println(string(data))
	} else {
		if err := account.WriteFileAtomic(path, data, 0o600); err != nil {
			return 0, err
		}
	}
	return len(env.Accounts), nil
}

// slimJSON keeps only one top-level key of a JSON object.
func slimJSON(raw, keep string) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return json.RawMessage(raw)
	}
	out := map[string]json.RawMessage{}
	if v, ok := obj[keep]; ok {
		out[keep] = v
	}
	data, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(raw)
	}
	return data
}

// ImportResult summarizes an import.
type ImportResult struct {
	Imported, Overwritten, Skipped int
	Warnings                       []string
}

// Import reads an envelope, validating every account before any write.
// Existing (email, org) slots are skipped unless force or their stored token
// is dead (auto-heal).
func Import(path string, force bool) (*ImportResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("not a cswap export file: %w", err)
	}
	if env.Version != TransferVersion {
		return nil, fmt.Errorf("unsupported export version %d (expected %d)", env.Version, TransferVersion)
	}
	if env.Encrypted {
		return nil, errors.New("encrypted envelopes are not supported; decrypt externally first")
	}

	// Pass 1: validate everything.
	seenIdentity := map[string]bool{}
	seenAlias := map[string]bool{}
	for i := range env.Accounts {
		ea := &env.Accounts[i]
		if !emailRe.MatchString(ea.Email) {
			return nil, fmt.Errorf("account %d: invalid email %q", ea.Number, ea.Email)
		}
		if ea.Number < 1 {
			return nil, fmt.Errorf("account %q: invalid slot number %d", ea.Email, ea.Number)
		}
		idKey := ea.Email + "\x00" + ea.OrganizationUUID
		if seenIdentity[idKey] {
			return nil, fmt.Errorf("duplicate account %s in export", ea.Email)
		}
		seenIdentity[idKey] = true
		if ea.Alias != "" {
			if aerr := account.ValidateAlias(ea.Alias); aerr != nil {
				return nil, fmt.Errorf("account %s: %w", ea.Email, aerr)
			}
			if seenAlias[ea.Alias] {
				return nil, fmt.Errorf("duplicate alias %q in export", ea.Alias)
			}
			seenAlias[ea.Alias] = true
		}
		if ea.Kind == "api_key" {
			var s string
			if json.Unmarshal(ea.Credentials, &s) != nil || !credentials.IsAPIKey(s) {
				return nil, fmt.Errorf("account %s: api_key credentials must be a raw key string", ea.Email)
			}
		} else {
			var obj map[string]json.RawMessage
			if json.Unmarshal(ea.Credentials, &obj) != nil || obj["claudeAiOauth"] == nil {
				return nil, fmt.Errorf("account %s: credentials must be an OAuth JSON object", ea.Email)
			}
		}
	}

	if err := paths.EnsureDirs(); err != nil {
		return nil, err
	}

	// Pass 2: write.
	res := &ImportResult{}
	for _, ea := range env.Accounts {
		seq, lerr := account.Load()
		if lerr != nil {
			return nil, lerr
		}
		existing := seq.FindByIdentity(ea.Email, ea.OrganizationUUID)
		slot := existing
		overwrite := false
		if existing != 0 {
			if !force && !slotTokenDead(existing, seq.Get(existing)) {
				res.Skipped++
				continue
			}
			overwrite = true
		} else {
			if seq.Get(ea.Number) == nil {
				slot = ea.Number
			} else {
				slot = seq.NextSlot()
			}
		}

		alias := ea.Alias
		if alias != "" {
			if conflict := aliasConflict(seq, alias, ea.Email, ea.OrganizationUUID); conflict != "" {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("dropped alias %q for %s (already used by %s)", alias, ea.Email, conflict))
				alias = ""
			}
		}

		var cred string
		if ea.Kind == "api_key" {
			// Shape validated in pass 1.
			_ = json.Unmarshal(ea.Credentials, &cred)
		} else {
			cred = string(ea.Credentials)
		}
		if err := credentials.WriteBackup(slot, ea.Email, cred); err != nil {
			return nil, fmt.Errorf("account %s: %w", ea.Email, err)
		}
		if err := account.WriteFileAtomic(paths.AccountConfigBackup(slot, ea.Email), ea.Config, 0o600); err != nil {
			return nil, fmt.Errorf("account %s: %w", ea.Email, err)
		}
		_ = usage.Update(func(s *usage.Store) {
			if e := s.Get(slot, ea.Email, ea.OrganizationUUID); e != nil {
				e.ClearDeadToken()
			}
		})

		added := ea.Added
		if added == "" {
			added = time.Now().UTC().Format(account.TimeFormat)
		}
		seq.Upsert(slot, &account.Account{
			Email: ea.Email, UUID: ea.UUID,
			OrganizationUUID: ea.OrganizationUUID, OrganizationName: ea.OrganizationName,
			Added: added, Alias: alias,
		})
		if err := seq.Save(); err != nil {
			return nil, err
		}
		if overwrite {
			res.Overwritten++
		} else {
			res.Imported++
		}
	}

	// Seed the active pointer from the envelope only when we have none.
	if env.ActiveAccountNumber != nil {
		seq, lerr := account.Load()
		if lerr == nil && seq.ActiveAccountNumber == nil {
			for _, ea := range env.Accounts {
				if ea.Number == *env.ActiveAccountNumber {
					if slot := seq.FindByIdentity(ea.Email, ea.OrganizationUUID); slot != 0 {
						seq.ActiveAccountNumber = &slot
						_ = seq.Save()
					}
					break
				}
			}
		}
	}
	return res, nil
}

// slotTokenDead reports whether a slot's stored token is quarantined
// (import auto-heal). Unreadable stores never condemn.
func slotTokenDead(slot int, a *account.Account) bool {
	if a == nil {
		return false
	}
	cred, unreadable := credentials.ReadBackup(slot, a.Email)
	if unreadable || cred == "" {
		return false
	}
	store := usage.LoadStore()
	e := store.Get(slot, a.Email, a.OrganizationUUID)
	if e == nil {
		return false
	}
	return e.TokenDead(oauth.Fingerprint([]byte(cred)))
}
