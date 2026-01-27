"""Command-line interface for the Raymond orchestrator."""

import argparse
import asyncio
import logging
import re
import sys
from pathlib import Path
from typing import Optional

from .config import load_config, merge_config_and_args, init_config, ConfigError
from .orchestrator import run_all_agents
from .state import (
    create_initial_state,
    write_state,
    read_state,
    list_workflows,
    recover_workflows,
    generate_workflow_id,
)


# Pattern for valid workflow IDs: alphanumeric, hyphens, underscores only
WORKFLOW_ID_PATTERN = re.compile(r'^[a-zA-Z0-9_-]+$')


def positive_float_or_zero(value: str) -> float:
    """Argparse type for non-negative float values (used for timeout).
    
    Args:
        value: String value from command line
        
    Returns:
        Float value if valid
        
    Raises:
        argparse.ArgumentTypeError: If value is negative
    """
    try:
        fval = float(value)
    except ValueError:
        raise argparse.ArgumentTypeError(f"invalid float value: '{value}'")
    
    if fval < 0:
        raise argparse.ArgumentTypeError(f"timeout must be non-negative, got {fval}")
    
    return fval


def positive_float(value: str) -> float:
    """Argparse type for positive float values (used for budget).
    
    Args:
        value: String value from command line
        
    Returns:
        Float value if valid
        
    Raises:
        argparse.ArgumentTypeError: If value is not positive
    """
    try:
        fval = float(value)
    except ValueError:
        raise argparse.ArgumentTypeError(f"invalid float value: '{value}'")
    
    if fval <= 0:
        raise argparse.ArgumentTypeError(f"budget must be positive, got {fval}")
    
    return fval


def validate_workflow_id(workflow_id: str) -> Optional[str]:
    """Validate workflow_id is safe for use in file paths.
    
    Args:
        workflow_id: The workflow identifier to validate
        
    Returns:
        Error message if invalid, None if valid
    """
    if not workflow_id:
        return "Workflow ID cannot be empty"
    
    if len(workflow_id) > 255:
        return "Workflow ID too long (max 255 characters)"
    
    if not WORKFLOW_ID_PATTERN.match(workflow_id):
        return (
            "Workflow ID contains invalid characters. "
            "Only letters, numbers, hyphens, and underscores are allowed."
        )
    
    # Prevent reserved names on Windows
    reserved_names = {'CON', 'PRN', 'AUX', 'NUL', 
                      'COM1', 'COM2', 'COM3', 'COM4', 'COM5', 'COM6', 'COM7', 'COM8', 'COM9',
                      'LPT1', 'LPT2', 'LPT3', 'LPT4', 'LPT5', 'LPT6', 'LPT7', 'LPT8', 'LPT9'}
    if workflow_id.upper() in reserved_names:
        return f"Workflow ID '{workflow_id}' is a reserved name"
    
    return None


def setup_logging(verbose: bool = False) -> None:
    """Configure logging for the CLI.
    
    By default, logging does not output to console (only to files/debug output).
    In verbose mode, DEBUG-level logs are shown on console.
    """
    # Remove any existing handlers to avoid duplicates
    root_logger = logging.getLogger()
    root_logger.handlers.clear()
    
    if verbose:
        # Verbose mode: show DEBUG-level logs on console
        console_handler = logging.StreamHandler(sys.stderr)
        console_handler.setLevel(logging.DEBUG)
        console_handler.setFormatter(
            logging.Formatter(
                "%(asctime)s [%(levelname)s] %(name)s: %(message)s",
                datefmt="%Y-%m-%d %H:%M:%S"
            )
        )
        root_logger.addHandler(console_handler)
        root_logger.setLevel(logging.DEBUG)
    else:
        # Default mode: no console output from logging
        # Logging calls still work for file/debug output, but won't appear on console
        root_logger.setLevel(logging.DEBUG)  # Still capture all levels for file handlers
        # No console handler added, so nothing appears on console


