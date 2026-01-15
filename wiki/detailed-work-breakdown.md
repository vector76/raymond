# Detailed Work Breakdown

A checklist of implementation tasks in order. Tests precede implementation to
support TDD. See `implementation-plan.md` for context on each phase and step.

---

## Phase 1: Core Infrastructure

### Step 1.1: Transition Tag Parsing

- [x] **1.1.1** Define `Transition` data structure (tag name, target filename, attributes dict)
- [x] **1.1.2** Write tests: parse `<goto>FILE.md</goto>` → Transition
- [x] **1.1.3** Write tests: parse `<reset>FILE.md</reset>` → Transition
- [x] **1.1.4** Write tests: parse `<result>payload</result>` → Transition with payload
- [x] **1.1.5** Write tests: parse `<function return="X.md">Y.md</function>` → Transition with attributes
- [x] **1.1.6** Write tests: parse `<call return="X.md">Y.md</call>` → Transition with attributes
- [x] **1.1.7** Write tests: parse `<fork next="X.md" item="foo">Y.md</fork>` → Transition with attributes
- [x] **1.1.8** Write tests: tag anywhere in text (not just last line)
- [x] **1.1.9** Write tests: zero tags → empty list
- [x] **1.1.10** Write tests: multiple tags → list with multiple items
- [x] **1.1.11** Write tests: path safety — reject `../FILE.md`, `foo/bar.md`, `C:\FILE.md`
- [x] **1.1.12** Implement `parse_transitions()` to pass all tests
- [x] **1.1.13** Add helper: `validate_single_transition()` that raises if count ≠ 1

### Step 1.2: State File Management

- [x] **1.2.1** Define state file location convention (e.g., `.raymond/state/{workflow_id}.json`)
- [x] **1.2.2** Write tests: `write_state()` creates file with correct JSON structure
- [x] **1.2.3** Write tests: `read_state()` returns dict matching written state
- [x] **1.2.4** Write tests: `read_state()` raises for missing file
- [x] **1.2.5** Write tests: `list_workflows()` returns IDs of existing state files
- [x] **1.2.6** Implement `read_state()`, `write_state()`, `list_workflows()`
- [x] **1.2.7** Add helper: `create_initial_state()` for starting a new workflow

### Step 1.3: Prompt File Loading

- [x] **1.3.1** Write tests: `load_prompt(scope_dir, filename)` returns file contents
- [x] **1.3.2** Write tests: raises for missing file
- [x] **1.3.3** Write tests: raises if filename contains path separators (defense in depth)
- [x] **1.3.4** Implement `load_prompt()`

### Step 1.4: Template Substitution

- [x] **1.4.1** Write tests: `render_prompt()` replaces `{{key}}` with value
- [x] **1.4.2** Write tests: multiple placeholders in same template
- [x] **1.4.3** Write tests: missing key in variables → leave placeholder or raise (decide policy)
- [x] **1.4.4** Write tests: `{{result}}` placeholder specifically (common case)
- [x] **1.4.5** Implement `render_prompt()`

---

## Phase 2: Single Workflow Orchestration

### Step 2.1: Basic Orchestrator Loop

- [x] **2.1.1** Write tests: `run_all_agents()` reads state file at start
- [x] **2.1.2** Write tests: orchestrator exits when `agents` array is empty
- [x] **2.1.3** Write tests: orchestrator calls Claude Code wrapper for each agent
- [x] **2.1.4** Write tests: orchestrator parses output and dispatches to handler
- [x] **2.1.5** Write tests: parse error (zero tags) raises exception
- [x] **2.1.6** Write tests: parse error (multiple tags) raises exception
- [x] **2.1.7** Implement `run_all_agents()` skeleton with stub handlers (all raise NotImplementedError)
- [x] **2.1.8** Implement dispatcher that routes tag type to handler

### Step 2.2: Goto (Resume Session)

Extends `wrap_claude_code()` from existing `src/cc_wrap.py`.

- [x] **2.2.1** Write tests: `wrap_claude_code()` accepts optional `session_id` parameter
- [x] **2.2.2** Write tests: when `session_id` provided, passes `--resume` flag
- [x] **2.2.3** Write tests: when `session_id` is None, no `--resume` flag
- [x] **2.2.4** Write tests: `wrap_claude_code()` returns session_id from Claude Code output
- [x] **2.2.5** Extend `wrap_claude_code()` implementation
- [x] **2.2.6** Write tests: orchestrator stores returned session_id in agent state
- [x] **2.2.7** Write tests: `<goto>` handler updates agent's `current_state`
- [x] **2.2.8** Write tests: `<goto>` handler preserves `session_id` for resume
- [x] **2.2.9** Write tests: `<result>` with empty stack removes agent from array
- [x] **2.2.10** Implement `<goto>` handler
- [x] **2.2.11** Implement `<result>` handler (empty stack case only)

**Integration checkpoint:** Can run a simple goto+result workflow (Test 2 from `sample-workflows.md`).

