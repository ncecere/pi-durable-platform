package llm

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func scan(t *testing.T, sse bool, body string, chunk int) Usage {
	t.Helper()
	s := newUsageScanner(sse)
	b := []byte(body)
	for len(b) > 0 { // in small pieces, as a stream arrives
		n := min(chunk, len(b))
		s.Write(b[:n])
		b = b[n:]
	}
	return s.Finish()
}

func TestUsageScanner(t *testing.T) {
	cases := []struct {
		name string
		sse  bool
		body string
		want Usage
	}{
		{"chat completions stream (usage in the last chunk)", true,
			"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":10}}}\n\n" +
				"data: [DONE]\n\n",
			Usage{Input: 42, Output: 7, CacheRead: 10}},
		{"chat completions JSON", false,
			`{"choices":[{"message":{"content":"Hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
			Usage{Input: 5, Output: 3}},
		{"responses stream (response.completed)", true,
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":64}}}}\n\n",
			Usage{Input: 100, Output: 20, CacheRead: 64}},
		{"anthropic stream (message_start, then cumulative message_delta)", true,
			"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":30,\"output_tokens\":1,\"cache_read_input_tokens\":200,\"cache_creation_input_tokens\":50}}}\r\n\r\n" +
				"event: message_delta\r\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":15}}\r\n\r\n" +
				"event: message_delta\r\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":44}}\r\n\r\n" +
				"event: message_stop\r\ndata: {\"type\":\"message_stop\"}",
			Usage{Input: 30, Output: 44, CacheRead: 200, CacheWrite: 50}},
		{"anthropic JSON", false,
			`{"content":[{"type":"text","text":"Hi"}],"usage":{"input_tokens":12,"output_tokens":4}}`,
			Usage{Input: 12, Output: 4}},
		{"an error reply has none", false, `{"error":{"message":"nope"}}`, Usage{}},
		{"garbage", true, "data: {not json\n\ndata: \n\n", Usage{}},
	}
	for _, c := range cases {
		for _, chunk := range []int{1, 7, 1 << 20} {
			if got := scan(t, c.sse, c.body, chunk); got != c.want {
				t.Errorf("%s (chunks of %d): %+v, want %+v", c.name, chunk, got, c.want)
			}
		}
	}
}

func TestCredential(t *testing.T) {
	id := uuid.New()
	cred := ProxyCredential(id.String(), "tok_en-123")
	for _, header := range []string{"Authorization", "x-api-key"} {
		r := httptest.NewRequest("POST", "/", nil)
		v := cred
		if header == "Authorization" {
			v = "Bearer " + cred
		}
		r.Header.Set(header, v)
		gotID, token, ok := credential(r)
		if !ok || gotID != id || token != "tok_en-123" {
			t.Errorf("%s: %v %q %v", header, gotID, token, ok)
		}
	}
	for _, bad := range []string{"", "Bearer sk-provider-key", "Bearer pdpm.not-a-uuid.tok", "Bearer pdpm." + id.String() + ".", "Bearer pdpm." + id.String()} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Authorization", bad)
		if _, _, ok := credential(r); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCheckBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.openai.com/v1/":     "https://api.openai.com/v1",
		" http://litellm.local:4000/v1 ": "http://litellm.local:4000/v1",
		"https://api.anthropic.com":      "https://api.anthropic.com",
	} {
		if got, err := checkBaseURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "file:///etc/passwd", "ftp://x", "https://user:pw@host/v1", "https://host/v1?key=1", "https://host/#x", "/v1",
		"https://" + strings.Repeat("a", 2048)} {
		if _, err := checkBaseURL(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCheckHeaders(t *testing.T) {
	if err := checkHeaders(map[string]string{"OpenAI-Organization": "org-1", "X-Project": "p"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]string{
		{"Authorization": "Bearer x"}, {"x-api-key": "k"}, {"Host": "evil"}, {"Bad Header": "v"}, {"X-A": "line\nbreak"},
	} {
		if err := checkHeaders(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestKinds(t *testing.T) {
	for _, k := range Kinds {
		if k.API == "" || len(k.ProxyPaths) == 0 || k.ModelsPath == "" || (k.AuthHeader != "authorization" && k.AuthHeader != "x-api-key") {
			t.Errorf("incomplete kind %+v", k)
		}
	}
	if _, ok := KindByID("openai"); !ok {
		t.Fatal("openai missing")
	}
}
