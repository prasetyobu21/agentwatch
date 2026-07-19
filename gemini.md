# AgentWatch Project Progress

## Changelog
- Set up initial project structure (Go daemon and CLI wrapper, SwiftUI app).
- Created Dynamic Island style expanding notch for AgentWatch.
- Fixed notch layout to be perfectly symmetrical around a hard-coded 220pt center gap to avoid text getting covered by the camera hardware.
- Added a prototype control panel in the macOS Menu Bar (accessed via the eye icon) with sliders and steppers for easy testing.
- Disabled mouse event interception on the notch window so that standard macOS menu items behind the transparent areas remain clickable.
- Adjusted the expanded notch height to 38pt to perfectly align with the macOS menu bar.
- Implemented continuous corner radius for a native Apple-like aesthetic.
