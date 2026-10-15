package zitadel

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReadExampleConfig(t *testing.T) {
	file, err := os.Open("config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cfg, err := ReadConfig(file, "separately-supplied-secret")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientSecret != "separately-supplied-secret" || cfg.ProjectID != "123456789" || cfg.TransactionTTL != 5*time.Minute || cfg.DisplayNameClaim != "email" {
		t.Fatal("example configuration was not loaded")
	}
	serialized, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), cfg.ClientSecret) {
		t.Fatal("client secret serialized")
	}
}

func TestReadDisplayNameClaim(t *testing.T) {
	cfg := testConfig()
	cfg.DisplayNameClaim = "preferred_username"
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadConfig(strings.NewReader(string(data)), "secret")
	if err != nil || decoded.DisplayNameClaim != cfg.DisplayNameClaim {
		t.Fatalf("display name claim did not survive JSON configuration: %v", err)
	}
}

func TestReadConfigRejectsAmbiguousInput(t *testing.T) {
	base, _ := json.Marshal(testConfig())
	for _, input := range []string{
		string(base) + " {}",
		strings.TrimSuffix(string(base), "}") + `,"client_secret":"SECRET-embedded"}`,
		strings.TrimSuffix(string(base), "}") + `,"role_mapping":{"operator":["editor"]}}`,
		strings.TrimSuffix(string(base), "}") + `,"transaction_ttl":"SECRET-not-a-duration"}`,
		strings.TrimSuffix(string(base), "}") + `,"transaction_ttl":"1h"}`,
		strings.Repeat("x", (64<<10)+1),
		`null`,
	} {
		_, err := ReadConfig(strings.NewReader(input), "secret")
		if err == nil {
			t.Fatal("ambiguous or invalid configuration accepted")
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatal("raw configuration leaked in error")
		}
	}
}

func TestReadConfigDurations(t *testing.T) {
	base, _ := json.Marshal(testConfig())
	input := strings.TrimSuffix(string(base), "}") + `,"transaction_ttl":"2m","max_authentication_age":"10m"}`
	cfg, err := ReadConfig(strings.NewReader(input), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TransactionTTL != 2*time.Minute || cfg.MaxAuthenticationAge != 10*time.Minute {
		t.Fatal("duration configuration lost")
	}
}
