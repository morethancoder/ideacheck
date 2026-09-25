import SwiftUI

/// Where an idea came from, in Review and Detail: the ideas it was mixed from
/// (each opens that idea) or the trend it started from (opens the link).
struct OriginLine: View {
    let origin: IdeaOrigin
    @Environment(AppState.self) private var appState

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label(origin.kind == .mixed ? "Mixed from" : "Started from", systemImage: origin.symbol)
                .font(.sjLabel(.caption))
                .textCase(.uppercase)
                .foregroundStyle(Color.sjViolet)
            switch origin.kind {
            case .mixed:
                FlowRow(origin.parents) { parent in
                    Button { open(parent.id) } label: {
                        Text(parent.title)
                            .font(.subheadline.weight(.semibold))
                            .padding(.horizontal, 12)
                            .padding(.vertical, 7)
                            .background(Color.sjRaised, in: Capsule())
                    }
                    .buttonStyle(.plain)
                    .accessibilityHint("Opens this idea")
                }
            case .trend:
                if let raw = origin.url, let url = URL(string: raw) {
                    Link(destination: url) {
                        Label(origin.title ?? raw, systemImage: "arrow.up.right.square").font(.subheadline.weight(.semibold))
                    }
                    .tint(Color.sjText)
                } else {
                    Text(origin.title ?? "").font(.subheadline.weight(.semibold))
                }
            }
            if let detail = origin.writer ?? origin.source {
                Text(detail).font(.caption).foregroundStyle(Color.sjMuted)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.sjViolet.opacity(0.1), in: .rect(cornerRadius: 16, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityLabel(origin.line)
    }

    private func open(_ id: UUID) {
        appState.reviewing = nil
        appState.tab = .ideas
        appState.focus = id
    }
}

/// Wraps its items onto as many lines as they need.
struct FlowRow<Item: Identifiable, Content: View>: View {
    var items: [Item]
    var content: (Item) -> Content

    init(_ items: [Item], @ViewBuilder content: @escaping (Item) -> Content) {
        self.items = items
        self.content = content
    }

    var body: some View {
        FlowLayout(spacing: 8) {
            ForEach(items) { content($0) }
        }
    }
}

struct FlowLayout: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(width: proposal.width ?? .infinity, subviews: subviews)
        return CGSize(width: proposal.width ?? rows.map(\.width).max() ?? 0, height: rows.last.map { $0.y + $0.height } ?? 0)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        for row in arrange(width: bounds.width, subviews: subviews) {
            var x = bounds.minX
            for i in row.indices {
                let size = subviews[i].sizeThatFits(.unspecified)
                subviews[i].place(at: CGPoint(x: x, y: bounds.minY + row.y), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
        }
    }

    private struct Row {
        var indices: [Int] = []
        var y: CGFloat = 0
        var width: CGFloat = 0
        var height: CGFloat = 0
    }

    private func arrange(width: CGFloat, subviews: Subviews) -> [Row] {
        var rows = [Row()]
        for i in subviews.indices {
            let size = subviews[i].sizeThatFits(.unspecified)
            if !rows[rows.count - 1].indices.isEmpty, rows[rows.count - 1].width + spacing + size.width > width {
                let last = rows[rows.count - 1]
                rows.append(Row(y: last.y + last.height + spacing))
            }
            var row = rows[rows.count - 1]
            row.width += (row.indices.isEmpty ? 0 : spacing) + size.width
            row.height = max(row.height, size.height)
            row.indices.append(i)
            rows[rows.count - 1] = row
        }
        return rows
    }
}