def cmd_start(args: argparse.Namespace) -> int:
    """Start a new workflow."""
    # Generate workflow_id if not provided
    if args.workflow_id is None:
        workflow_id = generate_workflow_id(state_dir=args.state_dir)
        print(f"Generated workflow ID: {workflow_id}")
    else:
        workflow_id = args.workflow_id
        # Validate workflow_id if provided
        error = validate_workflow_id(workflow_id)
        if error:
            print(f"Error: {error}", file=sys.stderr)
            return 1
    
    # Parse the initial file path to extract scope_dir and initial_state
    initial_path = Path(args.initial_file)
    
    # Validate initial state file exists
    if not initial_path.is_file():
        print(f"Error: Initial state file does not exist: {initial_path}", file=sys.stderr)
        return 1
    
    # Infer scope directory and initial state filename
    scope_dir = str(initial_path.parent.resolve())
    initial_state = initial_path.name
    state_dir = args.state_dir
    
    # Check if workflow already exists
    existing = list_workflows(state_dir=state_dir)
    if workflow_id in existing:
        print(f"Error: Workflow '{workflow_id}' already exists. Use --resume to continue it.", file=sys.stderr)
        return 1
    
    # Get budget from args or use default
    budget_usd = args.budget if args.budget is not None else 10.0
    
    # Create and write initial state
    state = create_initial_state(workflow_id, scope_dir, initial_state, budget_usd=budget_usd, initial_input=args.initial_input)
    write_state(workflow_id, state, state_dir=state_dir)
    
    print(f"Created workflow '{workflow_id}'")
    print(f"  Scope directory: {scope_dir}")
    print(f"  Initial state: {initial_state}")
    if args.initial_input is not None:
        # Truncate long inputs for display
        display_input = args.initial_input if len(args.initial_input) <= 50 else args.initial_input[:50] + "..."
        print(f"  Initial input: {display_input}")
    
    if not args.no_run:
        print("\nStarting orchestrator...")
        debug = not args.no_debug
        return cmd_run_workflow(workflow_id, state_dir, args.verbose, debug, args.model, args.timeout, args.dangerously_skip_permissions, args.quiet, getattr(args, 'width', None))
    
    print(f"\nRun with: raymond --resume {workflow_id}")
    return 0


def cmd_resume(args: argparse.Namespace) -> int:
    """Resume an existing workflow."""
    workflow_id = args.resume
    
    # Validate workflow_id
    error = validate_workflow_id(workflow_id)
    if error:
        print(f"Error: {error}", file=sys.stderr)
        return 1
    
    # Check if workflow exists before trying to run
    try:
        read_state(workflow_id, state_dir=args.state_dir)
    except FileNotFoundError:
        print(f"Error: Workflow '{workflow_id}' not found.", file=sys.stderr)
        return 1
    
    debug = not args.no_debug
    return cmd_run_workflow(workflow_id, args.state_dir, args.verbose, debug, args.model, args.timeout, args.dangerously_skip_permissions, args.quiet, getattr(args, 'width', None))


def cmd_run_workflow(workflow_id: str, state_dir: Optional[str], verbose: bool, debug: bool = True, default_model: Optional[str] = None, timeout: Optional[float] = None, dangerously_skip_permissions: bool = False, quiet: bool = False, width: Optional[int] = None) -> int:
    """Run a workflow by ID."""
    setup_logging(verbose)

    try:
        asyncio.run(run_all_agents(workflow_id, state_dir=state_dir, debug=debug, default_model=default_model, timeout=timeout, dangerously_skip_permissions=dangerously_skip_permissions, quiet=quiet, width=width))
        # Note: workflow completion message is displayed by console reporter
        return 0
    except FileNotFoundError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("\nInterrupted by user.")
        return 130
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1


