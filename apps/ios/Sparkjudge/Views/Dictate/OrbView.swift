import SwiftUI

/// The dictate button: a Metal orb that breathes when idle and ripples with
/// the microphone level while listening.
struct OrbView: View {
    /// Smoothed mic level, 0…1.
    var level: Float
    var active: Bool
    /// 0…1: how close the silence cutoff is, drawn as a thin ring.
    var silenceProgress: Double = 0

    @State private var start = Date.now

    var body: some View {
        TimelineView(.animation) { context in
            let t = context.date.timeIntervalSince(start)
            // Idle, the orb still moves a little so it reads as alive.
            let shown = active ? level : Float(0.08 + 0.05 * sin(t * 1.4))
            let inner = active ? Color.sjSpark : Color.sjViolet
            let outer = active ? Color(red: 0.55, green: 0.12, blue: 0.45) : Color(red: 0.12, green: 0.08, blue: 0.35)
            let glow = active ? Color(red: 1.0, green: 0.62, blue: 0.3) : Color(red: 0.55, green: 0.45, blue: 1.0)
            Rectangle()
                .fill(.white)
                .visualEffect { content, proxy in
                    content.colorEffect(ShaderLibrary.sparkOrb(
                        .float2(proxy.size), .float(Float(t)), .float(shown),
                        .color(inner), .color(outer), .color(glow)
                    ))
                }
        }
        .overlay {
            Circle()
                .trim(from: 0, to: silenceProgress)
                .stroke(Color.sjSpark.opacity(0.8), style: StrokeStyle(lineWidth: 3, lineCap: .round))
                .rotationEffect(.degrees(-90))
                .padding(28)
                .opacity(active && silenceProgress > 0.05 ? 1 : 0)
                .animation(.easeOut(duration: 0.2), value: silenceProgress)
        }
        .scaleEffect(active ? 1.04 : 1)
        .animation(.spring(response: 0.5, dampingFraction: 0.6), value: active)
        .contentShape(Circle().inset(by: 40))
        .accessibilityElement()
        .accessibilityLabel(active ? "Listening. Tap to stop and save the idea." : "Dictate an idea")
        .accessibilityHint(active ? "The take also ends by itself when you stop talking." : "Starts listening; your words appear as you speak.")
        .accessibilityAddTraits(.isButton)
    }
}

#Preview {
    OrbView(level: 0.5, active: true, silenceProgress: 0.4)
        .frame(width: 320, height: 320)
        .background(Color.sjInk)
}
