# Terminal Title Bar

This document describes how Raymond updates the terminal window title during workflow execution and the design decisions behind the feature.

## Purpose

When Raymond runs a workflow, execution can span many states and potentially multiple concurrent agents. The console output stream shows each transition and tool invocation, but a user who glances away and returns cannot immediately tell what is happening without scrolling back to find the last state announcement.

The terminal title bar solves this: at any moment, the user can glance at the window or tab title and see which state is currently executing. This is particularly useful when multiple agents run concurrently, or when long-running states produce no visible console output for extended periods.

## Mechanism

Raymond uses OSC 2 (Operating System Command 2) escape sequences to set the terminal title:

```
\x1b]2;<title>\x07
```

This is `ESC` + `]2;` + the title string + `BEL` (`\x07`). The sequence is written to `sys.stdout` and flushed immediately. There is no trailing newline — the sequence is invisible to the user and does not affect the displayed output.

OSC 2 is the standard mechanism for setting terminal window and tab titles. It is recognized by virtually all modern terminal emulators — iTerm2, GNOME Terminal, Windows Terminal, Konsole, tmux (forwarded to the outer terminal), and others. Terminals that do not support OSC 2 silently ignore the byte sequence. There is no error output and no fallback needed.

The title format is:

```
ray: <stem>
```

where `<stem>` is the state filename with its last extension stripped. For example, `START.md` becomes `ray: START`, `CHECK.sh` becomes `ray: CHECK`, and `foo.bar.md` becomes `ray: foo.bar`.

## Architecture

`TitleBarObserver` lives in `src/orchestrator/observers/titlebar.py` and follows the same observer pattern as `ConsoleObserver` and `DebugObserver`. It subscribes to `StateStarted` events on the `EventBus` and writes the OSC 2 sequence on each event.

`StateStarted` is the right trigger because it fires at the moment a state begins executing — before Claude is invoked for markdown states, or before a script is run for script states. This gives the user immediate feedback the instant execution shifts to a new state.

Extension stripping uses `pathlib.Path(state_name).stem`, which removes only the last extension generically. This avoids hardcoding `.md`, `.sh`, `.bat`, or any other specific extension, and handles edge cases correctly: a name with no extension passes through unchanged, and a name with multiple dots (e.g. `foo.bar.md`) has only the final extension removed.

`TitleBarObserver` is registered unconditionally in `run_all_agents()` in `src/orchestrator/workflow.py`, immediately after `ConsoleObserver` is set up. It is closed in the `finally` block alongside the other observers.

Handler exceptions are caught and logged as warnings using the same pattern as all other observers. Exceptions in handlers are never propagated to the orchestration loop.

## Multiple Agents

When multiple agents run concurrently, each fires its own `StateStarted` events as it transitions between states. All writes go to the same `sys.stdout`, so the terminal title reflects whichever agent wrote most recently — last-write-wins.

No agent ID or disambiguation is shown in the title. The format `ray: <state>` is intentionally short and readable. In concurrent scenarios, the title reflects the most recently started state across all agents, which is a reasonable approximation of "what is the system doing right now." Users who need precise per-agent tracking can follow the console output or enable debug mode.

## Always-On Design

There is no flag, environment variable, or constructor parameter to disable the title bar updates. This is intentional:

- The cost is negligible: a few invisible bytes written to stdout per state transition.
- The benefit is consistent: every user running Raymond in a terminal gets the feature without having to discover or enable it.
- Optional features create configuration surface, additional code paths, and conditional logic that are harder to test and maintain. Keeping the feature unconditional keeps the implementation simple and the test surface small.

The feature degrades gracefully in environments that do not support OSC 2 — the escape bytes are silently ignored, and no user-visible output is affected.
