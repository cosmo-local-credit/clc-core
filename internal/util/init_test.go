package util

import (
	"log/slog"
	"os"
	"testing"
)

func TestInitConfig_SpaceSeparatedEnvVarSplitIntoSlice(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "config-*.toml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[jetstream]\nid = \"test\"\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	t.Setenv("CUSTODIAL_JETSTREAM__ENDPOINTS", "nats://10.0.0.2:4222 nats://10.0.0.3:4222 nats://10.0.0.4:4222")

	ko := InitConfig(slog.Default(), f.Name())

	got := ko.Strings("jetstream.endpoints")
	want := []string{"nats://10.0.0.2:4222", "nats://10.0.0.3:4222", "nats://10.0.0.4:4222"}

	if len(got) != len(want) {
		t.Fatalf("expected %d endpoints, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("endpoint[%d]: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestInitConfig_SingleEnvVarNotSplit(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "config-*.toml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[jetstream]\nid = \"default\"\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	t.Setenv("CUSTODIAL_JETSTREAM__ID", "sn-gas-approver-prod")

	ko := InitConfig(slog.Default(), f.Name())

	if got := ko.String("jetstream.id"); got != "sn-gas-approver-prod" {
		t.Errorf("expected %q, got %q", "sn-gas-approver-prod", got)
	}
}
