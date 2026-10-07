package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const telemetryScreensFilename = "telemetry-screen-views"

// ClaimScreenView persists a daily claim before the daemon queues the event.
func (c *Config) ClaimScreenView(screen string, now time.Time) (bool, error) {
	claimed := false
	err := c.withConfigLock(func() error {
		data, err := os.ReadFile(filepath.Join(c.DataDir, telemetryScreensFilename))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		day := now.UTC().Format(time.DateOnly)
		fields := strings.Fields(string(data))
		if len(fields) < 2 || fields[0] != c.InstallationID || fields[1] != day {
			fields = []string{c.InstallationID, day}
		}
		if slices.Contains(fields[2:], screen) {
			return nil
		}
		if err := c.writeInstallationFile(telemetryScreensFilename, strings.Join(append(fields, screen), " ")); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed, err
}
