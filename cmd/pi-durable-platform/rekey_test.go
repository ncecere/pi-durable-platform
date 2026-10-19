package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/rekey"
)

func TestRekeyFlags(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{"--batch", "0"}, {"--batch", "100000"}, {"extra"}, {"--nope"}} {
		if err := runRekey(args, &out); err == nil {
			t.Errorf("rekey %v accepted", args)
		}
	}
	if err := runRekey([]string{"--help"}, &out); err != nil {
		t.Errorf("--help: %v", err)
	}
}

func TestWriteRekey(t *testing.T) {
	res := rekey.Result{Columns: []rekey.ColumnResult{
		{Table: "agents", Column: "db_password_enc", Current: 1, Previous: 2, Resealed: 2},
		{Table: "mcp_servers", Column: "headers_enc", Previous: 1, Unreadable: 1, Changed: 1},
	}}
	var out bytes.Buffer
	writeRekey(&out, res)
	for _, want := range []string{"agents.db_password_enc", "re-sealed", "mcp_servers.headers_enc", "re-sealed 2 value(s)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	res.DryRun = true
	writeRekey(&out, res)
	if strings.Contains(out.String(), "changed") || !strings.Contains(out.String(), "dry run: 1 value(s) under ENCRYPTION_KEY, 3 under the previous key") {
		t.Errorf("dry run output:\n%s", out.String())
	}
}
