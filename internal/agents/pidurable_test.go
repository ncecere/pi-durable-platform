package agents

import (
	"encoding/json"
	"os"
	"testing"
)

// ShippedPiDurableVersion is the pin in runtime/package.json.
func TestShippedPiDurableVersionMatchesTheRuntime(t *testing.T) {
	raw, err := os.ReadFile("../../runtime/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	if got := pkg.Dependencies["@earendil-works/pi-durable"]; got != ShippedPiDurableVersion {
		t.Fatalf("runtime/package.json pins pi-durable %q, ShippedPiDurableVersion is %q: change the constant with the pin", got, ShippedPiDurableVersion)
	}
}

// The upgrade check records a reported version only up to one minor
// version ahead of the shipped one, within its major.
func TestRecordable(t *testing.T) {
	if recordCeiling != "v1.5.0-0" && ShippedPiDurableVersion == "1.3.0" {
		t.Fatalf("ceiling %s", recordCeiling)
	}
	for v, want := range map[string]bool{
		ShippedPiDurableVersion: true, "1.0.0": true, "1.3.7": true, "1.4.0": true, "1.4.99": true, "1.4.0-rc.1": true,
		"1.5.0": false, "1.5.0-0": false, "1.5.0-rc.1": false, "1.999999.0": false, "2.0.0": false, "999999.0.0": false, "0.9.0": false,
		"": false, "1.3": false, "v1.3.0": false, "not-a-version": false,
	} {
		if got := recordable(v); got != want {
			t.Errorf("recordable(%q) = %v, want %v", v, got, want)
		}
	}
}
