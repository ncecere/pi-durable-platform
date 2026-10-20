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

// The reset's flags are checked before anything is read or changed.
func TestResetStorageVersionFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "--agent must be the agent's ID"},
		{[]string{"--agent", "nope"}, "--agent must be the agent's ID"},
		{[]string{"--agent", "6f1c2a52-6e0b-4c3e-9d55-0d0b9f2b8a11", "extra"}, "takes no arguments"},
	} {
		err := runResetStorageVersion(tc.args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
	if err := runResetStorageVersion([]string{"--help"}, io.Discard); err != nil {
		t.Errorf("--help: %v", err)
	}
}
