import Foundation
import SwiftUI
import ServiceManagement

class PreferencesManager: ObservableObject {
    static let shared = PreferencesManager()

    @AppStorage("isEnabled") var isEnabled: Bool = true
    @AppStorage("cpuThreshold") var cpuThreshold: Double = 80.0
    @AppStorage("killDuration") var killDuration: Double = 60.0
    @AppStorage("killOnSleep") var killOnSleep: Bool = true
    @AppStorage("protectActiveApp") var protectActiveApp: Bool = true

    private let whitelistKey = "processWhitelist"

    private let defaultWhitelist: [String] = [
        "Xcode", "swift", "clang", "swiftc", "node", "python3"
    ]

    @Published var whitelist: [String] = []

    /// Whether Hotfix is registered as a macOS login item. Backed by
    /// `SMAppService` (the real source of truth), mirrored here so SwiftUI can
    /// bind to it. Never assigned directly by the UI — go through
    /// `setLaunchAtLogin(_:)`.
    @Published var launchAtLogin: Bool = false

    private init() {
        loadWhitelist()
        launchAtLogin = (SMAppService.mainApp.status == .enabled)
    }

    // MARK: - Launch at login
    /// Register or unregister Hotfix as a login item (macOS 13+, no helper
    /// bundle needed). `launchAtLogin` is always re-synced from the service's
    /// actual status afterward so the toggle can never lie about the real state.
    func setLaunchAtLogin(_ enabled: Bool) {
        do {
            if enabled {
                if SMAppService.mainApp.status != .enabled {
                    try SMAppService.mainApp.register()
                }
            } else {
                if SMAppService.mainApp.status == .enabled {
                    try SMAppService.mainApp.unregister()
                }
            }
            logf("launch at login \(enabled ? "enabled" : "disabled")")
        } catch {
            logf("launch at login toggle failed: \(error.localizedDescription)")
        }
        launchAtLogin = (SMAppService.mainApp.status == .enabled)
    }

    // MARK: - Whitelist persistence
    private func loadWhitelist() {
        if let data = UserDefaults.standard.data(forKey: whitelistKey),
           let decoded = try? JSONDecoder().decode([String].self, from: data) {
            whitelist = decoded
        } else {
            // First run — set defaults
            whitelist = defaultWhitelist
            saveWhitelist()
        }
    }

    private func saveWhitelist() {
        if let encoded = try? JSONEncoder().encode(whitelist) {
            UserDefaults.standard.set(encoded, forKey: whitelistKey)
        }
    }

    func addToWhitelist(_ name: String) {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !whitelist.contains(trimmed) else { return }
        whitelist.append(trimmed)
        saveWhitelist()
    }

    func removeFromWhitelist(_ name: String) {
        whitelist.removeAll { $0 == name }
        saveWhitelist()
    }

    func isWhitelisted(_ name: String) -> Bool {
        whitelist.contains(name)
    }
}
