# AgentWatch status model

AgentWatch derives state from terminal observations and process lifecycle. The
terminal remains transparent: status detection never changes the command,
injects input, or writes agent configuration.

`starting`, `running`, `executing_tool`, `permission_required`,
`input_required`, `permission_resolving`, `idle`, `completed`, `failed`, and
`orphaned` are the normalized states published through the versioned API.

The wrapper only sends coarse input categories in memory (`enter`, `escape`,
`tab`, `interrupt`, and text). It does not transmit or persist what the user
typed.

The daemon exposes a current snapshot at `GET /v1/status` and a local SSE stream
at `GET /v1/events`. Events have per-daemon monotonic sequence numbers. Clients
should reconnect with `Last-Event-ID` and recover with a snapshot whenever a
sequence gap is detected. The legacy `/event` and `/status` endpoints remain
available during the migration.
