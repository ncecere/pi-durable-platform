// Package mcp is the platform's MCP catalog: remote MCP servers platform
// admins publish to teams, the servers template versions give their agents,
// servers apps give an agent for one thread, and the gateway agents call them
// through (proxy mode), which adds the server's headers, allows only the
// tools the agent may use and logs each call.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ProtocolVersion is the MCP version the platform asks for.
const ProtocolVersion = "2025-06-18"

// Tool is one tool of a server, as listed.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

const (
	maxTools        = 500
	maxSchemaBytes  = 64 << 10
	maxReplyBytes   = 8 << 20
	listToolsBudget = 20 * time.Second
)

func newHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = 5 * time.Minute
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// session is one MCP session with a server (Streamable HTTP).
type session struct {
	client  *http.Client
	url     string
	headers map[string]string
	id      string // Mcp-Session-Id
	nextID  int
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// call sends a request (id > 0) or a notification and returns the result.
func (s *session) call(ctx context.Context, method string, params any, notify bool) (json.RawMessage, error) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	var id int
	if !notify {
		s.nextID++
		id = s.nextID
		msg["id"] = id
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if s.id != "" {
		req.Header.Set("Mcp-Session-Id", s.id)
		req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, &reachError{err: err}
	}
	defer resp.Body.Close()
	if v := resp.Header.Get("Mcp-Session-Id"); v != "" {
		s.id = v
	}
	if notify {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("the server answered %d to %s", resp.StatusCode, method)
		}
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server answered %d to %s", resp.StatusCode, method)
	}
	reply, err := readReply(resp, id)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if reply.Error != nil {
		return nil, fmt.Errorf("%s: the server returned error %d: %s", method, reply.Error.Code, truncate(reply.Error.Message, 200))
	}
	return reply.Result, nil
}

// reachError: the server could not be reached (connection, TLS, timeout).
// Its text names the URL's error; it unwraps to the client's error (for
// netguard.ErrBlockedAddress).
type reachError struct{ err error }

func (e *reachError) Error() string {
	return fmt.Sprintf("could not reach the server: %v", errors.Unwrap(e.err))
}

func (e *reachError) Unwrap() error { return e.err }

// readReply reads the response to request id: a JSON body, or the matching
// message of a server-sent event stream.
func readReply(resp *http.Response, id int) (rpcReply, error) {
	body := io.LimitReader(resp.Body, maxReplyBytes)
	want := fmt.Sprint(id)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var r rpcReply
		if err := json.NewDecoder(body).Decode(&r); err != nil {
			return r, errors.New("the reply is not JSON-RPC")
		}
		return r, nil
	}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), maxReplyBytes)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimPrefix(v, " "))
			continue
		}
		if line != "" || data.Len() == 0 {
			continue
		}
		var r rpcReply
		if json.Unmarshal([]byte(data.String()), &r) == nil && string(r.ID) == want {
			return r, nil
		}
		data.Reset()
	}
	if data.Len() > 0 {
		var r rpcReply
		if json.Unmarshal([]byte(data.String()), &r) == nil && string(r.ID) == want {
			return r, nil
		}
	}
	return rpcReply{}, errors.New("the stream ended without a reply")
}

func (s *session) close() {
	if s.id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.url, nil)
	if err != nil {
		return
	}
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Mcp-Session-Id", s.id)
	if resp, err := s.client.Do(req); err == nil {
		resp.Body.Close()
	}
}

// ListTools connects to a server and lists its tools (at most 500; large
// input schemas are dropped to an empty object schema).
func ListTools(ctx context.Context, client *http.Client, url string, headers map[string]string) ([]Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, listToolsBudget)
	defer cancel()
	s := &session{client: client, url: url, headers: headers}
	if _, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "pi-durable-platform", "version": "1"},
	}, false); err != nil {
		return nil, err
	}
	defer s.close()
	if _, err := s.call(ctx, "notifications/initialized", nil, true); err != nil {
		return nil, err
	}
	var tools []Tool
	cursor := ""
	for range 50 {
		var params any
		if cursor != "" {
			params = map[string]string{"cursor": cursor}
		}
		raw, err := s.call(ctx, "tools/list", params, false)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, errors.New("tools/list: unexpected result")
		}
		for _, t := range page.Tools {
			if t.Name == "" || len(t.Name) > 128 || len(tools) >= maxTools {
				continue
			}
			if len(t.InputSchema) == 0 || len(t.InputSchema) > maxSchemaBytes {
				t.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			t.Title, t.Description = truncate(t.Title, 200), truncate(t.Description, 4000)
			tools = append(tools, t)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return tools, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
