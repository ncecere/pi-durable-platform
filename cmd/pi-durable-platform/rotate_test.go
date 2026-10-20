package main

import (
	"io"
	"strings"
	"testing"
)

// The rotation command's flags are checked before anything is read or
// changed.
func TestRotateMCPIdentityKeyFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--drop-previous"}, "--drop-previous needs --now"},
		{[]string{"extra"}, "takes no arguments"},
		{[]string{"--prepublish", "soon"}, "invalid value"},
	} {
		err := runRotateMCPIdentityKey(tc.args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
	if err := runRotateMCPIdentityKey([]string{"--help"}, io.Discard); err != nil {
		t.Errorf("--help: %v", err)
	}
}
