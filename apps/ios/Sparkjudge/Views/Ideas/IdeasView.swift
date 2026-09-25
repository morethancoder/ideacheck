import SwiftData
import SwiftUI

enum IdeasLayout: String, CaseIterable, Identifiable {
    case list, pile
    var id: String { rawValue }
    var title: String { self == .list ? "List" : "Piles" }
    var symbol: String { self == .list ? "rectangle.split.3x1" : "square.stack.3d.down.right" }
}

/// Every idea, sorted into its category. List view: a shelf of cards per
/// category. Pile view: each category a stack you open.
struct IdeasView: View {
    @Query(sort: \Idea.createdAt, order: .reverse) private var ideas: [Idea]
    @Environment(AppState.self) private var appState
    @AppStorage(SettingsKey.ideasLayout) private var layoutRaw = IdeasLayout.list.rawValue
    @State private var order: IdeaGrouping.Order = .newest
    @State private var path: [UUID] = []
    @State private var openPile: IdeaCategory?
    @State private var didApplyLaunch = false
    @Namespace private var piles

    private var layout: IdeasLayout { IdeasLayout(rawValue: layoutRaw) ?? .list }
    private var groups: [IdeaGroup] { IdeaGrouping.group(ideas.map(\.summary), order: order) }
    private var byID: [UUID: Idea] { Dictionary(ideas.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a }) }

    var body: some View {
        NavigationStack(path: $path) {
            ZStack {
                Color.sjInk.ignoresSafeArea()
                if ideas.isEmpty {
                    empty
                } else if layout == .list {
                    listLayout
                } else {
                    pileLayout
                }
            }
            .navigationTitle("Ideas")
            .toolbar { toolbar }
            .navigationDestination(for: UUID.self) { id in
                if let idea = byID[id] {
                    IdeaDetailView(idea: idea)
                } else {
                    ContentUnavailableView("This idea is gone", systemImage: "questionmark.folder")
                }
            }
        }
        .onAppear(perform: applyLaunch)
        .onChange(of: appState.focus) { _, id in
            guard let id else { return }
            openPile = nil
            path = [id]
            appState.focus = nil
        }
    }

    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            Menu {
                Picker("Order", selection: $order) {
                    Text("Newest first").tag(IdeaGrouping.Order.newest)
                    Text("Best rated first").tag(IdeaGrouping.Order.bestRated)
                }
            } label: {
                Image(systemName: "arrow.up.arrow.down")
            }
            .accessibilityLabel("Sort order")
        }
        ToolbarItem(placement: .topBarTrailing) {
            Picker("Layout", selection: Binding(get: { layout }, set: { l in withAnimation(.smooth) { layoutRaw = l.rawValue; openPile = nil } })) {
                ForEach(IdeasLayout.allCases) { l in
                    Image(systemName: l.symbol).accessibilityLabel(l.title).tag(l)
                }
            }
            .pickerStyle(.segmented)
            .frame(width: 110)
        }
    }

    private var empty: some View {
        ContentUnavailableView {
            Label("No ideas yet", systemImage: "sparkles")
        } description: {
            Text("Dictate one and it lands here, sorted by kind.")
        } actions: {
            Button("Dictate an idea") { appState.tab = .dictate }
                .buttonStyle(.borderedProminent)
                .tint(Color.sjSpark)
        }
    }

    // MARK: - List

    private var listLayout: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 30) {
                ForEach(groups) { group in
                    VStack(alignment: .leading, spacing: 12) {
                        SectionTitle(title: group.category.title, symbol: group.category.symbol, trailing: "\(group.ids.count)")
                            .padding(.horizontal, 20)
                        ScrollView(.horizontal, showsIndicators: false) {
                            LazyHStack(spacing: 14) {
                                ForEach(group.ids, id: \.self) { id in
                                    if let idea = byID[id] {
                                        NavigationLink(value: id) {
                                            IdeaCardView(idea: idea)
                                        }
                                        .buttonStyle(CardPressStyle())
                                    }
                                }
                            }
                            .scrollTargetLayout()
                            .padding(.vertical, 10)
                        }
                        .contentMargins(.horizontal, 20, for: .scrollContent)
                        .scrollTargetBehavior(.viewAligned)
                    }
                }
            }
            .padding(.vertical, 12)
        }
    }

    // MARK: - Piles

    private var pileLayout: some View {
        ZStack {
            ScrollView {
                LazyVGrid(columns: [GridItem(.flexible(), spacing: 18), GridItem(.flexible(), spacing: 18)], spacing: 28) {
                    ForEach(groups) { group in
                        let members = group.ids.compactMap { byID[$0] }
                        if openPile == group.category {
                            Color.clear.frame(height: PileView.height)
                        } else {
                            PileView(category: group.category, ideas: members)
                                .matchedGeometryEffect(id: group.category, in: piles)
                                .onTapGesture { open(group.category) }
                                .accessibilityAction { open(group.category) }
                        }
                    }
                }
                .padding(20)
            }
            .blur(radius: openPile == nil ? 0 : 6)
            .allowsHitTesting(openPile == nil)

            if let category = openPile, let group = groups.first(where: { $0.category == category }) {
                Color.black.opacity(0.35)
                    .ignoresSafeArea()
                    .onTapGesture { close() }
                    .transition(.opacity)
                OpenPileView(category: category, ideas: group.ids.compactMap { byID[$0] }, onClose: close)
                    .matchedGeometryEffect(id: category, in: piles)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .transition(.asymmetric(insertion: .identity, removal: .identity))
                    .zIndex(1)
            }
        }
        .sensoryFeedback(.impact(weight: .light), trigger: openPile)
    }

    private func open(_ category: IdeaCategory) {
        withAnimation(.spring(response: 0.5, dampingFraction: 0.82)) { openPile = category }
    }

    private func close() {
        withAnimation(.spring(response: 0.45, dampingFraction: 0.86)) { openPile = nil }
    }

    private func applyLaunch() {
        guard !didApplyLaunch else { return }
        didApplyLaunch = true
        let open = LaunchOptions.current.open ?? ""
        if open == "detail", let best = ideas.filter({ $0.composite != nil }).max(by: { ($0.composite ?? 0) < ($1.composite ?? 0) }) {
            path = [best.id]
        } else if open.hasPrefix("pile:"), let c = IdeaCategory(rawValue: String(open.dropFirst(5))) {
            layoutRaw = IdeasLayout.pile.rawValue
            openPile = c
        }
    }
}

/// A gentle press-down for cards.
struct CardPressStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .scaleEffect(configuration.isPressed ? 0.96 : 1)
            .animation(.spring(response: 0.3, dampingFraction: 0.6), value: configuration.isPressed)
    }
}
