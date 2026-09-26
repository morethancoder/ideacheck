import SwiftData
import SwiftUI

/// Everything a check said about one idea: verdict and why, every dimension
/// with its score and confidence, what is missing, strengths and risks,
/// research findings with links, and a re-check.
struct IdeaDetailView: View {
    @Bindable var idea: Idea
    @Environment(CheckCoordinator.self) private var coordinator
    @Environment(AppState.self) private var appState
    @Environment(\.modelContext) private var context
    @Environment(\.dismiss) private var dismiss
    @State private var confirmDelete = false
    @State private var sharing = false

    private var result: CheckResult? { idea.result }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 28) {
                IdeaCardView(idea: idea, size: .hero, animated: true)
                    .padding(.top, 4)
                actions
                if let origin = idea.origin {
                    OriginLine(origin: origin)
                }
                if let run = coordinator.runs[idea.id] {
                    progress(run)
                }
                if let error = idea.lastError, !coordinator.isChecking(idea) {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .font(.callout)
                        .foregroundStyle(Verdict.park.color)
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Color.sjSurface, in: .rect(cornerRadius: 16))
                }
                if let result {
                    verdict(result)
                    if let summary = result.summary, !summary.isEmpty {
                        block("Summary", symbol: "text.quote") {
                            Text(summary).font(.system(.body, design: .serif)).lineSpacing(3)
                        }
                    }
                    contributions(result)
                    dimensions(result)
                    missing(result)
                    research(result)
                    about(result)
                } else if !coordinator.isChecking(idea) {
                    ContentUnavailableView("Not checked yet", systemImage: "sparkles",
                                           description: Text("Check it to get a verdict, a score for each dimension and what's missing."))
                }
                transcript
            }
            .padding(20)
        }
        .background(Color.sjInk)
        .navigationTitle(idea.displayTitle)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Button("Edit details", systemImage: "pencil") { appState.reviewing = idea }
                    Button("Share as an image", systemImage: "square.and.arrow.up") { sharing = true }
                    Button("New card style", systemImage: "dice") {
                        var rng = SplitMix64(seed: idea.seed)
                        withAnimation(.smooth) { idea.styleSeed = Int64(bitPattern: rng.next()) }
                        try? context.save()
                    }
                    Divider()
                    Button("Delete idea", systemImage: "trash", role: .destructive) { confirmDelete = true }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
                .accessibilityLabel("More")
            }
        }
        .sheet(isPresented: $sharing) {
            NavigationStack {
                ShareView(initial: [idea.id])
                    .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { sharing = false } } }
            }
        }
        .confirmationDialog("Delete this idea?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                coordinator.cancel(idea)
                context.delete(idea)
                try? context.save()
                dismiss()
            }
        } message: {
            Text("It will be gone from every device.")
        }
    }

    // MARK: - Sections

    private var actions: some View {
        HStack(spacing: 12) {
            Button {
                coordinator.check(idea, in: context)
            } label: {
                Label(idea.resultData == nil ? "Check" : "Re-check", systemImage: "arrow.clockwise")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .tint(Color.sjSpark)
            .foregroundStyle(.black)
            .disabled(coordinator.isChecking(idea))
            Button {
                appState.reviewing = idea
            } label: {
                Label("Edit", systemImage: "pencil").frame(maxWidth: .infinity)
            }
            .buttonStyle(.bordered)
            .tint(Color.sjText)
        }
        .controlSize(.large)
        .font(.headline)
    }

    private func progress(_ run: CheckRun) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(run.label)
                    .font(.sjLabel(.footnote))
                    .contentTransition(.numericText())
                Spacer()
                Button("Stop") { coordinator.cancel(idea) }
                    .font(.sjLabel(.footnote))
                    .tint(Color.sjMuted)
            }
            ProgressView(value: run.fraction).tint(Color.sjSpark)
            if let q = run.question {
                Text(FieldSpec.humanize(q)).font(.caption).foregroundStyle(Color.sjMuted).contentTransition(.opacity)
            }
        }
        .padding(16)
        .background(Color.sjSurface, in: .rect(cornerRadius: 18))
        .animation(.smooth, value: run)
        .accessibilityElement(children: .combine)
    }

    private func verdict(_ r: CheckResult) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if let v = r.verdictValue {
                HStack(alignment: .firstTextBaseline) {
                    Text(v.word)
                        .font(.system(.largeTitle, design: .serif, weight: .black))
                        .foregroundStyle(v.color)
                    Text(v.blurb).font(.headline).foregroundStyle(Color.sjMuted)
                }
            }
            if let reason = r.verdictReason {
                Text(reason).font(.callout).foregroundStyle(Color.sjText)
            }
            HStack(spacing: 16) {
                stat("Rating", String(format: "%.1f/10", r.rating))
                stat("Confidence", r.compositeConfidence.formatted(.percent.precision(.fractionLength(0))))
                if let t = r.ideaType { stat("Kind", IdeaCategory(routerChoice: t.choice).badge) }
            }
            if r.partial == true {
                Label("Scored on what is known; some facts were missing.", systemImage: "circle.lefthalf.filled")
                    .font(.caption).foregroundStyle(Color.sjMuted)
            }
        }
        .accessibilityElement(children: .combine)
    }

    private func stat(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label).font(.sjLabel(.caption2)).textCase(.uppercase).foregroundStyle(Color.sjMuted)
            Text(value).font(.system(.headline, design: .rounded)).monospacedDigit()
        }
    }

    @ViewBuilder private func contributions(_ r: CheckResult) -> some View {
        if !r.topStrengths.isEmpty || !r.topRisks.isEmpty {
            HStack(alignment: .top, spacing: 12) {
                contributionList("Strengths", symbol: "arrow.up.right", items: r.topStrengths, tint: Verdict.build.color)
                contributionList("Risks", symbol: "arrow.down.right", items: r.topRisks, tint: Verdict.kill.color)
            }
        }
    }

    private func contributionList(_ title: String, symbol: String, items: [CheckResult.Contribution], tint: Color) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(title, systemImage: symbol).font(.headline).foregroundStyle(tint)
            ForEach(items) { item in
                HStack {
                    Text(FieldSpec.humanize(item.id)).font(.subheadline).lineLimit(2)
                    Spacer(minLength: 4)
                    Text(item.value * 10, format: .number.precision(.fractionLength(1)))
                        .font(.system(.subheadline, design: .rounded, weight: .bold)).monospacedDigit()
                }
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .topLeading)
        .background(Color.sjSurface, in: .rect(cornerRadius: 18))
    }

    private func dimensions(_ r: CheckResult) -> some View {
        block("Dimensions", symbol: "slider.horizontal.3", trailing: "\(r.dimensions.count)") {
            VStack(spacing: 14) {
                ForEach(r.dimensions) { d in DimensionRow(dimension: d) }
            }
        }
    }

    @ViewBuilder private func missing(_ r: CheckResult) -> some View {
        if !r.missing.isEmpty {
            block("Missing", symbol: "questionmark.bubble", trailing: "\(r.missing.count)") {
                VStack(alignment: .leading, spacing: 12) {
                    ForEach(r.missing) { m in
                        VStack(alignment: .leading, spacing: 4) {
                            Text(m.ask).font(.body)
                            Text("Answer it in \(FieldSpec.humanize(m.fills).lowercased())").font(.caption).foregroundStyle(Color.sjMuted)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    Button("Add the missing details") { appState.reviewing = idea }
                        .font(.subheadline.weight(.semibold))
                        .tint(Color.sjSpark)
                }
            }
        }
    }

    @ViewBuilder private func research(_ r: CheckResult) -> some View {
        if let report = r.research, !report.findings.isEmpty {
            block("Research", symbol: "globe", trailing: report.cached == true ? "cached" : nil) {
                VStack(alignment: .leading, spacing: 16) {
                    ForEach(Array(report.findings.enumerated()), id: \.offset) { _, f in
                        VStack(alignment: .leading, spacing: 4) {
                            HStack(spacing: 6) {
                                Badge(text: FieldSpec.humanize(f.topic), tint: Color.sjViolet)
                                if let rel = f.relation { Badge(text: rel, tint: Color.sjMuted) }
                            }
                            if let url = URL(string: f.url) {
                                Link(destination: url) {
                                    Label(f.title, systemImage: "arrow.up.right.square")
                                        .font(.headline)
                                        .multilineTextAlignment(.leading)
                                }
                                .tint(Color.sjSpark)
                            } else {
                                Text(f.title).font(.headline)
                            }
                            Text(f.summary).font(.callout).foregroundStyle(Color.sjMuted)
                        }
                    }
                }
            }
        }
    }

    private func about(_ r: CheckResult) -> some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: 6) {
                meta("Judge", "\(r.backend) · \(r.model)")
                if let w = r.writer { meta("Writer", w) }
                if let rubric = r.rubric { meta("Rubric", rubric.name) }
                meta("Took", Duration.milliseconds(r.timing.totalMs).formatted(.units(allowed: [.seconds, .milliseconds], width: .abbreviated)))
                meta("Cost", r.cost.usd.formatted(.currency(code: "USD").precision(.fractionLength(4))))
                if let checked = idea.checkedAt { meta("Checked", checked.formatted(date: .abbreviated, time: .shortened)) }
                ForEach(r.warnings ?? [], id: \.self) { w in
                    Label(w, systemImage: "exclamationmark.circle").font(.caption).foregroundStyle(Color.sjMuted)
                }
            }
            .padding(.top, 8)
        } label: {
            Label("About this check", systemImage: "info.circle").font(.headline)
        }
        .tint(Color.sjText)
    }

    private func meta(_ k: String, _ v: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(k).font(.sjLabel(.caption)).foregroundStyle(Color.sjMuted).frame(width: 70, alignment: .leading)
            Text(v).font(.caption)
        }
    }

    private var transcript: some View {
        DisclosureGroup {
            Text(idea.transcript.isEmpty ? "—" : idea.transcript)
                .font(.system(.body, design: .serif))
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.top, 8)
                .textSelection(.enabled)
        } label: {
            Label("What you said", systemImage: "quote.opening").font(.headline)
        }
        .tint(Color.sjText)
    }

    private func block<Content: View>(_ title: String, symbol: String, trailing: String? = nil, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            SectionTitle(title: title, symbol: symbol, trailing: trailing)
            content()
        }
    }
}

