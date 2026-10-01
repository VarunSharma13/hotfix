//go:build linux

package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"
)

// Tray icon: the color flame, same PNG the Windows tray uses. The Linux tray
// (StatusNotifierItem) takes PNG bytes directly, so no ICO wrapping is needed.
//
//go:embed assets/flame32.png
var flamePNG []byte

const defaultTooltip = "Hotfix — by BuildCraft Labs"

// The tray menu is the whole settings UI on Linux (there is no settings
// window): toggles are checkboxes, and the two numeric settings are submenus of
// presets. Any other value — and the whitelist — is set in config.json, which
// "Edit Config File…" opens and which is reloaded automatically on save.
var (
	thresholdPresets = []float64{50, 60, 70, 80, 90, 100} // percent
	durationPresets  = []float64{15, 30, 60, 120, 300}    // seconds
)

type presetItem struct {
	value float64
	item  *systray.MenuItem
}

// --- Tray state ---
var (
	mStatus    *systray.MenuItem
	mToggle    *systray.MenuItem
	mThreshold *systray.MenuItem
	mDuration  *systray.MenuItem
	mSleep     *systray.MenuItem
	mProtect   *systray.MenuItem
	mStartup   *systray.MenuItem

	thresholdItems []presetItem
	durationItems  []presetItem

	trayMu        sync.Mutex
	statusTimeout *time.Timer
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println("Hotfix", currentVersion)
		return
	}

	// Set up logging before anything else.
	initLog()

	if !acquireInstanceLock() {
		logf("Hotfix already running; exiting")
		fmt.Fprintln(os.Stderr, "Hotfix is already running.")
		return
	}
	logf("Hotfix starting (version %s)", currentVersion)

	// If a previous run left a crash behind, surface it for one-click reporting.
	reportPendingCrash()

	// Load config.
	cfg := loadConfig()
	configMu.Lock()
	current = cfg
	configMu.Unlock()

	// Hand control to systray — onReady and onExit run on its goroutine.
	systray.Run(onReady, onExit)
}

// instanceLock is held for the life of the process so a second launch (e.g.
// autostart plus a manual start) doesn't run two monitors. It is close-on-exec,
// so the updater's re-exec releases it and the new image takes it again.
var instanceLock *os.File

