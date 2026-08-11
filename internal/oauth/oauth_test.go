package oauth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseBlobRoundTripPreservesUnknownKeys(t *testing.T) {
	raw := `{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt","expiresAt":123,"scopes":["a"],"subscriptionType":"max"},"mcpOAuth":{"x":1},"trustedDeviceToken":"tdt"}`
	blob, err := ParseBlob([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if blob.OAuth.AccessToken != "at" || blob.OAuth.RefreshToken != "rt" || blob.OAuth.ExpiresAt != 123 {
		t.Fatalf("bad parse: %+v", blob.OAuth)
	}
	out, err := MarshalBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(out, &got)
	if got["mcpOAuth"] == nil || got["trustedDeviceToken"] == nil {
		t.Fatalf("unknown top-level keys lost: %s", out)
	}
	inner := got["claudeAiOauth"].(map[string]any)
	if inner["subscriptionType"] != "max" {
		t.Fatalf("unknown inner key lost: %s", out)
	}
}

func TestExpired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cases := []struct {
		expiresAt int64
		want      bool
	}{
		{0, false}, // absent → not expired
		{now.UnixMilli() + 10*60*1000, false},
		{now.UnixMilli() + 4*60*1000, true}, // inside the 5-min buffer
		{now.UnixMilli() - 1000, true},
	}
	for _, c := range cases {
		if got := Expired(c.expiresAt, now); got != c.want {
			t.Errorf("Expired(%d) = %v, want %v", c.expiresAt, got, c.want)
		}
	}
}

func TestFingerprint(t *testing.T) {
	withRT := `{"claudeAiOauth":{"accessToken":"a1","refreshToken":"rt"}}`
	rotated := `{"claudeAiOauth":{"accessToken":"a2","refreshToken":"rt"}}`
	if Fingerprint([]byte(withRT)) != Fingerprint([]byte(rotated)) {
		t.Error("fingerprint must survive access-token rotation")
	}
	if !strings.HasPrefix(Fingerprint([]byte(withRT)), "sha256:") {
		t.Error("refresh-token fingerprint prefix")
	}
	if !strings.HasPrefix(Fingerprint([]byte(`sk-ant-api-something`)), "sha256-full:") {
		t.Error("full-content fingerprint prefix")
	}
	if Fingerprint(nil) != "" {
		t.Error("empty input → empty fingerprint")
	}
}
