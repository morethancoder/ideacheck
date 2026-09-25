// Package sparkjudge is the hosted API behind the Sparkjudge iOS app: the
// check server (package server) plus who may call it (Apple App Attest), how
// much they may run (plans from RevenueCat, a monthly ledger, rate limits and
// a daily spend cap) and the routes the app needs around a check.
// cmd/sparkjudge-api runs it; docs/sparkjudge-api.md describes it.
package sparkjudge

import (
	"bytes"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// ConfigFile is the service's configuration, beside config.yaml in configs/.
const ConfigFile = "sparkjudge.yaml"

// Attest modes.
const (
	AttestRequired = "required"
	AttestOff      = "off"
)

// Plan names. A user is Pro while their entitlement is active.
const (
	PlanFree = "free"
	PlanPro  = "pro"
)

type Config struct {
	Listen      string          `yaml:"listen"`
	DataDir     string          `yaml:"data_dir"`
	Attest      Attest          `yaml:"attest"`
	Plans       map[string]Plan `yaml:"plans"`
	Entitlement string          `yaml:"entitlement"`
	Limits      Limits          `yaml:"limits"`
	// Engine is a config.yaml layer: the check's backends, research and the rest.
	Engine yaml.Node `yaml:"engine"`
}

type Attest struct {
	Mode         string        `yaml:"mode"`
	TeamID       string        `yaml:"team_id"`
	BundleID     string        `yaml:"bundle_id"`
	Development  bool          `yaml:"development"`
	ChallengeTTL time.Duration `yaml:"challenge_ttl"`
}

// AppID is what Apple hashes into every attestation and assertion.
func (a Attest) AppID() string { return a.TeamID + "." + a.BundleID }

type Plan struct {
	ChecksPerMonth int `yaml:"checks_per_month"`
}

type Limits struct {
	RequestsPerMinute int     `yaml:"requests_per_minute"`
	ChecksPerHour     int     `yaml:"checks_per_hour"`
	DailySpendUSD     float64 `yaml:"daily_spend_usd"`
	MaxBodyBytes      int64   `yaml:"max_body_bytes"`
}

// ParseConfig reads sparkjudge.yaml, then the few values the environment may
// set (getenv), and checks the result.
func ParseConfig(raw []byte, getenv func(string) string) (Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	for env, into := range map[string]*string{
		"SPARKJUDGE_LISTEN":   &c.Listen,
		"SPARKJUDGE_DATA_DIR": &c.DataDir,
		"SPARKJUDGE_ATTEST":   &c.Attest.Mode,
		"SPARKJUDGE_TEAM_ID":  &c.Attest.TeamID,
	} {
		if v := getenv(env); v != "" {
			*into = v
		}
	}
	return c, c.Validate()
}

// Validate rejects a configuration the service cannot run safely with.
func (c Config) Validate() error {
	switch c.Attest.Mode {
	case AttestOff:
	case AttestRequired:
		if len(c.Attest.TeamID) != 10 || c.Attest.BundleID == "" {
			return fmt.Errorf("attest.mode is required but the App ID is incomplete: set SPARKJUDGE_TEAM_ID (the 10-character Apple team id) and attest.bundle_id")
		}
	default:
		return fmt.Errorf("attest.mode %q is not one of required, off", c.Attest.Mode)
	}
	if c.Attest.ChallengeTTL <= 0 {
		return fmt.Errorf("attest.challenge_ttl must be positive")
	}
	for _, name := range []string{PlanFree, PlanPro} {
		if p, ok := c.Plans[name]; !ok || p.ChecksPerMonth < 0 {
			return fmt.Errorf("plans.%s.checks_per_month must be set and not negative", name)
		}
	}
	l := c.Limits
	if l.RequestsPerMinute < 1 || l.ChecksPerHour < 1 || l.DailySpendUSD <= 0 || l.MaxBodyBytes < 1 {
		return fmt.Errorf("limits: requests_per_minute, checks_per_hour, daily_spend_usd and max_body_bytes must be positive")
	}
	if c.Entitlement == "" {
		return fmt.Errorf("entitlement names no RevenueCat entitlement")
	}
	return nil
}

// EngineLayer is the engine: section as a config.yaml document.
func (c Config) EngineLayer() ([]byte, error) {
	if c.Engine.Kind == 0 {
		return nil, nil
	}
	return yaml.Marshal(&c.Engine)
}
