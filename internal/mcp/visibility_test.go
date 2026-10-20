package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// A tool an MCP App marks app-only (`_meta.ui.visibility` without "model") is
// for the app's own interface: the MCP Apps extension says a host must not
// offer it to the model, so the platform never lists it as an agent's tool.
func TestListToolsLeavesOutAppOnlyTools(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	tools := []any{
		map[string]any{"name": "plain", "inputSchema": schema},
		map[string]any{"name": "both", "inputSchema": schema, "_meta": map[string]any{"ui": map[string]any{
			"resourceUri": "ui://app/view.html", "visibility": []string{"model", "app"}}}},
		map[string]any{"name": "model_only", "inputSchema": schema, "_meta": map[string]any{"ui": map[string]any{"visibility": []string{"model"}}}},
		map[string]any{"name": "app_only", "inputSchema": schema, "_meta": map[string]any{"ui": map[string]any{"visibility": []string{"app"}}}},
		map[string]any{"name": "nobody", "inputSchema": schema, "_meta": map[string]any{"ui": map[string]any{"visibility": []string{}}}},
		// Metadata the platform doesn't understand keeps the default (visible).
		map[string]any{"name": "odd_meta", "inputSchema": schema, "_meta": map[string]any{"ui": "not an object"}},
		map[string]any{"name": "other_meta", "inputSchema": schema, "_meta": map[string]any{"vendor/x": 1}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "apps", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": tools}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}))
	t.Cleanup(srv.Close)

	got, err := ListTools(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range got {
		names = append(names, tool.Name)
	}
	want := []string{"plain", "both", "model_only", "odd_meta", "other_meta"}
	if !slices.Equal(names, want) {
		t.Fatalf("listed %v, want %v", names, want)
	}
}
