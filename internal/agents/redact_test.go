package agents

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApprovalArgumentsAreRedactedAndBounded(t *testing.T) {
	for _, k := range []string{"Authorization", "api_key", "apiKey", "clientSecret", "refresh-token", "headers", "Cookie", "password", "x-api-key"} {
		if !isSecretKey(k) {
			t.Errorf("%q is not treated as a secret", k)
		}
	}
	for _, k := range []string{"max_tokens", "url", "title", "keyword"} {
		if isSecretKey(k) {
			t.Errorf("%q is treated as a secret", k)
		}
	}
	raw := json.RawMessage(`{"url":"https://example.com","headers":{"Authorization":"Bearer s3cret"},"nested":{"token":"t","list":[1,2]},"long":"` +
		strings.Repeat("x", 5000) + `"}`)
	out := string(redactArguments(raw))
	if strings.Contains(out, "s3cret") || !strings.Contains(out, `"headers":"[redacted]"`) || !strings.Contains(out, `"token":"[redacted]"`) {
		t.Fatalf("redacted = %s", out)
	}
	if strings.Count(out, "x") > 2001+20 {
		t.Fatal("a long string was not cut")
	}
	huge := map[string]string{}
	for i := 0; i < 90; i++ {
		huge[strings.Repeat("k", i+1)] = strings.Repeat("y", 1000)
	}
	b, _ := json.Marshal(huge)
	if got := string(redactArguments(b)); !strings.HasPrefix(got, `{"[truncated]":`) || len(got) > 2200 {
		t.Fatalf("an oversized view = %d bytes: %.60s", len(got), got)
	}
	for _, bad := range []string{``, `not json`, `"a string"`} {
		var v map[string]any
		if err := json.Unmarshal(redactArguments(json.RawMessage(bad)), &v); err != nil {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}
