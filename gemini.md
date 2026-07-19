# AgentWatch Project Progress

## Changelog
<!-- Add new changelog entries here for the next batch of work -->
- Normalized carriage returns (`\r` to `\n`) in the CLI wrapper output parser to properly split visual lines.
- Restructured idle/busy detection to check the last 5 non-empty visual lines instead of scanning the entire scrolling buffer.
- Added a short-circuit check for idle status bar indicators (`? for shortcuts`, `← for agents`, `ctrl-c again to exit`) on the very bottom visual line, preventing historical completion markers from locking the state.
- Updated the SwiftUI macOS app's Notch progress indicator to use an animated Unicode Braille spinner (`⣾`, `⣽`, `⣻`...) cycling at `0.08s` matching the agy style.
- Added a new `StatusInitializing` state ("Initializing") to represent command startup/opening.
- Set the initial PTY session status to `StatusInitializing` in `cmd/agentwatch/main.go`.
- Enhanced ANSI escape sequence stripping regex in the CLI wrapper to reliably catch cursor-related escape sequences (like `\x1b[?25h`), improving idle prompt detection.
- Improved prompt idle detection in the CLI wrapper to inspect the last output line for specific prompt symbols (like `❯` and `User:`), preventing echoed user typing from incorrectly triggering the "Running" state.
- Updated the SwiftUI macOS app to count `Initializing` sessions as active and display a blue progress indicator on the Notch.
- Added a live list of active agents and their specific status names (with color-coded pills) directly inside the macOS menu bar extra dropdown.
