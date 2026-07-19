# AgentWatch — Product & Technical Plan

## Vision
AgentWatch is a macOS menu bar app that monitors terminal AI agents.

## Background Context
I frequently use the terminal to run multiple coding agents (such as Claude Code, Codex, and Antigravity) concurrently. Since I often multitask in other windows while these agents are executing, it's easy to lose track of their progress. AgentWatch solves this by providing a persistent notch/menu bar UI that instantly alerts me when an agent has finished a task or requires my input, without needing to constantly check the terminal.

## Architecture
- agentwatch: CLI wrapper (Go)
- agentwatchd: daemon (Go)
- AgentWatch.app: SwiftUI menu bar UI

## Tech Stack & Prerequisites
To build and develop this project locally, you will need:
- **[Xcode](https://apps.apple.com/us/app/xcode/id497799835)**: Required to build the native SwiftUI macOS app and interface with system-level UI like the Notch. (Note: The standalone Command Line Tools are not sufficient).
- **Go (Golang)**: Used for building the lightweight background daemon and CLI wrapper. (Can be installed via `brew install go`).

## Goals
- Support Claude Code, Codex, Antigravity, Gemini CLI, Aider
- Show Running, Waiting, Idle, Finished, Error
- Notify when user input is required
- Open the correct terminal window

## Design Principles
- Agent agnostic
- Lightweight
- Native macOS
- Plugin architecture
- Open source

## Folder Structure
```text
AgentWatch/
  apps/AgentWatch.app/
  cmd/agentwatch/
  cmd/agentwatchd/
  parsers/
  internal/
  docs/
```

## MVP
- [x] 1. CLI wrapper
- [x] 2. Background daemon
- [x] 3. Menu bar app
- [ ] 4. Claude + Codex support
- [ ] 5. Ghostty + Terminal support
- [ ] 6. Notifications
- [ ] 7. Multiple concurrent sessions

## Future
- Dashboard
- Notch UI
- Token usage
- Runtime
- Plugin SDK
- MCP integration
