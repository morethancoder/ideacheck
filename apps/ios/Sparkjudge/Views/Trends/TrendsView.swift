import SwiftData
import SwiftUI

/// Trends & inspiration: what launched or was talked about this week, sorted
/// into the same kinds of idea as your own, with a prompt for each kind.
/// "Start an idea" opens Review with a draft that remembers where it came from.
struct TrendsView: View {
    @State private var store = TrendsStore.live()
    @Environment(AppState.self) private var appState
    @Environment(\.modelContext) private var context

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 28, pinnedViews: []) {
                status
                if let report = store.report {
                    ForEach(report.groups) { group in
                        section(group)
                    }
                } else if !store.loading {
                    ContentUnavailableView("No trends yet", systemImage: "chart.line.uptrend.xyaxis",
                                           description: Text(store.note ?? "Pull to load this week's trends."))
                }
            }
            .padding(20)
        }
        .background(Color.sjInk)
        .navigationTitle("Trends")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await store.refresh() }
        .task { await store.refresh() }
    }

    private var status: some View {
        HStack(spacing: 8) {
            if store.loading {
                ProgressView().controlSize(.small)
                Text("Fetching this week's trends…")
            } else if let date = store.report?.fetchedDate {
                Image(systemName: store.note == nil ? "clock" : "wifi.slash")
                Text("Fetched ") + Text(date, format: .relative(presentation: .named))
            }
            Spacer()
        }
        .font(.sjLabel(.footnote))
        .foregroundStyle(Color.sjMuted)
        .overlay(alignment: .bottomLeading) {
            if let note = store.note, store.report != nil {
                Text(note).font(.footnote).foregroundStyle(Verdict.park.color).offset(y: 22)
            }
        }
        .padding(.bottom, store.note == nil ? 0 : 18)
    }

    private func section(_ group: TrendGroup) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            SectionTitle(title: group.category.title, symbol: group.category.symbol, trailing: "\(group.items.count)")
            if let spark = group.spark {
                sparkCard(spark, category: group.category)
            }
            ForEach(group.items) { item in
                row(item)
            }
        }
    }

    private func sparkCard(_ spark: TrendSpark, category: IdeaCategory) -> some View {
        Button { start(transcript: spark.prompt + " ", category: category, origin: nil) } label: {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: "sparkles").foregroundStyle(Color.sjSpark).font(.title3)
                VStack(alignment: .leading, spacing: 6) {
                    Text(spark.prompt).font(.system(.body, design: .serif)).foregroundStyle(Color.sjText)
                        .multilineTextAlignment(.leading)
                    Text("Answer this").font(.sjLabel(.caption)).textCase(.uppercase).foregroundStyle(Color.sjSpark)
                }
                Spacer(minLength: 0)
            }
            .padding(16)
            .background(
                LinearGradient(colors: [Color.sjViolet.opacity(0.22), Color.sjSpark.opacity(0.12)], startPoint: .topLeading, endPoint: .bottomTrailing),
                in: .rect(cornerRadius: 18, style: .continuous)
            )
        }
        .buttonStyle(CardPressStyle())
        .accessibilityHint("Starts a new idea from this prompt")
    }

    private func row(_ item: TrendItem) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            if let url = URL(string: item.url) {
                Link(destination: url) {
                    Text(item.title).font(.headline).foregroundStyle(Color.sjText).multilineTextAlignment(.leading)
                }
            } else {
                Text(item.title).font(.headline)
            }
            if let summary = item.summary, !summary.isEmpty {
                Text(summary).font(.subheadline).foregroundStyle(Color.sjMuted).lineLimit(2)
            }
            HStack(spacing: 10) {
                Badge(text: item.source, tint: Color.sjMuted)
                if let points = item.points, points > 0 {
                    Label("\(points)", systemImage: item.source == "GitHub" ? "star.fill" : "arrow.up")
                        .font(.sjLabel(.caption2)).foregroundStyle(Color.sjMuted)
                }
                Spacer()
                Button {
                    start(transcript: "Inspired by “\(item.title)”: ", category: item.category, origin: .trend(item))
                } label: {
                    Label("Start an idea", systemImage: "plus.circle.fill").font(.subheadline.weight(.semibold))
                }
                .buttonStyle(.borderless)
                .tint(Color.sjSpark)
            }
        }
        .padding(14)
        .background(Color.sjSurface, in: .rect(cornerRadius: 16, style: .continuous))
    }

    /// A new draft, saved at once like a dictated one, opened in Review.
    private func start(transcript: String, category: IdeaCategory, origin: IdeaOrigin?) {
        let idea = Idea(transcript: transcript, category: category)
        idea.categoryChosen = category != .other
        idea.suggested = true // nothing of theirs to read yet
        idea.origin = origin
        context.insert(idea)
        try? context.save()
        appState.reviewing = idea
    }
}