def cmd_list(args: argparse.Namespace) -> int:
    """List all workflows."""
    state_dir = args.state_dir
    
    workflows = list_workflows(state_dir=state_dir)
    
    if not workflows:
        print("No workflows found.")
        return 0
    
    # Get in-progress workflows
    in_progress = set(recover_workflows(state_dir=state_dir))
    
    print(f"Workflows ({len(workflows)} total):\n")
    for wf_id in sorted(workflows):
        status = "in-progress" if wf_id in in_progress else "completed"
        print(f"  {wf_id}  [{status}]")
    
    return 0


def cmd_status(args: argparse.Namespace) -> int:
    """Show status of a workflow."""
    workflow_id = args.status
    state_dir = args.state_dir
    
    # Validate workflow_id
    error = validate_workflow_id(workflow_id)
    if error:
        print(f"Error: {error}", file=sys.stderr)
        return 1
    
    try:
        state = read_state(workflow_id, state_dir=state_dir)
    except FileNotFoundError:
        print(f"Error: Workflow '{workflow_id}' not found.", file=sys.stderr)
        return 1
    
    agents = state.get("agents", [])
    scope_dir = state.get("scope_dir", "unknown")
    
    print(f"Workflow: {workflow_id}")
    print(f"Scope directory: {scope_dir}")
    print(f"Status: {'in-progress' if agents else 'completed'}")
    print(f"Active agents: {len(agents)}")
    
    if agents:
        print("\nAgents:")
        for agent in agents:
            agent_id = agent.get("id", "unknown")
            current_state = agent.get("current_state", "unknown")
            session_id = agent.get("session_id", "none")
            stack_depth = len(agent.get("stack", []))
            print(f"  - {agent_id}")
            print(f"      State: {current_state}")
            print(f"      Session: {session_id or 'none'}")
            print(f"      Stack depth: {stack_depth}")
    
    return 0


def cmd_recover(args: argparse.Namespace) -> int:
    """List in-progress workflows that can be resumed."""
    state_dir = args.state_dir
    
    in_progress = recover_workflows(state_dir=state_dir)
    
    if not in_progress:
        print("No in-progress workflows found.")
        return 0
    
    print(f"In-progress workflows ({len(in_progress)}):\n")
    for wf_id in sorted(in_progress):
        print(f"  {wf_id}")
    
    print(f"\nResume with: raymond --resume <workflow_id>")
    return 0


