import SwiftData
import SwiftUI

struct RootView: View {
    @Environment(AppState.self) private var appState
    @Environment(CheckCoordinator.self) private var coordinator
    @Environment(\.modelContext) private var context

    var body: some View {
        @Bindable var appState = appState
        TabView(selection: $appState.tab) {
            Tab("Dictate", systemImage: "waveform", value: AppTab.dictate) {
                DictateView()
            }
            Tab("Ideas", systemImage: "square.stack.3d.up.fill", value: AppTab.ideas) {
                IdeasView()
            }
            Tab("Lab", systemImage: "flask.fill", value: AppTab.lab) {
                LabView()
            }
            Tab("Settings", systemImage: "gearshape.fill", value: AppTab.settings) {
                SettingsView()
            }
        }
        .tint(Color.sjSpark)
        .paywallSheet()
        .sheet(item: $appState.reviewing) { idea in
            ReviewView(idea: idea) { checkNow in
                finishReview(idea, check: checkNow)
            }
            .interactiveDismissDisabled(false)
        }
    }

    /// Leaving review always keeps the idea (it was saved when the take ended).
    /// It is checked when the person pressed Check, or on the way out when
    /// "Check automatically after review" is on.
    private func finishReview(_ idea: Idea, check: Bool) {
        // A "Type instead" draft left blank is not an idea; drop it.
        if idea.transcript.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
           idea.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, idea.fields.isEmpty {
            context.delete(idea)
            try? context.save()
            appState.reviewing = nil
            return
        }
        if idea.status == .draft { idea.status = .reviewed }
        idea.updatedAt = .now
        try? context.save()
        appState.reviewing = nil
        if check || (AppSettings.autoCheck && idea.resultData == nil) {
            coordinator.check(idea, in: context)
            appState.focus = idea.id
            appState.tab = .ideas
        }
    }
}
