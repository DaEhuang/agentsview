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
	now := time.Date(2026, 7, 1, 23, 59, 0, 0, time.UTC)
	for _, tc := range []struct {
		screen            string
		at                time.Time
		want              bool
		failure, identity string
	}{
		{"sessions", now, true, "", ""},
		{"sessions", now, false, "", ""},
		{"usage", now, true, "", ""},
		{"sessions", now.Add(time.Minute), true, "", ""},
		{"sessions", now.Add(time.Minute).In(time.FixedZone("west", -7*3600)), false, "", ""},
		{"sessions", now.Add(time.Minute), true, "", "install-two"},
		{"sessions", now, false, "enqueue", ""},
		{"sessions", now, false, "storage", ""},
		{"sessions", now, true, "write", ""},
	} {
		if tc.identity != "" {
			c.InstallationID = tc.identity
		}
		if tc.failure != "" {
			c.DataDir = t.TempDir()
		}
		path := filepath.Join(c.DataDir, telemetryScreensFilename)
		if tc.failure == "storage" {
			require.NoError(t, os.Mkdir(path, 0o700))
		}
		sends := 0
		claimed, err := c.ClaimScreenView(tc.screen, tc.at, func() error {
			sends++
			switch tc.failure {
			case "enqueue":
				return errors.New("enqueue failed")
			case "write":
				return os.Mkdir(path, 0o700)
			default:
				return nil
			}
		})
		assert.Equal(t, tc.want, claimed)
		if tc.failure == "" {
			require.NoError(t, err)
			if tc.identity != "" {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, "install-two 2026-07-02 sessions\n", string(data))
			}
			continue
		}
		require.Error(t, err)
		switch tc.failure {
		case "storage":
			assert.Zero(t, sends)
		case "write":
			assert.Equal(t, 1, sends)
		case "enqueue":
			assert.NoFileExists(t, path)
			claimed, err = c.ClaimScreenView(tc.screen, tc.at, func() error { sends++; return nil })
			require.NoError(t, err)
			assert.True(t, claimed)
			assert.Equal(t, 2, sends)
		}
	}
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