/// One scored question: name, a bar colored by good news, the score out of 10
/// and how confident the judge was.
struct DimensionRow: View {
    let dimension: CheckResult.Dimension

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Text(FieldSpec.humanize(dimension.id)).font(.subheadline.weight(.semibold))
                if dimension.polarity < 0 {
                    Text("lower is better").font(.caption2).foregroundStyle(Color.sjMuted)
                }
                Spacer()
                if let g = dimension.goodness {
                    Text(g * 10, format: .number.precision(.fractionLength(1)))
                        .font(.system(.subheadline, design: .rounded, weight: .bold))
                        .monospacedDigit()
                } else {
                    Text("not scored").font(.caption).foregroundStyle(Color.sjMuted)
                }
            }
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.sjRaised)
                    if let g = dimension.goodness {
                        Capsule().fill(goodnessColor(g)).frame(width: max(6, geo.size.width * g))
                    }
                }
            }
            .frame(height: 8)
            HStack(spacing: 6) {
                Text("confidence").font(.sjLabel(.caption2)).foregroundStyle(Color.sjMuted)
                ConfidenceDots(value: dimension.confidence)
                Spacer()
                Text("weight \(dimension.weight.formatted(.number.precision(.fractionLength(0...1))))")
                    .font(.sjLabel(.caption2)).foregroundStyle(Color.sjMuted)
            }
            if let error = dimension.error {
                Text(error).font(.caption).foregroundStyle(Verdict.park.color)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibility)
    }

    private var accessibility: String {
        let name = FieldSpec.humanize(dimension.id)
        guard let g = dimension.goodness else { return "\(name), not scored. \(dimension.error ?? "")" }
        return String(format: "%@, %.1f out of 10, confidence %d percent", name, g * 10, Int(dimension.confidence * 100))
    }
}

struct ConfidenceDots: View {
    var value: Double

    var body: some View {
        HStack(spacing: 3) {
            ForEach(0..<5) { i in
                Circle()
                    .fill(Double(i) < (value * 5).rounded() ? Color.sjText : Color.sjRaised)
                    .frame(width: 6, height: 6)
            }
        }
    }
}
