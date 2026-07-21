import SwiftUI
import AppKit
import CoreGraphics

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
    private var notchWindows: [String: NSPanel] = [:]
    var daemonClient = DaemonClient()
    let displayManager = DisplayManager()
    private var daemonProcess: Process? = nil
    private var screenParametersObserver: NSObjectProtocol?

    func applicationDidFinishLaunching(_ notification: Notification) {
        startDaemonIfNeeded()
        screenParametersObserver = NotificationCenter.default.addObserver(
            forName: NSApplication.didChangeScreenParametersNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            self?.displayManager.refreshDisplays()
            self?.updateNotchWindows()
        }
        displayManager.refreshDisplays()
        updateNotchWindows()
    }
    
    func applicationWillTerminate(_ notification: Notification) {
        if let screenParametersObserver {
            NotificationCenter.default.removeObserver(screenParametersObserver)
        }
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

    private func updateNotchWindows() {
        let selectedScreens = displayManager.selectedScreens
        let selectedIDs = Set(selectedScreens.map(\.id))

        let staleIDs = notchWindows.keys.filter { !selectedIDs.contains($0) }
        for id in staleIDs {
            notchWindows[id]?.orderOut(nil)
            notchWindows.removeValue(forKey: id)
        }

        for display in selectedScreens {
            let window: NSPanel
            if let existingWindow = notchWindows[display.id] {
                window = existingWindow
                setNotchContent(of: window, for: display.screen)
            } else {
                window = makeNotchWindow(for: display.screen)
            }
            position(window, on: display.screen)
            window.orderFrontRegardless()
            notchWindows[display.id] = window
        }
    }

    private func makeNotchWindow(for screen: NSScreen) -> NSPanel {
        let width: CGFloat = 800 // Very wide to allow dynamic SwiftUI sizing without clipping
        let height = notchSize(for: screen).height

        let window = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: width, height: height),
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: false
        )

        window.isOpaque = false
        window.backgroundColor = .clear
        window.hasShadow = false
        window.level = .mainMenu + 2
        window.collectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle]
        window.ignoresMouseEvents = true
        
        setNotchContent(of: window, for: screen)
        return window
    }

    private func setNotchContent(of window: NSPanel, for screen: NSScreen) {
        let notchSize = notchSize(for: screen)
        window.setContentSize(NSSize(width: 800, height: notchSize.height))
        let hostingView = NSHostingView(rootView: NotchView(daemonClient: daemonClient, notchWidth: notchSize.width, notchHeight: notchSize.height).edgesIgnoringSafeArea(.all))
        hostingView.wantsLayer = true
        hostingView.layer?.backgroundColor = NSColor.clear.cgColor
        window.contentView = hostingView
    }

    private func notchSize(for screen: NSScreen) -> CGSize {
        let width: CGFloat
        if let left = screen.auxiliaryTopLeftArea, let right = screen.auxiliaryTopRightArea {
            width = max(0, right.minX - left.maxX)
        } else {
            width = 210
        }
        return CGSize(width: width, height: screen.safeAreaInsets.top > 0 ? screen.safeAreaInsets.top : 38)
    }

    private func position(_ window: NSPanel, on screen: NSScreen) {
        let screenFrame = screen.frame
        let windowSize = window.frame.size
        let origin = NSPoint(
            x: screenFrame.midX - windowSize.width / 2,
            y: screenFrame.maxY - windowSize.height
        )
        window.setFrameOrigin(origin)
    }
}

struct DisplayInfo: Identifiable {
    let id: String
    let name: String
    let isBuiltIn: Bool
    let screen: NSScreen
}

final class DisplayManager: ObservableObject {
    @Published private(set) var displays: [DisplayInfo] = []

    var selectedScreens: [DisplayInfo] {
        // For now the notch belongs exclusively to the Mac's physical panel.
        // Never fall back to an external display when the built-in display is
        // unavailable (for example, while a MacBook is used in clamshell mode).
        displays.filter(\.isBuiltIn)
    }

    func refreshDisplays() {
        displays = NSScreen.screens.map { screen in
            let screenNumber = screen.deviceDescription[NSDeviceDescriptionKey("NSScreenNumber")] as? NSNumber
            let displayID = CGDirectDisplayID(screenNumber?.uint32Value ?? 0)
            return DisplayInfo(
                id: Self.stableID(for: displayID),
                name: screen.localizedName,
                isBuiltIn: CGDisplayIsBuiltin(displayID) != 0,
                screen: screen
            )
        }

    }

    private static func stableID(for displayID: CGDirectDisplayID) -> String {
        guard let uuid = CGDisplayCreateUUIDFromDisplayID(displayID)?.takeRetainedValue() else {
            return String(displayID)
        }
        return CFUUIDCreateString(nil, uuid) as String
    }
}

struct NotchView: View {
    @ObservedObject var daemonClient: DaemonClient
    let notchWidth: CGFloat
    let notchHeight: CGFloat
    
    @State private var previousSessions: [String: AgentSession] = [:]
    @State private var showingDone: Bool = false
    @State private var doneCount: Int = 0
    @State private var doneTimer: Timer? = nil
    @State private var displayedAttentionKind: String? = nil
    @State private var displayedAttentionCount: Int = 0
    @State private var attentionShownAt: Date = .distantPast
    @State private var attentionTimer: Timer? = nil
    
    var activeCount: Int {
        activeSessions.count
    }

