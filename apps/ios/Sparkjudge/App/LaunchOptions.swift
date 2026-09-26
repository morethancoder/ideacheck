import Foundation
import SwiftUI

/// Launch arguments for demos, screenshots and UI tests. They arrive through
/// UserDefaults' argument domain, e.g. `-sjSeed YES -sjTab ideas`. Any setting
/// key works the same way (`-checker preview`, `-ideasLayout pile`).
struct LaunchOptions: Sendable {
    /// Use an in-memory store filled with sample ideas.
    var seed: Bool
    var tab: AppTab?
    /// `review` (the sample draft), `detail` (the best-rated idea), `check` (the
    /// newest unchecked idea, checked at once), `pile:<category>`, `judge`
    /// (Settings → On this iPhone, with `-sjTab settings`),
    /// `lab:share|mixer|trends` (a Lab screen), `lab:mix` (mix the two best ideas, then review).
    var open: String?
    /// Replace the microphone with a scripted voice.
    var demoDictation: Bool
    /// Start a take as soon as the dictate screen appears.
    var autoDictate: Bool
    var colorScheme: ColorScheme?
    /// Write each rendered share image to Documents (screenshots).
    var exportShare = false

    static let current: LaunchOptions = {
        let d = UserDefaults.standard
        return LaunchOptions(
            seed: d.bool(forKey: "sjSeed"),
            tab: d.string(forKey: "sjTab").flatMap(AppTab.init(rawValue:)),
            open: d.string(forKey: "sjOpen"),
            demoDictation: d.bool(forKey: "sjDemoDictation"),
            autoDictate: d.bool(forKey: "sjAutoDictate"),
            colorScheme: d.string(forKey: "sjScheme").flatMap { $0 == "light" ? .light : ($0 == "dark" ? .dark : nil) },
            exportShare: d.bool(forKey: "sjExportShare")
        )
    }()
}

enum AppTab: String, Hashable, Sendable {
    case dictate, ideas, lab, settings
}

/// App-wide navigation state shared by the tabs.
@MainActor
@Observable
final class AppState {
    var tab: AppTab
    /// The idea being reviewed, presented over any tab.
    var reviewing: Idea?
    /// An idea the Ideas tab should open (after a check started from review).
    var focus: UUID?

    init(tab: AppTab = .dictate) {
        self.tab = tab
    }
}