def create_parser() -> argparse.ArgumentParser:
    """Create the argument parser."""
    parser = argparse.ArgumentParser(
        prog="raymond",
        description="Multi-agent orchestrator for Claude Code workflows",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  raymond workflow.md                  Start a new workflow
  raymond workflow.md --budget 5.0     Start with $5 budget
  raymond workflow.md --input "data"   Start with initial input for {{result}}
  raymond --list                       List all workflows
  raymond --resume <id>                Resume an existing workflow
  raymond --status <id>                Show workflow status
""",
    )
    
    # Positional argument for starting workflows (optional)
    parser.add_argument(
        "initial_file",
        nargs="?",
        default=None,
        metavar="FILE",
        help="Path to initial prompt file to start a new workflow",
    )
    
    # Management commands (mutually exclusive)
    mgmt_group = parser.add_argument_group("management commands")
    mgmt_mutex = mgmt_group.add_mutually_exclusive_group()
    mgmt_mutex.add_argument(
        "--list",
        action="store_true",
        help="List all workflows",
    )
    mgmt_mutex.add_argument(
        "--status",
        metavar="ID",
        help="Show status of a workflow",
    )
    mgmt_mutex.add_argument(
        "--resume",
        metavar="ID",
        help="Resume an existing workflow",
    )
    mgmt_mutex.add_argument(
        "--recover",
        action="store_true",
        help="List in-progress workflows that can be resumed",
    )
    
    # Start options (only applicable when starting a workflow)
    start_group = parser.add_argument_group("start options (used when starting a workflow)")
    start_group.add_argument(
        "--workflow-id",
        dest="workflow_id",
        metavar="ID",
        default=None,
        help="Custom workflow identifier (auto-generated if not provided)",
    )
    start_group.add_argument(
        "--no-run",
        dest="no_run",
        action="store_true",
        help="Create workflow without running it",
    )
    start_group.add_argument(
        "--budget",
        type=positive_float,
        metavar="USD",
        default=None,
        help="Cost budget limit in USD (default: 10.00)",
    )
    start_group.add_argument(
        "--input",
        dest="initial_input",
        metavar="TEXT",
        default=None,
        help="Initial input passed to first state as {{result}} template variable",
    )
    
    # Runtime options (applicable to start and resume)
    runtime_group = parser.add_argument_group("runtime options")
    runtime_group.add_argument(
        "--no-debug",
        dest="no_debug",
        action="store_true",
        help="Disable debug mode (debug mode is enabled by default)",
    )
    runtime_group.add_argument(
        "--model",
        choices=["opus", "sonnet", "haiku"],
        default=None,
        help="Default model for Claude Code (can be overridden by prompt frontmatter)",
    )
    runtime_group.add_argument(
        "--timeout",
        type=positive_float_or_zero,
        metavar="SEC",
        default=None,
        help="Timeout per Claude Code invocation in seconds (default: 600, 0=none)",
    )
    runtime_group.add_argument(
        "--dangerously-skip-permissions",
        dest="dangerously_skip_permissions",
        action="store_true",
        help="Pass --dangerously-skip-permissions to Claude instead of --permission-mode acceptEdits. "
             "WARNING: This allows Claude to execute any action without prompting for permission.",
    )
    runtime_group.add_argument(
        "--quiet",
        action="store_true",
        help="Suppress progress messages and tool invocations in console output. "
             "Still shows state transitions, errors, costs, and results.",
    )
    # Global options
    global_group = parser.add_argument_group("global options")
    global_group.add_argument(
        "-v", "--verbose",
        action="store_true",
        help="Enable verbose logging",
    )
    global_group.add_argument(
        "--state-dir",
        metavar="DIR",
        default=None,
        help="Custom state directory (default: .raymond/state)",
    )
    global_group.add_argument(
        "--init-config",
        action="store_true",
        help="Generate a new .raymond/config.toml file with all options commented out",
    )
    
    return parser


def main() -> int:
    """Main entry point."""
    parser = create_parser()
    args = parser.parse_args()
    
    # Handle --init-config command (early exit, doesn't need config loading)
    if args.init_config:
        return init_config()
    
    # Determine which mode we're in BEFORE config merge
    # (so we check CLI-provided values, not config-merged values)
    has_file = args.initial_file is not None
    has_mgmt_cmd = args.list or args.status or args.resume or args.recover
    has_start_opts = args.workflow_id or args.no_run or args.budget is not None or args.initial_input is not None
    
    # Validate: can't have both file and management command
    if has_file and has_mgmt_cmd:
        print("Error: Cannot specify a file with management commands (--list, --status, --resume, --recover)", file=sys.stderr)
        return 1
    
    # Validate: start options require a file
    if has_start_opts and not has_file:
        if has_mgmt_cmd:
            print("Error: --workflow-id, --no-run, --budget, and --input are only valid when starting a workflow", file=sys.stderr)
        else:
            print("Error: --workflow-id, --no-run, --budget, and --input require a FILE argument", file=sys.stderr)
        return 1
    
    # Load and merge configuration file (if present)
    try:
        config = load_config()
        args = merge_config_and_args(config, args)
    except ConfigError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1
    
    # Dispatch to appropriate handler
    if has_file:
        return cmd_start(args)
    elif args.list:
        return cmd_list(args)
    elif args.status:
        return cmd_status(args)
    elif args.resume:
        return cmd_resume(args)
    elif args.recover:
        return cmd_recover(args)
    else:
        # No arguments - show help
        parser.print_help()
        return 0


if __name__ == "__main__":
    sys.exit(main())
