package sparkjudge

import (
	"strings"
	"testing"

	"github.com/morethancoder/ideacheck/configs"
)

func env(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

// The shipped file needs only the team id to run for real, and says so when
// it is missing rather than serving unattested.
func TestShippedConfig(t *testing.T) {
	raw, err := configs.Defaults().Read(ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConfig(raw, env(nil)); err == nil || !strings.Contains(err.Error(), "SPARKJUDGE_TEAM_ID") {
		t.Errorf("no team id: err = %v", err)
	}
	c, err := ParseConfig(raw, env(map[string]string{"SPARKJUDGE_TEAM_ID": "ABCDE12345", "SPARKJUDGE_DATA_DIR": "/tmp/x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Attest.AppID() != "ABCDE12345.com.morethancoder.sparkjudge" || c.DataDir != "/tmp/x" || c.Plans[PlanFree].ChecksPerMonth != 3 || c.Plans[PlanPro].ChecksPerMonth != 100 {
		t.Errorf("config = %+v", c)
	}
	layer, err := c.EngineLayer()
	if err != nil || !strings.Contains(string(layer), "z-ai/glm-5.3-flash") || !strings.Contains(string(layer), "backend: jev") {
		t.Errorf("engine layer = %s, %v", layer, err)
	}
	if _, err := ParseConfig(raw, env(map[string]string{"SPARKJUDGE_ATTEST": "maybe"})); err == nil {
		t.Error("an unknown attest mode was accepted")
	}
}
