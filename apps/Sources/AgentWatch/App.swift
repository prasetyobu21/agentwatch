import SwiftUI
import AppKit

@main
struct AgentWatchApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate
    
    @State private var testDuration: Double = 5.0
    @State private var testAgentCount: Int = 1

    var body: some Scene {
        MenuBarExtra("AgentWatch", systemImage: appDelegate.daemonClient.globalStatus == "Running" ? "bolt.fill" : "eye") {
            VStack(spacing: 12) {
                Text("Prototype Controls")
                    .font(.headline)
                
                Divider()
                
                VStack(alignment: .leading) {
                    Text("Duration: \(Int(testDuration)) seconds")
                    Slider(value: $testDuration, in: 1...30, step: 1)
                }
                
                HStack {
                    Text("Concurrent Agents: \(testAgentCount)")
                    Spacer()
                    Stepper("", value: $testAgentCount, in: 1...10)
                }
                
                Button("Trigger Test") {
                    appDelegate.triggerTest(agents: testAgentCount, duration: Int(testDuration))
                }
                .buttonStyle(.borderedProminent)
                .padding(.top, 4)
                
                Divider()
                
                Button("Quit") {
                    NSApplication.shared.terminate(nil)
                }
            }
            .padding()
            .frame(width: 250)
        }
        .menuBarExtraStyle(.window)
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    var notchWindow: NSPanel!
    var daemonClient = DaemonClient()

    func applicationDidFinishLaunching(_ notification: Notification) {
        setupNotchWindow()
    }
    
    func triggerTest(agents: Int, duration: Int) {
        for i in 1...agents {
            let sessionID = "test-agent-\(i)-\(UUID().uuidString.prefix(4))"
            let agentName = "Test Agent \(i)"
            
            sendMockEvent(sessionID: sessionID, agentName: agentName, status: "Running")
            
            DispatchQueue.main.asyncAfter(deadline: .now() + .seconds(duration)) {
                self.sendMockEvent(sessionID: sessionID, agentName: agentName, status: "Finished")
            }
        }
    }
    
    private func sendMockEvent(sessionID: String, agentName: String, status: String) {
        guard let url = URL(string: "http://127.0.0.1:8765/event") else { return }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let payload: [String: Any] = [
            "session_id": sessionID,
            "agent_name": agentName,
            "status": status,
            "message": status == "Finished" ? "Completed test" : "Test running..."
        ]
        request.httpBody = try? JSONSerialization.data(withJSONObject: payload)
        URLSession.shared.dataTask(with: request).resume()
    }

    func setupNotchWindow() {
        let screenWidth = NSScreen.main?.frame.width ?? 1440
        let screenHeight = NSScreen.main?.frame.height ?? 900
        
        let width: CGFloat = 800 // Very wide to allow dynamic SwiftUI sizing without clipping
        let height: CGFloat = 38 // Match the standard 38pt menu bar height on 16-inch Macs
        
        let rect = NSRect(x: (screenWidth - width) / 2, y: screenHeight - height, width: width, height: height)
        
        notchWindow = NSPanel(
            contentRect: rect,
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: false
        )
        
        notchWindow.isOpaque = false
        notchWindow.backgroundColor = .clear
        notchWindow.hasShadow = false
        notchWindow.level = .mainMenu + 2
        notchWindow.collectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle]
        notchWindow.ignoresMouseEvents = true
        
        let contentView = NotchView(daemonClient: daemonClient)
            .edgesIgnoringSafeArea(.all)
        
        let hostingView = NSHostingView(rootView: contentView)
        hostingView.wantsLayer = true
        hostingView.layer?.backgroundColor = NSColor.clear.cgColor
        
        notchWindow.contentView = hostingView
        notchWindow.makeKeyAndOrderFront(nil)
    }
}

struct NotchView: View {
    @ObservedObject var daemonClient: DaemonClient
    
    var activeCount: Int {
        daemonClient.sessions.values.filter { $0.status == "Running" || $0.status == "Waiting" }.count
    }
    
    var isExpanded: Bool {
        activeCount > 0
    }
    
    var body: some View {
        GeometryReader { geo in
            VStack {
                HStack(spacing: 0) {
                    // Left ear
                    HStack {
                        if isExpanded {
                            ProgressIcon(status: daemonClient.globalStatus)
                                .frame(width: 20, height: 20)
                                .padding(.leading, 24)
                                .transition(.opacity.animation(.easeIn(duration: 0.2).delay(0.1)))
                        }
                        Spacer(minLength: 0)
                    }
                    .frame(width: isExpanded ? 130 : 0)
                    .clipped()
                    
                    // The physical hardware notch gap (always dead center)
                    Color.clear
                        .frame(width: 220, height: 38)
                    
                    // Right ear
                    HStack {
                        Spacer(minLength: 0)
                        if isExpanded {
                            Text("\(activeCount) in progress")
                                .font(.system(size: 14, weight: .medium, design: .default))
                                .foregroundColor(.white)
                                .lineLimit(1)
                                .fixedSize(horizontal: true, vertical: false)
                                .padding(.trailing, 24)
                                .transition(.opacity.animation(.easeIn(duration: 0.2).delay(0.1)))
                        }
                    }
                    .frame(width: isExpanded ? 130 : 0)
                    .clipped()
                }
                .frame(height: 38)
                .background(Color.black)
                .clipShape(UnevenRoundedRectangle(
                    topLeadingRadius: 0,
                    bottomLeadingRadius: 24,
                    bottomTrailingRadius: 24,
                    topTrailingRadius: 0,
                    style: .continuous
                ))
                // Smooth animation for expansion
                .animation(.spring(response: 0.5, dampingFraction: 0.7, blendDuration: 0), value: isExpanded)
                
                Spacer()
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        }
    }
}

struct ProgressIcon: View {
    var status: String
    @State private var rotation: Double = 0
    
    var body: some View {
        ZStack {
            if status == "Running" {
                Circle()
                    .stroke(
                        AngularGradient(gradient: Gradient(colors: [.white.opacity(0.2), .white]), center: .center),
                        lineWidth: 2
                    )
                    .rotationEffect(.degrees(rotation))
                    .onAppear {
                        withAnimation(Animation.linear(duration: 1.5).repeatForever(autoreverses: false)) {
                            rotation = 360
                        }
                    }
                
                Circle()
                    .stroke(Color.white, lineWidth: 2)
                    .frame(width: 10, height: 10)
            } else if status == "Waiting" {
                Image(systemName: "hand.raised.fill")
                    .foregroundColor(.yellow)
            } else {
                Image(systemName: "eye")
                    .foregroundColor(.gray)
            }
        }
    }
}
