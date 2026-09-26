import SwiftUI

/// A grid of small cards to pick from, numbered in the order they were picked.
/// Past `limit`, a new pick replaces the oldest.
struct IdeaPicker: View {
    let ideas: [Idea]
    @Binding var selection: [UUID]
    var limit: Int

    var body: some View {
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 132), spacing: 10)], spacing: 10) {
            ForEach(ideas) { idea in
                let index = selection.firstIndex(of: idea.id)
                Button { toggle(idea.id) } label: {
                    IdeaCardView(idea: idea, size: .small)
                        .overlay(alignment: .topTrailing) {
                            if let index {
                                Text("\(index + 1)")
                                    .font(.system(.subheadline, design: .rounded, weight: .heavy))
                                    .foregroundStyle(.black)
                                    .frame(width: 28, height: 28)
                                    .background(Color.sjSpark, in: Circle())
                                    .padding(6)
                            }
                        }
                        .overlay {
                            RoundedRectangle(cornerRadius: 18, style: .continuous)
                                .strokeBorder(index == nil ? Color.clear : Color.sjSpark, lineWidth: 3)
                        }
                        .opacity(index == nil && selection.count >= limit ? 0.6 : 1)
                }
                .buttonStyle(CardPressStyle())
                .accessibilityLabel(idea.displayTitle)
                .accessibilityValue(index.map { "picked \($0 + 1)" } ?? "")
                .accessibilityAddTraits(index == nil ? [] : .isSelected)
            }
        }
    }

    private func toggle(_ id: UUID) {
        withAnimation(.snappy) {
            if let i = selection.firstIndex(of: id) {
                selection.remove(at: i)
            } else {
                if selection.count >= limit { selection.removeFirst() }
                selection.append(id)
            }
        }
    }
}
