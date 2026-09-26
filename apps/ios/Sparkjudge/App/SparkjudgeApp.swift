import SwiftData
import SwiftUI

@main
struct SparkjudgeApp: App {
    private let container: ModelContainer
    private let launch = LaunchOptions.current
    @State private var coordinator: CheckCoordinator
    @State private var entitlements: Entitlements
    @State private var judges: OnDeviceJudges
    @State private var appState: AppState

    init() {
        let launch = LaunchOptions.current
        let entitlements = Entitlements.live()
        let judges = OnDeviceJudges()
        let coordinator = CheckCoordinator()
        // Free users check on this iPhone unless they chose Cloud; Pro users
        // in Cloud unless they chose the phone (CheckRoute).
        coordinator.makeChecker = { kind in
            AppSettings.makeChecker(kind ?? AppSettings.checker(isPro: entitlements.isPro), onDevice: judges.checker)
        }
        _entitlements = State(initialValue: entitlements)
        _judges = State(initialValue: judges)
        _coordinator = State(initialValue: coordinator)
        container = Self.makeContainer(inMemory: launch.seed)
        let state = AppState(tab: launch.tab ?? .dictate)
        if launch.seed {
            let draft = SampleData.seed(into: container.mainContext)
            if launch.open == "review" { state.reviewing = draft }
        }
        _appState = State(initialValue: state)
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(coordinator)
                .environment(entitlements)
                .environment(judges)
                .environment(appState)
                .preferredColorScheme(launch.colorScheme)
        }
        .modelContainer(container)
    }

    /// The on-disk store; an in-memory one for demos, or if the disk store
    /// cannot open (so the app still starts and can capture ideas).
    private static func makeContainer(inMemory: Bool) -> ModelContainer {
        let schema = Schema([Idea.self])
        if !inMemory {
            // On a fresh install Application Support does not exist yet, and the
            // store cannot create it: make it first, then open the store in it.
            let dir = URL.applicationSupportDirectory
            try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let config = ModelConfiguration(schema: schema, url: dir.appending(path: "Sparkjudge.store"), cloudKitDatabase: .none)
            if let container = try? ModelContainer(for: schema, configurations: config) {
                return container
            }
        }
        return try! ModelContainer(for: schema, configurations: ModelConfiguration(schema: schema, isStoredInMemoryOnly: true, cloudKitDatabase: .none))
    }
}
