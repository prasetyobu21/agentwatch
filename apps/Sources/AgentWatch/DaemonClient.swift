import Foundation
import Combine

struct AgentSession: Codable, Identifiable, Equatable {
    var id: String { sessionID }
    let sessionID: String
    let agentName: String
    let status: String
    let message: String?

    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case agentName = "agent_name"
        case status
        case message
    }
}

class DaemonClient: ObservableObject {
    @Published var sessions: [String: AgentSession] = [:]
    @Published var globalStatus: String = "Idle"
    
    private var timer: AnyCancellable?
    
    init() {
        startPolling()
    }
    
    func startPolling() {
        // The daemon is local, so frequent lightweight polling keeps the notch
        // in sync with short Codex turns without visible lag.
        timer = Timer.publish(every: 0.1, on: .main, in: .common)
            .autoconnect()
            .sink { [weak self] _ in
                self?.fetchStatus()
            }
        // Fetch immediately
        fetchStatus()
    }
    
    func fetchStatus() {
        guard let url = URL(string: "http://127.0.0.1:8765/status") else { return }
        
        let task = URLSession.shared.dataTask(with: url) { [weak self] data, response, error in
            guard let self = self else { return }
            
            if error != nil || data == nil {
                DispatchQueue.main.async {
                    if !self.sessions.isEmpty {
                        self.sessions.removeAll()
                        self.updateGlobalStatus()
                    }
                }
                return
            }
            
            do {
                let decoded = try JSONDecoder().decode([String: AgentSession].self, from: data!)
                DispatchQueue.main.async {
                    self.sessions = decoded
                    self.updateGlobalStatus()
                }
            } catch {
                print("Failed to decode daemon status:", error)
            }
        }
        task.resume()
    }
    
    private func updateGlobalStatus() {
        let allStatuses = sessions.values.map { $0.status }
        
        if allStatuses.contains("Error") {
            globalStatus = "Error"
        } else if allStatuses.contains("Running") {
            globalStatus = "Running"
        } else if allStatuses.contains("Initializing") {
            globalStatus = "Initializing"
        } else if allStatuses.contains("Waiting") {
            globalStatus = "Waiting"
        } else {
            globalStatus = "Idle"
        }
    }
}