func acquireInstanceLock() bool {
	dir := stateDir()
	if dir == "" {
		return true // can't lock; don't block startup over it
	}
	_ = os.MkdirAll(dir, 0755)
	f, err := os.OpenFile(filepath.Join(dir, "hotfix.lock"), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	instanceLock = f
	return true
}

func onReady() {
	// Set tray icon and tooltip.
	systray.SetIcon(flamePNG)
	systray.SetTitle("Hotfix")
	systray.SetTooltip(defaultTooltip)

	// Build menu.
	mStatus = systray.AddMenuItem("Hotfix — Watching", "")
	mStatus.Disable()
	systray.AddSeparator()

	mToggle = systray.AddMenuItemCheckbox("Enable Monitoring", "", false)

	mThreshold = systray.AddMenuItem("CPU Threshold", "Kill processes above this CPU usage")
	for _, v := range thresholdPresets {
		it := mThreshold.AddSubMenuItemCheckbox(fmt.Sprintf("%.0f%%", v), "", false)
		thresholdItems = append(thresholdItems, presetItem{v, it})
		onClick(it, "threshold", func() { updateConfig(func(c *Config) { c.CPUThreshold = v }) })
	}

	mDuration = systray.AddMenuItem("Kill After", "How long a process must stay hot before it is killed")
	for _, v := range durationPresets {
		it := mDuration.AddSubMenuItemCheckbox(formatDuration(v), "", false)
		durationItems = append(durationItems, presetItem{v, it})
		onClick(it, "duration", func() { updateConfig(func(c *Config) { c.KillDuration = v }) })
	}

	mSleep = systray.AddMenuItemCheckbox("Kill on Sleep", "Kill hot processes when the system suspends", false)
	mProtect = systray.AddMenuItemCheckbox("Protect Active App", "Never kill the app you're currently using", false)
	mStartup = systray.AddMenuItemCheckbox("Start at Login", "Launch Hotfix when you log in", false)
	systray.AddSeparator()

	mConfig := systray.AddMenuItem("Edit Config File…", "Edit the whitelist and other settings")
	mLog := systray.AddMenuItem("View Log", "Open the Hotfix log")
	mUpdate := systray.AddMenuItem("Check for Updates", "Check GitHub for a newer release")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit Hotfix")

	// Apply the loaded config: starts the monitor if enabled and syncs the menu.
	applyRuntime(getConfig())

	// Watch for system suspend (KillOnSleep support).
	watchSleep()

	// Pick up hand edits to config.json.
	watchConfigFile()

	// Begin silent background auto-updates (launch check + periodic poll).
	startAutoUpdater()

	// Click handlers. Each runs through safe(), so a panic in one handler is
	// captured and reported rather than silently killing the tray.
	onClick(mToggle, "toggle", func() { updateConfig(func(c *Config) { c.Enabled = !c.Enabled }) })
	onClick(mSleep, "kill-on-sleep", func() { updateConfig(func(c *Config) { c.KillOnSleep = !c.KillOnSleep }) })
	onClick(mProtect, "protect-active", func() { updateConfig(func(c *Config) { c.ProtectActiveApp = !c.ProtectActiveApp }) })
	onClick(mStartup, "start-at-login", func() { updateConfig(func(c *Config) { c.LaunchAtLogin = !c.LaunchAtLogin }) })
	onClick(mConfig, "edit-config", openConfigFile)
	onClick(mLog, "view-log", openLogFile)
	onClick(mUpdate, "update", func() { safeGo("update", func() { checkForUpdates(false) }) })
	onClick(mQuit, "quit", systray.Quit)
}

func onExit() {
	stopMonitor()
	logf("Hotfix exiting")
	closeLog()
}

// onClick runs fn for every click on item, on a dedicated goroutine.
func onClick(item *systray.MenuItem, name string, fn func()) {
	safeGo("menu-"+name, func() {
		for range item.ClickedCh {
			safe(name, fn)
		}
	})
}

// updateConfig applies one change from the tray menu to the current config.
func updateConfig(mutate func(*Config)) {
	applyMu.Lock()
	defer applyMu.Unlock()

	cfg := getConfig()
	mutate(&cfg)
	if err := applyConfig(cfg); err != nil {
		logf("settings: rejected change: %v", err)
	}
}

// applyConfig validates cfg, persists it, applies the autostart toggle, and
// brings the monitor and tray menu in line. The caller must hold applyMu.
func applyConfig(cfg Config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if cfg.Whitelist == nil {
		cfg.Whitelist = []string{}
	}

	// Apply the autostart toggle first and record what actually happened, so a
	// failure leaves the checkbox reflecting the real state.
	if cfg.LaunchAtLogin != launchAtLoginEnabled() {
		if err := setLaunchAtLogin(cfg.LaunchAtLogin); err != nil {
			logf("settings: launch-at-login toggle failed: %v", err)
		} else {
			logf("settings: launch at login %v", cfg.LaunchAtLogin)
		}
		cfg.LaunchAtLogin = launchAtLoginEnabled()
	}

	if err := setConfig(cfg); err != nil {
		logf("settings: save config error: %v", err)
		return fmt.Errorf("save error")
	}
	logf("settings: config saved (enabled=%v, threshold=%.0f%%, kill_after=%.0fs)",
		cfg.Enabled, cfg.CPUThreshold, cfg.KillDuration)

	applyRuntime(cfg)
	return nil
}

// applyRuntime starts/stops the monitor to match cfg.Enabled and refreshes the
// tray. Both monitor calls are idempotent.
func applyRuntime(cfg Config) {
	if cfg.Enabled {
		startMonitor()
		setTrayStatus("Watching", false)
	} else {
		stopMonitor()
		setTrayStatus("Disabled", false)
	}
	syncMenu()
}

// syncMenu updates every checkbox and preset label to reflect the config.
// nil-guarded so it is safe to call before the tray menu is built (e.g. from
// tests) without panicking.
func syncMenu() {
	if mToggle == nil {
		return
	}
	cfg := getConfig()

	setChecked(mToggle, cfg.Enabled)
	setChecked(mSleep, cfg.KillOnSleep)
	setChecked(mProtect, cfg.ProtectActiveApp)
	setChecked(mStartup, cfg.LaunchAtLogin)

	// The current value is shown in the parent label, so a hand-edited value
	// that matches no preset is still visible.
	mThreshold.SetTitle(fmt.Sprintf("CPU Threshold: %.0f%%", cfg.CPUThreshold))
	for _, p := range thresholdItems {
		setChecked(p.item, p.value == cfg.CPUThreshold)
	}
	mDuration.SetTitle("Kill After: " + formatDuration(cfg.KillDuration))
	for _, p := range durationItems {
		setChecked(p.item, p.value == cfg.KillDuration)
	}
}

func setChecked(item *systray.MenuItem, on bool) {
	if on {
		item.Check()
	} else {
		item.Uncheck()
	}
}

// formatDuration renders seconds as "30s" or, for whole minutes, "2 min".
func formatDuration(sec float64) string {
	if s := int(sec); float64(s) == sec && s >= 60 && s%60 == 0 {
		return fmt.Sprintf("%d min", s/60)
	}
	return fmt.Sprintf("%.0fs", sec)
}

// openConfigFile opens config.json in the user's default editor.
func openConfigFile() {
	path, err := configPath()
	if err != nil {
		logf("settings: config path unavailable: %v", err)
		return
	}
	if _, err := os.Stat(path); err != nil {
		_ = saveConfig(getConfig())
	}
	openURL(path)
}

// openLogFile opens the log in the user's default text viewer.
func openLogFile() {
	if path := logFilePath(); path != "" {
		openURL(path)
	}
}

// notifyKilled is called from monitor.go after a process is terminated.
// It shows a desktop notification and updates the tray tooltip and status label.
func notifyKilled(name string, pid int, cpu float64) {
	// Desktop notification (non-blocking).
	notifyKilledToast(name, pid, cpu)

	if mStatus == nil {
		return
	}
	setTrayStatus(fmt.Sprintf("Killed: %s", name), true)
	systray.SetTooltip(fmt.Sprintf("Hotfix — Killed %s (PID %d, %.0f%% CPU)", name, pid, cpu))
	restoreTrayStatusAfter(3 * time.Second)
}

// flashTrayStatus shows a transient status label, then returns to normal.
func flashTrayStatus(label string) {
	setTrayStatus(label, false)
	restoreTrayStatusAfter(3 * time.Second)
}

// restoreTrayStatusAfter resets the status label and tooltip after d,
// replacing any pending reset.
func restoreTrayStatusAfter(d time.Duration) {
	trayMu.Lock()
	defer trayMu.Unlock()
	if statusTimeout != nil {
		statusTimeout.Stop()
	}
	statusTimeout = time.AfterFunc(d, func() {
		if mStatus == nil {
			return
		}
		if getConfig().Enabled {
			setTrayStatus("Watching", false)
		} else {
			setTrayStatus("Disabled", false)
		}
		systray.SetTooltip(defaultTooltip)
	})
}

// setTrayStatus updates the disabled status label at the top of the menu.
// nil-guarded so it is safe to call before the tray menu is built.
func setTrayStatus(label string, alert bool) {
	if mStatus == nil {
		return
	}
	prefix := "Hotfix — "
	if alert {
		prefix = "🔥 Hotfix — "
	}
	mStatus.SetTitle(prefix + label)
}
