package zitadel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ReadConfig strictly decodes server-side JSON and supplies the client secret
// separately. Duration settings use strings such as "5m". Unknown fields,
// trailing JSON, embedded secrets, and configuration above 64 KiB are rejected.
func ReadConfig(reader io.Reader, clientSecret string) (Config, error) {
	data, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil {
		return Config{}, errors.New("read OIDC configuration failed")
	}
	if len(data) > 64<<10 {
		return Config{}, errors.New("OIDC configuration exceeds 64 KiB")
	}
	var file struct {
		Config
		TransactionTTL       string `json:"transaction_ttl,omitempty"`
		MaxAuthenticationAge string `json:"max_authentication_age,omitempty"`
	}
	// Avoid including raw configuration in parse errors: unknown field names
	// and malformed values may contain operator-supplied secrets.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return Config{}, errors.New("invalid OIDC configuration JSON or unknown field")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("OIDC configuration must contain exactly one JSON object")
	}
	cfg := file.Config
	cfg.ClientSecret = clientSecret
	for _, item := range []struct {
		name, value string
		target      *time.Duration
	}{
		{"transaction_ttl", file.TransactionTTL, &cfg.TransactionTTL},
		{"max_authentication_age", file.MaxAuthenticationAge, &cfg.MaxAuthenticationAge},
	} {
		if item.value == "" {
			continue
		}
		duration, err := time.ParseDuration(item.value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid duration for %s", item.name)
		}
		*item.target = duration
	}
	return cfg.validated()
}
