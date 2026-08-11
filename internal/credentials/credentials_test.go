package credentials

import (
	"encoding/json"
	"testing"
)

func TestIsAPIKey(t *testing.T) {
	if !IsAPIKey("sk-ant-api03-xyz") || !IsAPIKey("  sk-ant-api03-xyz\n") {
		t.Error("raw keys should classify as API keys")
	}
	if IsAPIKey(`{"claudeAiOauth":{}}`) || IsAPIKey("sk-ant-oat01-token") {
		t.Error("OAuth blobs and setup tokens are not API keys")
	}
}

func TestMergeSharedSplicesLiveMachineState(t *testing.T) {
	target := `{"claudeAiOauth":{"accessToken":"new"},"mcpOAuth":{"stale":true},"trustedDeviceToken":"slot-tdt"}`
	live := `{"claudeAiOauth":{"accessToken":"old"},"mcpOAuth":{"fresh":true},"pluginSecrets":{"s":1}}`

	var got map[string]any
	if err := json.Unmarshal([]byte(MergeShared(target, live)), &got); err != nil {
		t.Fatal(err)
	}
	mcp := got["mcpOAuth"].(map[string]any)
	if mcp["fresh"] != true {
		t.Error("live mcpOAuth must win")
	}
	if _, ok := got["pluginSecrets"]; !ok {
		t.Error("live-only shared key must be spliced in")
	}
	if got["trustedDeviceToken"] != "slot-tdt" {
		t.Error("account-scoped keys stay with the slot")
	}
	oauth := got["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "new" {
		t.Error("target claudeAiOauth must be preserved")
	}
}

func TestMergeSharedAbsenceIsAuthoritative(t *testing.T) {
	// Live has no mcpOAuth: the target's stale copy must be dropped.
	target := `{"claudeAiOauth":{"accessToken":"a"},"mcpOAuth":{"stale":true}}`
	live := `{"claudeAiOauth":{"accessToken":"b"}}`
	var got map[string]any
	json.Unmarshal([]byte(MergeShared(target, live)), &got)
	if _, ok := got["mcpOAuth"]; ok {
		t.Error("live absence of a shared key must drop the slot's copy")
	}
}

func TestMergeSharedNoOpForNonOAuth(t *testing.T) {
	if got := MergeShared("sk-ant-api03-x", `{"claudeAiOauth":{}}`); got != "sk-ant-api03-x" {
		t.Error("API-key target must pass through untouched")
	}
	target := `{"claudeAiOauth":{"a":1}}`
	if got := MergeShared(target, "sk-ant-api03-x"); got != target {
		t.Error("non-JSON live credential → no-op")
	}
}
