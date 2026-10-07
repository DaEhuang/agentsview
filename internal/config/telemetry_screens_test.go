package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaimScreenView(t *testing.T) {
	c := Config{DataDir: t.TempDir(), InstallationID: "install-one"}
	other := c
	now := time.Date(2026, 7, 1, 23, 59, 0, 0, time.UTC)
	for _, tc := range []struct {
		config *Config
		screen string
		at     time.Time
		want   bool
	}{
		{&c, "sessions", now, true},
		{&other, "sessions", now, false},
		{&other, "usage", now, true},
		{&c, "sessions", now.Add(time.Minute), true},
		{&other, "sessions", now.Add(time.Minute).In(time.FixedZone("west", -7*3600)), false},
	} {
		claimed, err := tc.config.ClaimScreenView(tc.screen, tc.at)
		require.NoError(t, err)
		assert.Equal(t, tc.want, claimed)
	}
	c.InstallationID = "install-two"
	claimed, err := c.ClaimScreenView("sessions", now.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, claimed)
	data, err := os.ReadFile(filepath.Join(c.DataDir, telemetryScreensFilename))
	require.NoError(t, err)
	assert.Equal(t, "install-two 2026-07-02 sessions\n", string(data))
}

func TestClaimScreenViewConcurrent(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	for range 8 {
		wg.Go(func() {
			c := Config{DataDir: dir, InstallationID: "install-one"}
			claimed, err := c.ClaimScreenView("settings", now)
			assert.NoError(t, err)
			results <- claimed
		})
	}
	wg.Wait()
	close(results)
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	assert.Equal(t, 1, claims)
}

func TestClaimScreenViewStorageFailure(t *testing.T) {
	c := Config{DataDir: t.TempDir(), InstallationID: "install-one"}
	require.NoError(t, os.Mkdir(filepath.Join(c.DataDir, telemetryScreensFilename), 0o700))
	claimed, err := c.ClaimScreenView("sessions", time.Now())
	assert.Error(t, err)
	assert.False(t, claimed)
}
