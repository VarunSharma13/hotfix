//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Config holds all user-configurable settings for Hotfix.
type Config struct {
	Enabled          bool     `json:"enabled"`
	CPUThreshold     float64  `json:"cpu_threshold"`      // percent, default 80.0
	KillDuration     float64  `json:"kill_duration"`      // seconds before kill, default 60
	KillOnSleep      bool     `json:"kill_on_sleep"`      // kill hot procs on system sleep
	ProtectActiveApp bool     `json:"protect_active_app"` // never kill the foreground app
	LaunchAtLogin    bool     `json:"launch_at_login"`    // start automatically at sign-in
	Whitelist        []string `json:"whitelist"`          // user-managed exclusions
}

var defaultWhitelist = []string{
	"explorer", "svchost", "lsass", "csrss", "wininit",
	"services", "winlogon",
}

func defaultConfig() Config {
	return Config{
		Enabled:          true,
		CPUThreshold:     80.0,
		KillDuration:     60.0,
		KillOnSleep:      true,
		ProtectActiveApp: true,
		Whitelist:        defaultWhitelist,
	}
}

// configPath returns the path to the config file inside %APPDATA%\Hotfix\.
func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Hotfix", "config.json"), nil
}

var (
	configMu sync.RWMutex
	current  Config
)

// loadConfig reads config from disk, falling back to defaults on any error.
func loadConfig() Config {
	path, err := configPath()
	if err != nil {
		logf("config: cannot determine config path: %v", err)
		return defaultConfig()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// First run or missing file — return defaults and persist them. The
		// installer may already have set the autostart Run value, so reflect the
		// real registry state rather than a hardcoded default.
		cfg := defaultConfig()
		cfg.LaunchAtLogin = launchAtLoginEnabled()
		_ = saveConfig(cfg)
		return cfg
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		logf("config: parse error (%v), using defaults", err)
		return defaultConfig()
	}

	// Migrate pre-existing configs that predate protect_active_app: an absent
	// field unmarshals to false, but the intended default is ON. Detect absence
	// with a pointer probe so an explicit `false` is preserved.
	var probe struct {
		ProtectActiveApp *bool `json:"protect_active_app"`
	}
	if json.Unmarshal(data, &probe) == nil && probe.ProtectActiveApp == nil {
		cfg.ProtectActiveApp = true
	}

	// Guard against invalid values.
	if cfg.CPUThreshold < 1 || cfg.CPUThreshold > 100 {
		cfg.CPUThreshold = 80.0
	}
	if cfg.KillDuration < 5 {
		cfg.KillDuration = 60.0
	}
	if cfg.Whitelist == nil {
		cfg.Whitelist = defaultWhitelist
	}

	// The autostart registry value is the source of truth for this toggle (the
	// installer or the user may change it outside the app), so always reflect
	// the live registry state rather than the persisted JSON.
	cfg.LaunchAtLogin = launchAtLoginEnabled()

	return cfg
}

// saveConfig persists cfg to disk, creating the directory if needed.
func saveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// getConfig returns the current in-memory config (thread-safe).
func getConfig() Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return current
}

// setConfig atomically replaces the in-memory config and saves to disk.
func setConfig(cfg Config) error {
	configMu.Lock()
	current = cfg
	configMu.Unlock()
	return saveConfig(cfg)
}