### Step 2.3: Reset (Fresh Start)

- [x] **2.3.1** Write tests: `<reset>` handler updates `current_state`
- [x] **2.3.2** Write tests: `<reset>` handler sets `session_id` to None (fresh start)
- [x] **2.3.3** Write tests: `<reset>` handler clears return stack
- [x] **2.3.4** Write tests: `<reset>` with non-empty stack logs warning
- [x] **2.3.5** Implement `<reset>` handler

**Integration checkpoint:** Can run reset workflow (Test 6 from `sample-workflows.md`).

### Step 2.4: Function (Stateless with Return)

- [x] **2.4.1** Write tests: `<function>` handler pushes frame to stack
- [x] **2.4.2** Write tests: pushed frame contains caller's session_id and return state
- [x] **2.4.3** Write tests: `<function>` handler sets `session_id` to None (fresh)
- [x] **2.4.4** Write tests: `<function>` handler updates `current_state` to function target
- [x] **2.4.5** Implement `<function>` handler
- [x] **2.4.6** Write tests: `<result>` with non-empty stack pops frame
- [x] **2.4.7** Write tests: `<result>` resumes caller's session_id
- [x] **2.4.8** Write tests: `<result>` sets `current_state` to return state from frame
- [x] **2.4.9** Write tests: `<result>` payload available as `{{result}}` variable
- [x] **2.4.10** Extend `<result>` handler for non-empty stack case

**Integration checkpoint:** Create and run a minimal `<function>` test case (not in
`sample-workflows.md` — Test 1 there is stateless without return stack semantics).

### Step 2.5: Call with Return

- [ ] **2.5.1** Write tests: `<call>` handler pushes frame to stack (like function)
- [ ] **2.5.2** Write tests: `<call>` handler uses Claude Code `--fork` to branch context from caller
- [ ] **2.5.3** Write tests: `<call>` handler updates `current_state` to callee target
- [ ] **2.5.4** Implement `handle_call_transition()`

**Integration checkpoint:** Can run call-with-return workflow (Test 3 from `sample-workflows.md`).

---

## Phase 3: Fork (Multi-Agent)

### Step 3.1: Fork Implementation

- [ ] **3.1.1** Write tests: `<fork>` handler creates new agent in `agents` array
- [ ] **3.1.2** Write tests: new agent has unique ID
- [ ] **3.1.3** Write tests: new agent has empty return stack
- [ ] **3.1.4** Write tests: new agent has `session_id` = None (fresh)
- [ ] **3.1.5** Write tests: new agent's `current_state` is fork target
- [ ] **3.1.6** Write tests: parent agent continues at `next` state
- [ ] **3.1.7** Write tests: fork attributes available as template variables for new agent
- [ ] **3.1.8** Implement `<fork>` handler

**Integration checkpoint:** Can run fork workflow (Test 4 from `sample-workflows.md`).

---

## Phase 4: Robustness

### Step 4.1: Error Handling

- [ ] **4.1.1** Write tests: Claude Code non-zero exit raises appropriate exception
- [ ] **4.1.2** Write tests: missing prompt file raises appropriate exception
- [ ] **4.1.3** Write tests: malformed state file raises appropriate exception
- [ ] **4.1.4** Add exception handling throughout orchestrator
- [ ] **4.1.5** Define recovery strategies (retry, skip, abort)

### Step 4.2: Crash Recovery

- [ ] **4.2.1** Write tests: orchestrator can resume from existing state file
- [ ] **4.2.2** Write tests: `recover_workflows()` finds in-progress workflows
- [ ] **4.2.3** Implement `recover_workflows()`

### Step 4.3: Logging

- [ ] **4.3.1** Add structured logging for state transitions
- [ ] **4.3.2** Add structured logging for Claude Code invocations
- [ ] **4.3.3** Add structured logging for errors

---

## Phase 5: Refinements

### Step 5.1: Evaluator Integration

- [ ] **5.1.1** Design evaluator override mechanism
- [ ] **5.1.2** Add iteration counting to agent state
- [ ] **5.1.3** Implement iteration limit enforcement

### Step 5.2: Result Extraction

- [ ] **5.2.1** Design configurable result extraction
- [ ] **5.2.2** Implement extraction options

### Step 5.3: Workflow Configuration

- [ ] **5.3.1** Design YAML frontmatter schema for per-state policy
- [ ] **5.3.2** Implement frontmatter parsing in `load_prompt()`
- [ ] **5.3.3** Implement policy enforcement in orchestrator

### Step 5.4: Protocol Reminder

- [ ] **5.4.1** Design reminder prompt format
- [ ] **5.4.2** Replace parse error exception with re-prompt logic

---

## Notes

- **Integration checkpoints** reference test workflows in `sample-workflows.md`
- **TDD approach**: Write tests first, then implement to pass them
- **Incremental delivery**: Each step should leave the system in a working state
- Tasks in Phase 4-5 are less granular; break down further when starting them
