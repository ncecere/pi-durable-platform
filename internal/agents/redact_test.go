package agents

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApprovalArgumentsAreRedactedAndBounded(t *testing.T) {
	for _, k := range []string{"Authorization", "api_key", "apiKey", "clientSecret", "client_secret", "refresh-token", "access_token",
		"Cookie", "password", "DB_PASSWORD", "x-api-key", "private_key", "aws_secret_access_key", "github_token", "secret", "token"} {
		if !isSecretKey(k) {
			t.Errorf("%q is not treated as a secret", k)
		}
	}
	// Names that only might be: an S3 object's key, an auth method, headers (their own keys are checked), pagination.
	for _, k := range []string{"key", "Key", "object_key", "auth", "auth_method", "headers", "header", "signature", "max_tokens",
		"pageToken", "next_page_token", "url", "title", "keyword", "keys", "credential_type"} {
		if isSecretKey(k) {
			t.Errorf("%q is treated as a secret", k)
		}
	}
	raw := json.RawMessage(`{"url":"https://example.com","headers":{"Authorization":"Bearer s3cret","Accept":"application/json"},` +
		`"nested":{"token":"t","list":[1,2]},"key":"reports/2026/10/summary.csv","long":"` + strings.Repeat("x", 5000) + `"}`)
	out := string(redactArguments(raw))
	for _, want := range []string{`"Authorization":"[redacted, 13 characters]"`, `"Accept":"application/json"`, `"token":"[redacted, 1 character]"`,
		`"key":"reports/2026/10/summary.csv"`} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in %s", want, out)
		}
	}
	if strings.Contains(out, "s3cret") {
		t.Fatalf("redacted = %s", out)
	}
	// A credential's value under any name; a non-string under a secret's name; one the runtime already redacted.
	for _, v := range []string{"pdp_" + strings.Repeat("A1b2", 11), "pdp_sa_" + strings.Repeat("x9Y", 12), "pdpat_" + strings.Repeat("Zz0", 15),
		"pdpk_" + strings.Repeat("q", 20), "sk-proj-" + strings.Repeat("aB3", 16), "sk_live_" + strings.Repeat("4eC3", 6), "ghp_" + strings.Repeat("a1B2", 9),
		"xoxb-123456789012-abcdefghijkl", "AKIAIOSFODNN7EXAMPLE", "AIzaSyD-" + strings.Repeat("x7", 16), "Bearer abcdefgh12345678",
		"basic dXNlcjpwYXNzd29yZA==", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.c2lnbmF0dXJl",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk=\n-----END OPENSSH PRIVATE KEY-----", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"} {
		if !isSecretValue(v) {
			t.Errorf("%q is not treated as a credential", v)
		}
	}
	// Shown: identifiers, paths, one-case hashes, UUIDs, prose, short words.
	for _, v := range []string{"reports/2026/10/summary.csv", "550e8400-e29b-41d4-a716-446655440000", "da39a3ee5e6b4b0d3255bfef95601890afd80709",
		"Fix the login page", "https://example.com/a/b?c=d", "main", "s3://bucket/path/to/object.json", "token limit exceeded",
		"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", "issue-1234-fix-the-thing-in-the-parser"} {
		if isSecretValue(v) {
			t.Errorf("%q is treated as a credential", v)
		}
	}
	out = string(redactArguments(json.RawMessage(`{"query":"AKIAIOSFODNN7EXAMPLE","password":{"a":1},"apiKey":"[redacted, 42 characters]","list":["ghp_` +
		strings.Repeat("a1B2", 9) + `"]}`)))
	for _, want := range []string{`"query":"[redacted, 20 characters]"`, `"password":"[redacted, 7 characters]"`, `"apiKey":"[redacted, 42 characters]"`,
		`"list":["[redacted, 40 characters]"]`} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in %s", want, out)
		}
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
