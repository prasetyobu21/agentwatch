import Foundation
import Combine

struct AgentSession: Codable, Identifiable, Equatable {
    var id: String { sessionID }
    let sessionID: String
    let agent: String
    let state: String
    let sequence: UInt64
    let confidence: Double
    let summary: String?
    let source: String

    enum CodingKeys: String, CodingKey {
        case sessionID = "sessionId"
        case agent, state, sequence, confidence, summary, source
    }

    // Compatibility helpers keep the existing SwiftUI views compact while the
    // daemon migrates from the original status protocol.
    var agentName: String { agent }
    var status: String {
        switch state {
        case "starting": return "Initializing"
        case "running", "executing_tool", "permission_resolving": return "Running"
        case "permission_required", "input_required": return "Waiting"
        case "completed", "orphaned": return "Finished"
        case "failed": return "Error"
        default: return "Idle"
        }
    }
    var message: String? { summary }
}

final class DaemonClient: NSObject, ObservableObject, URLSessionDataDelegate {
    @Published var sessions: [String: AgentSession] = [:]
    @Published var globalStatus: String = "Idle"

    private var fallbackTimer: AnyCancellable?
    private var streamSession: URLSession?
    private var streamTask: URLSessionDataTask?
    private var streamBuffer = ""
    private var reconnectWork: DispatchWorkItem?
    private var lastEventID: UInt64 = 0

    override init() {
        super.init()
        startStreaming()
        startFallbackPolling()
        fetchStatus()
    }

    deinit { streamTask?.cancel() }

    private func startFallbackPolling() {
        // SSE is the normal transport; this is only snapshot recovery.
        fallbackTimer = Timer.publish(every: 10, on: .main, in: .common)
            .autoconnect()
            .sink { [weak self] _ in self?.fetchStatus() }
    }

    private func startStreaming() {
        reconnectWork?.cancel()
        guard let url = URL(string: "http://127.0.0.1:8765/v1/events") else { return }
        var request = URLRequest(url: url)
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        if lastEventID > 0 { request.setValue(String(lastEventID), forHTTPHeaderField: "Last-Event-ID") }
        let configuration = URLSessionConfiguration.default
        configuration.timeoutIntervalForRequest = 60
        streamSession = URLSession(configuration: configuration, delegate: self, delegateQueue: nil)
        streamTask = streamSession?.dataTask(with: request)
        streamTask?.resume()
    }

    private func reconnect() {
        let work = DispatchWorkItem { [weak self] in self?.fetchStatus(); self?.startStreaming() }
        reconnectWork = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 1, execute: work)
    }

    func fetchStatus() {
        guard let url = URL(string: "http://127.0.0.1:8765/v1/status") else { return }
        // Remember exactly what the client knew when this snapshot began. An
        // absent, unchanged session is stale and can be removed; a newer SSE
        // event arriving while the request is in flight must be preserved.
        let knownSequences = sessions.mapValues(\.sequence)
        URLSession.shared.dataTask(with: url) { [weak self] data, _, error in
            guard let self, error == nil, let data else { return }
            guard let decoded = try? JSONDecoder().decode([String: AgentSession].self, from: data) else { return }
            DispatchQueue.main.async {
                for (id, sequence) in knownSequences where decoded[id] == nil && self.sessions[id]?.sequence == sequence {
                    self.sessions.removeValue(forKey: id)
                }
                for (id, session) in decoded where session.sequence >= (self.sessions[id]?.sequence ?? 0) {
                    self.sessions[id] = session
                    self.lastEventID = max(self.lastEventID, session.sequence)
                }
                self.updateGlobalStatus()
            }
        }.resume()
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        guard let text = String(data: data, encoding: .utf8) else { return }
        streamBuffer += text
        while let range = streamBuffer.range(of: "\n\n") {
            let frame = String(streamBuffer[..<range.lowerBound])
            streamBuffer.removeSubrange(..<range.upperBound)
            handle(frame: frame)
        }
    }

    private func handle(frame: String) {
        guard frame.contains("event: state"), let dataLine = frame.split(separator: "\n").first(where: { $0.hasPrefix("data: ") }) else { return }
        let json = String(dataLine.dropFirst(6)).data(using: .utf8)!
        guard let event = try? JSONDecoder().decode(AgentSession.self, from: json) else { return }
        DispatchQueue.main.async {
            guard event.sequence > (self.sessions[event.sessionID]?.sequence ?? 0) else { return }
            self.sessions[event.sessionID] = event
            self.lastEventID = max(self.lastEventID, event.sequence)
            self.updateGlobalStatus()
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) { reconnect() }

    private func updateGlobalStatus() {
        let states = sessions.values.map(\.state)
        let urgency = ["failed", "permission_required", "input_required", "orphaned", "executing_tool", "running", "permission_resolving", "starting", "completed", "idle"]
        let selected = urgency.first(where: { states.contains($0) }) ?? "idle"
        globalStatus = AgentSession(sessionID: "", agent: "", state: selected, sequence: 0, confidence: 0, summary: nil, source: "ui").status
    }
}
