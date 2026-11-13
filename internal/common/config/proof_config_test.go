package config

import (
	"testing"
	"time"
)

func TestGetProofMaxAge(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		name string
		cfg  *AutoupdateConfig
		want time.Duration
	}{
		{"nil config", nil, 7 * day},
		{"unset", &AutoupdateConfig{}, 7 * day},
		{"non-positive", &AutoupdateConfig{Proof: ProofConfig{MaxAgeDays: -1}}, 7 * day},
		{"set", &AutoupdateConfig{Proof: ProofConfig{MaxAgeDays: 3}}, 3 * day},
	} {
		if got := tc.cfg.GetProofMaxAge(); got != tc.want {
			t.Errorf("%s: GetProofMaxAge = %s, want %s", tc.name, got, tc.want)
		}
	}
}
