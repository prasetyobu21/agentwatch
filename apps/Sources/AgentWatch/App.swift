import SwiftUI
import AppKit

@main
struct AgentWatchApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate
    
    @State private var testDuration: Double = 5.0
    @State private var testAgentCount: Int = 1

    var body: some Scene {
        MenuBarExtra("AgentWatch", systemImage: (appDelegate.daemonClient.globalStatus == "Running" || appDelegate.daemonClient.globalStatus == "Initializing") ? "bolt.fill" : "eye") {
            VStack(spacing: 12) {
                Text("Active Agents")
                    .font(.headline)
                
                Divider()
                
                let activeSessions = Array(appDelegate.daemonClient.sessions.values).filter { !["idle", "completed", "failed", "orphaned"].contains($0.state) }
                if activeSessions.isEmpty {
                    Text("No active agents (Idle)")
                        .foregroundColor(.gray)
                        .font(.subheadline)
                        .padding(.vertical, 8)
                } else {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(activeSessions, id: \.sessionID) { session in
                            HStack {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(session.agentName)
                                        .font(.system(size: 13, weight: .semibold))
                                    if let msg = session.message, !msg.isEmpty {
                                        Text(msg)
                                            .font(.system(size: 11))
                                            .foregroundColor(.gray)
                                    }
                                }
                                Spacer()
                                
                                Text(session.state.replacingOccurrences(of: "_", with: " ").capitalized)
                                    .font(.system(size: 10, weight: .bold))
                                    .padding(.horizontal, 6)
                                    .padding(.vertical, 2)
                                    .background(statusColor(session.state).opacity(0.15))
                                    .foregroundColor(statusColor(session.state))
                                    .cornerRadius(4)
                            }
                        }
                    }
                    .padding(.vertical, 4)
                }
                
                Divider()
                
                Text("Prototype Controls")
                    .font(.subheadline)
                    .foregroundColor(.gray)
                
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
                .buttonStyle(.bordered)
                .padding(.top, 4)
                
                Divider()
                
                Button("Quit") {
                    NSApplication.shared.terminate(nil)
                }
            }
            .padding()
            .frame(width: 280)
        }
        .menuBarExtraStyle(.window)
    }

    func statusColor(_ state: String) -> Color {
        switch state {
        case "starting": return .blue
        case "running", "executing_tool": return .white
        case "permission_required", "permission_resolving": return .orange
        case "input_required": return .yellow
        case "completed": return .green
        case "failed": return .red
        case "orphaned": return .gray
        default: return .primary
        }
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    var notchWindow: NSPanel!
    var daemonClient = DaemonClient()
    private var daemonProcess: Process? = nil

    func applicationDidFinishLaunching(_ notification: Notification) {
        startDaemonIfNeeded()
        setupNotchWindow()
    }
    
    func applicationWillTerminate(_ notification: Notification) {
        daemonProcess?.terminate()
    }
    
    private func startDaemonIfNeeded() {
        let fm = FileManager.default
        let exeURL = Bundle.main.executableURL ?? URL(fileURLWithPath: CommandLine.arguments[0])
        let exeDir = exeURL.deletingLastPathComponent().path
        
        let paths = [
            exeDir + "/agentwatchd",
            exeDir + "/../../../bin/agentwatchd",
            "/usr/local/bin/agentwatchd"
        ]
        
        var daemonBinaryPath: String? = nil
        for path in paths {
            let standardPath = (path as NSString).standardizingPath
            if fm.fileExists(atPath: standardPath) {
                daemonBinaryPath = standardPath
                break
            }
        }
        
        guard let daemonPath = daemonBinaryPath else {
            print("Could not find agentwatchd binary")
            return
        }
        
        let process = Process()
        process.executableURL = URL(fileURLWithPath: daemonPath)
        process.standardOutput = nil
        process.standardError = nil
        
        do {
            try process.run()
            self.daemonProcess = process
            print("Started daemon from \(daemonPath)")
        } catch {
            print("Failed to start daemon: \(error)")
        }
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
    
    @State private var previousSessions: [String: AgentSession] = [:]
    @State private var showingDone: Bool = false
    @State private var doneCount: Int = 0
    @State private var doneTimer: Timer? = nil
    
    var activeCount: Int {
        daemonClient.sessions.values.filter {
            ["starting", "running", "executing_tool", "permission_resolving", "permission_required", "input_required"].contains($0.state)
        }.count
    }

    var permissionCount: Int {
        daemonClient.sessions.values.filter { $0.state == "permission_required" }.count
    }

    var inputCount: Int {
        daemonClient.sessions.values.filter { $0.state == "input_required" }.count
    }

    var attentionText: String? {
        if permissionCount > 0 {
            return "Asking for permission (\(permissionCount))"
        }
        if inputCount > 0 {
            return "Asking for input (\(inputCount))"
        }
        return nil
    }
    
    var isExpanded: Bool {
        activeCount > 0 || showingDone
    }
    
    var earWidth: CGFloat {
        if attentionText != nil {
            return 170
        } else if showingDone {
            return 130
        } else if activeCount > 0 {
            return 60
        } else {
            return 0
        }
    }
    
    var body: some View {
        GeometryReader { geo in
            VStack {
                HStack(spacing: 0) {
                    // Left ear
                    HStack {
                        if isExpanded {
                            if activeCount > 0 {
                                ProgressIcon(status: daemonClient.globalStatus)
                                    .frame(width: 20, height: 20)
                                    .padding(.leading, 24)
                                    .transition(.opacity.animation(.easeIn(duration: 0.2).delay(0.1)))
                            } else if showingDone {
                                Image(systemName: "checkmark.circle.fill")
                                    .foregroundColor(.green)
                                    .frame(width: 20, height: 20)
                                    .padding(.leading, 24)
                                    .transition(.opacity.animation(.easeIn(duration: 0.2).delay(0.1)))
                            }
                        }
                        Spacer(minLength: 0)
                    }
                    .frame(width: earWidth)
                    .clipped()
                    
                    // The physical hardware notch gap (always dead center)
                    Color.clear
                        .frame(width: 220, height: 38)
                    
                    // Right ear
                    HStack {
                        Spacer(minLength: 0)
                        if isExpanded {
                            if let attentionText {
                                Text(attentionText)
                                    .font(.system(size: 14, weight: .semibold, design: .default))
                                    .foregroundColor(permissionCount > 0 ? .yellow : .orange)
                                    .lineLimit(1)
                                    .fixedSize(horizontal: true, vertical: false)
                                    .padding(.trailing, 24)
                                    .transition(.opacity.animation(.easeIn(duration: 0.2).delay(0.1)))
                            } else if showingDone {
                                Text("\(doneCount) progress done")
                                    .font(.system(size: 14, weight: .semibold, design: .default))
                                    .foregroundColor(.green)
                                    .lineLimit(1)
                                    .fixedSize(horizontal: true, vertical: false)
                                    .padding(.trailing, 24)
                                    .transition(.opacity.animation(.easeIn(duration: 0.2).delay(0.1)))
                            }
                        }
                    }
                    .frame(width: earWidth)
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
                // Smooth animation for expansion and transitions
                .animation(.spring(response: 0.5, dampingFraction: 0.7, blendDuration: 0), value: earWidth)
                
                Spacer()
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        }
        .onAppear {
            previousSessions = daemonClient.sessions
        }
        .onChange(of: daemonClient.sessions) { newSessions in
            var completedCount = 0
            for (id, session) in newSessions {
                if let oldSession = previousSessions[id] {
                    let wasWorking = ["starting", "running", "executing_tool", "permission_resolving"].contains(oldSession.state)
                    // Permission/input states are attention requests, never a
                    // completion. Interactive agents report a completed turn
                    // through their normal idle prompt.
                    // Older wrappers only report an ambiguous Waiting state.
                    // The daemon maps it to idle, but it must not produce a
                    // false completion animation. New screen-classified idle
                    // states still represent a completed interactive turn.
                    let isDone = session.state == "completed" ||
                        (session.state == "idle" && session.source != "legacy-event")
                    if wasWorking && isDone {
                        completedCount += 1
                    }
                }
            }
            
            if completedCount > 0 {
                doneCount += completedCount
                showingDone = true
                
                doneTimer?.invalidate()
                doneTimer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: false) { _ in
                    DispatchQueue.main.async {
                        self.showingDone = false
                        self.doneCount = 0
                    }
                }
            }
            
            previousSessions = newSessions
        }
    }
}

struct ProgressIcon: View {
    var status: String
    
    // Braille spinner frames matching agy's animation
    private let frames = ["⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"]
    @State private var frameIndex = 0
    @State private var timer: Timer? = nil
    
    var body: some View {
        ZStack {
            if status == "Running" || status == "Initializing" {
                Text(frames[frameIndex])
                    .font(.system(size: 16, weight: .semibold, design: .monospaced))
                    .foregroundColor(status == "Initializing" ? .blue : .white)
                    .onAppear {
                        startTimer()
                    }
                    .onDisappear {
                        stopTimer()
                    }
                    .onChange(of: status) { _ in
                        startTimer()
                    }
            } else if status == "Waiting" {
                Image(systemName: "hand.raised.fill")
                    .foregroundColor(.yellow)
            } else {
                Image(systemName: "eye")
                    .foregroundColor(.gray)
            }
        }
    }
    
    private func startTimer() {
        stopTimer()
        guard status == "Running" || status == "Initializing" else { return }
        timer = Timer.scheduledTimer(withTimeInterval: 0.08, repeats: true) { _ in
            DispatchQueue.main.async {
                frameIndex = (frameIndex + 1) % frames.count
            }
        }
    }
    
    private func stopTimer() {
        timer?.invalidate()
        timer = nil
    }
}
