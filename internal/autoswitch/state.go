package autoswitch

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// stateSchemaVersion of autoswitch_state.json.
const stateSchemaVersion = 1

// quarantineRecord marks a slot whose refresh lineage died. It binds to the
// credential generation via the fingerprint, so replacing the credential
// (or the account) releases the quarantine.
type quarantineRecord struct {
	Email                   string `json:"email"`
	Reason                  string `json:"reason"`
	At                      string `json:"at"`
	RefreshTokenFingerprint string `json:"refreshTokenFingerprint"`
}

// state is the persisted auto-switch memory: cooldown bookkeeping, what was
// left behind on the last switch, and quarantined slots.
type state struct {
	SchemaVersion  int                          `json:"schemaVersion"`
	LastSwitchAt   float64                      `json:"lastSwitchAt"`
	LastSwitchTo   string                       `json:"lastSwitchTo"`
	LastSwitchFrom int                          `json:"lastSwitchFrom"`
	LeftHeadroom   *float64                     `json:"leftHeadroom"`
	LeftRecoveryAt *float64                     `json:"leftRecoveryAt"`
	LeftTrigger    string                       `json:"leftTrigger"`
	Quarantine     map[string]*quarantineRecord `json:"quarantine"`
}

func statePath() string { return filepath.Join(paths.BackupRoot(), "autoswitch_state.json") }

func stateLockPath() string { return filepath.Join(paths.BackupRoot(), ".autoswitch_state.lock") }

// loadState reads autoswitch_state.json. Missing or corrupt files yield a
// fresh state: the loop must keep running over a torn file, and the state is
// advisory (worst case one early switch after losing cooldown history).
func loadState() *state {
	fresh := &state{SchemaVersion: stateSchemaVersion, Quarantine: map[string]*quarantineRecord{}}
	raw, err := os.ReadFile(statePath())
	if err != nil {
		return fresh
	}
	var parsed state
	if json.Unmarshal(raw, &parsed) != nil {
		return fresh
	}
	if parsed.Quarantine == nil {
		parsed.Quarantine = map[string]*quarantineRecord{}
	}
	parsed.SchemaVersion = stateSchemaVersion
	return &parsed
}

// saveState writes the state atomically with 2-space indentation.
func saveState(st *state) error {
	st.SchemaVersion = stateSchemaVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return account.WriteFileAtomic(statePath(), data, 0o600)
}

// mutateState applies fn to the current state under the state lock and
// persists the result when fn reports a change.
func mutateState(fn func(*state) bool) error {
	lock := locks.NewFileLock(stateLockPath())
	if err := lock.Acquire(); err != nil {
		return err
	}
	defer lock.Release()
	st := loadState()
	if !fn(st) {
		return nil
	}
	return saveState(st)
}
