package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
		claimed, err := tc.config.ClaimScreenView(tc.screen, tc.at, func() error { return nil })
		require.NoError(t, err)
		assert.Equal(t, tc.want, claimed)
	}
	c.InstallationID = "install-two"
	claimed, err := c.ClaimScreenView("sessions", now.Add(time.Minute), func() error { return nil })
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
	var sends atomic.Int32
	results := make(chan bool, 8)
	for range 8 {
		wg.Go(func() {
			c := Config{DataDir: dir, InstallationID: "install-one"}
			claimed, err := c.ClaimScreenView("settings", now, func() error {
				sends.Add(1)
				return nil
			})
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
	assert.EqualValues(t, 1, sends.Load())
}

func TestClaimScreenViewFailure(t *testing.T) {
	for _, tc := range []struct {
		name           string
		storageFailure bool
		writeFailure   bool
	}{{"enqueue", false, false}, {"storage", true, false}, {"write", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{DataDir: t.TempDir(), InstallationID: "install-one"}
			path := filepath.Join(c.DataDir, telemetryScreensFilename)
			if tc.storageFailure {
				require.NoError(t, os.Mkdir(path, 0o700))
			}
			sends := 0
			claimed, err := c.ClaimScreenView("sessions", time.Now(), func() error {
				sends++
				if tc.writeFailure {
					return os.Mkdir(path, 0o700)
				}
				return errors.New("enqueue failed")
			})
			require.Error(t, err)
			assert.Equal(t, tc.writeFailure, claimed)
			if tc.writeFailure {
				assert.Equal(t, 1, sends)
				return
			}
			if tc.storageFailure {
				assert.Zero(t, sends)
				return
			}
			assert.NoFileExists(t, path)
			claimed, err = c.ClaimScreenView("sessions", time.Now(), func() error { sends++; return nil })
			require.NoError(t, err)
			assert.True(t, claimed)
			assert.Equal(t, 2, sends)
		})
	}
}