    private var activeSessions: [AgentSession] {
        daemonClient.sessions.values.filter {
            ["starting", "running", "executing_tool", "permission_resolving", "permission_required", "input_required"].contains($0.state)
        }
    }

    // The notch must derive both its visibility and its icon from the same
    // sessions. `globalStatus` is an aggregate convenience label and can lag
    // behind an individual state update during SSE recovery.
    private var activeIndicatorStatus: String {
        let states = Set(activeSessions.map(\.state))
        if states.contains("permission_required") || states.contains("input_required") {
            return "Waiting"
        }
        if states.contains("starting") && !states.contains(where: { $0 != "starting" }) {
            return "Initializing"
        }
        return "Running"
    }

    var permissionCount: Int {
        daemonClient.sessions.values.filter { $0.state == "permission_required" }.count
    }

    var inputCount: Int {
        daemonClient.sessions.values.filter { $0.state == "input_required" }.count
    }

    var attentionText: String? {
        if displayedAttentionKind == "permission" {
            return "Asking for permission (\(displayedAttentionCount))"
        }
        if displayedAttentionKind == "input" {
            return "Asking for input (\(displayedAttentionCount))"
        }
        return nil
    }
    
    var isExpanded: Bool {
        activeCount > 0 || attentionText != nil || showingDone
    }
    
    var earWidth: CGFloat {
        if displayedAttentionKind == "permission" {
            // The permission label plus its trailing inset is wider than the
            // compact progress ear. Keep it fully outside the physical notch.
            return 220
        } else if displayedAttentionKind == "input" {
            // Input requests use a shorter label and do not need the extra
            // trailing space reserved for permission text.
            return 150
        } else if activeCount > 0 {
            return 60
        } else if showingDone {
            return 130
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
                            if attentionText != nil {
                                ProgressIcon(status: "Waiting")
                                    .frame(width: 20, height: 20)
                                    .padding(.leading, 24)
                                    .transition(.opacity.animation(.easeIn(duration: 0.2).delay(0.1)))
                            } else if activeCount > 0 {
                                ProgressIcon(status: activeIndicatorStatus)
                                    .frame(width: 20, height: 20)
                                    .padding(.leading, 24)
                                    .transition(.opacity.animation(.easeIn(duration: 0.2).delay(0.1)))
                            } else if showingDone && activeCount == 0 {
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
                        .frame(width: notchWidth, height: notchHeight)
                    
                    // Right ear
                    HStack {
                        if isExpanded {
                            if let attentionText {
                                Text(attentionText)
                                    .font(.system(size: 14, weight: .semibold, design: .default))
                                    .foregroundColor(displayedAttentionKind == "permission" ? .yellow : .orange)
                                    .lineLimit(1)
                                    .fixedSize(horizontal: true, vertical: false)
                                    .padding(.leading, 12)
                                    .transition(.opacity.animation(.easeIn(duration: 0.2).delay(0.1)))
                                Spacer(minLength: 0)
                            } else if showingDone {
                                Spacer(minLength: 0)
                                Text("\(doneCount) progress done")
                                    .font(.system(size: 14, weight: .semibold, design: .default))
                                    .foregroundColor(.green)
                                    .lineLimit(1)
                                    .fixedSize(horizontal: true, vertical: false)
                                    .padding(.trailing, 24)
                                    .transition(.opacity.animation(.easeIn(duration: 0.2).delay(0.1)))
                            }
                        } else {
                            Spacer(minLength: 0)
                        }
                    }
                    .frame(width: earWidth)
                    .clipped()
                }
                .frame(height: notchHeight)
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
            refreshAttentionState()
        }
        .onChange(of: daemonClient.sessions) { newSessions in
            refreshAttentionState()
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
                        // Approval TUIs can briefly paint an idle-looking
                        // composer before their permission menu. Only announce
                        // completion if this state survives the redraw.
                        DispatchQueue.main.asyncAfter(deadline: .now() + 0.35) {
                            guard daemonClient.sessions[id]?.state == session.state else { return }
                            doneCount += 1
                            showingDone = true

                            doneTimer?.invalidate()
                            doneTimer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: false) { _ in
                                DispatchQueue.main.async {
                                    self.showingDone = false
                                    self.doneCount = 0
                                }
                            }
                        }
                    }
                }
            }

            previousSessions = newSessions
        }
        .onDisappear {
            attentionTimer?.invalidate()
        }
    }

    private func refreshAttentionState() {
        let nextKind: String?
        let nextCount: Int
        if permissionCount > 0 {
            nextKind = "permission"
            nextCount = permissionCount
        } else if inputCount > 0 {
            nextKind = "input"
            nextCount = inputCount
        } else {
            nextKind = nil
            nextCount = 0
        }

        if let nextKind {
            attentionTimer?.invalidate()
            attentionTimer = nil
            if displayedAttentionKind != nextKind {
                attentionShownAt = Date()
            }
            displayedAttentionKind = nextKind
            displayedAttentionCount = nextCount
            return
        }

        guard displayedAttentionKind != nil else { return }
        let remaining = max(0, 2.0 - Date().timeIntervalSince(attentionShownAt))
        attentionTimer?.invalidate()
        attentionTimer = Timer.scheduledTimer(withTimeInterval: remaining, repeats: false) { _ in
            DispatchQueue.main.async {
                guard permissionCount == 0 && inputCount == 0 else { return }
                displayedAttentionKind = nil
                displayedAttentionCount = 0
                attentionTimer = nil
            }
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
