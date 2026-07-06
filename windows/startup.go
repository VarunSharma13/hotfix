//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

// Per-user autostart lives under the HKCU Run key — no admin rights required.
// The Inno Setup installer writes the same value when the user opts into
// "start at sign in"; managing it here keeps the Settings toggle and the
// installer in sync (the registry is the single source of truth for the state).
const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "Hotfix"
)

// launchAtLoginEnabled reports whether the autostart Run value is present.
func launchAtLoginEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValueName)
	return err == nil
}

// setLaunchAtLogin adds or removes the autostart Run value, pointing it at the
// currently running executable. The path is quoted so a location containing
// spaces (e.g. under a username with a space) launches correctly.
func setLaunchAtLogin(enabled bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()

	if enabled {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve exe path: %w", err)
		}
		return k.SetStringValue(runValueName, `"`+exe+`"`)
	}

	if err := k.DeleteValue(runValueName); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("delete Run value: %w", err)
	}
	return nil
}
