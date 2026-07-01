package egress

import (
	"os"
	"testing"
)

func TestRegistryFromConfigSelectsDefaultAndRequested(t *testing.T) {
	t.Setenv("WB_EGRESS_TEST_PASSWORD", "secret")
	reg, err := RegistryFromConfig(Config{
		SchemaVersion: 1,
		DefaultEgress: "direct",
		Egresses: []Profile{
			{ID: "direct", Type: TypeDirect, Enabled: true},
			{ID: "de-fra-1", Type: TypeSOCKS5, Address: "127.0.0.1:1080", Username: "user", PasswordEnv: "WB_EGRESS_TEST_PASSWORD", Enabled: true},
			{ID: "disabled", Type: TypeDirect, Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("RegistryFromConfig() error = %v", err)
	}
	if _, id, err := reg.Select(""); err != nil || id != "direct" {
		t.Fatalf("Select(default) = id %q err %v", id, err)
	}
	if _, id, err := reg.Select("de-fra-1"); err != nil || id != "de-fra-1" {
		t.Fatalf("Select(requested) = id %q err %v", id, err)
	}
	if _, _, err := reg.Select("disabled"); err == nil {
		t.Fatal("Select(disabled) expected error")
	}
}

func TestRegistryRejectsInvalidConfig(t *testing.T) {
	tests := []Config{
		{SchemaVersion: 2, DefaultEgress: "direct", Egresses: []Profile{{ID: "direct", Type: TypeDirect, Enabled: true}}},
		{SchemaVersion: 1, DefaultEgress: "direct", Egresses: []Profile{{ID: "../bad", Type: TypeDirect, Enabled: true}}},
		{SchemaVersion: 1, DefaultEgress: "missing", Egresses: []Profile{{ID: "direct", Type: TypeDirect, Enabled: true}}},
		{SchemaVersion: 1, DefaultEgress: "socks", Egresses: []Profile{{ID: "socks", Type: TypeSOCKS5, Enabled: true}}},
	}
	for _, tt := range tests {
		if _, err := RegistryFromConfig(tt); err == nil {
			t.Fatalf("RegistryFromConfig(%+v) expected error", tt)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "egress-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"schemaVersion":1,"defaultEgress":"direct","egresses":[{"id":"direct","type":"direct","enabled":true}]}`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadConfig(file.Name())
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if _, id, err := reg.Select(""); err != nil || id != "direct" {
		t.Fatalf("Select(default) = id %q err %v", id, err)
	}
}

func TestRegistryDescriptorsAreSortedAndMarkDefault(t *testing.T) {
	registry, err := NewRegistry("fi", DirectDialer{ProfileID: "fi"}, DirectDialer{ProfileID: "ee"})
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.Descriptors()
	if len(descriptors) != 2 || descriptors[0].ID != "ee" || descriptors[0].IsDefault || descriptors[1].ID != "fi" || !descriptors[1].IsDefault {
		t.Fatalf("Descriptors() = %+v", descriptors)
	}
}

func TestRegistryRejectsInvalidProbeAddress(t *testing.T) {
	_, err := RegistryFromConfig(Config{
		SchemaVersion: 1,
		DefaultEgress: "direct",
		ProbeAddress:  "missing-port",
		Egresses:      []Profile{{ID: "direct", Type: TypeDirect, Enabled: true}},
	})
	if err == nil {
		t.Fatal("RegistryFromConfig() expected invalid probeAddress error")
	}
}
