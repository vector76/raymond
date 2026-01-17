"""Command-line interface for the Raymond orchestrator."""

import argparse
import asyncio
import logging
import re
import sys
from pathlib import Path
from typing import Optional

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
    """Configure logging for the CLI."""
    level = logging.DEBUG if verbose else logging.INFO
    logging.basicConfig(
        level=level,
        format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )


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
        print(f"Error: Workflow '{workflow_id}' already exists. Use 'resume' to continue it.", file=sys.stderr)
        return 1
    
    # Get budget from args or use default
    budget_usd = args.budget if hasattr(args, 'budget') and args.budget is not None else 1.0
    
    # Create and write initial state
    state = create_initial_state(workflow_id, scope_dir, initial_state, budget_usd=budget_usd)
    write_state(workflow_id, state, state_dir=state_dir)
    
    print(f"Created workflow '{workflow_id}'")
    print(f"  Scope directory: {scope_dir}")
    print(f"  Initial state: {initial_state}")
    
    if not args.no_run:
        print("\nStarting orchestrator...")
        debug = getattr(args, 'debug', False)
        return cmd_run_workflow(workflow_id, state_dir, args.verbose, debug)
    
    print(f"\nRun with: raymond run {workflow_id}")
    return 0


def cmd_run(args: argparse.Namespace) -> int:
    """Run/resume a workflow."""
    # Validate workflow_id
    error = validate_workflow_id(args.workflow_id)
    if error:
        print(f"Error: {error}", file=sys.stderr)
        return 1
    debug = getattr(args, 'debug', False)
    return cmd_run_workflow(args.workflow_id, args.state_dir, args.verbose, debug)


def cmd_run_workflow(workflow_id: str, state_dir: Optional[str], verbose: bool, debug: bool = False) -> int:
    """Run a workflow by ID."""
    setup_logging(verbose)
    
    try:
        asyncio.run(run_all_agents(workflow_id, state_dir=state_dir, debug=debug))
        print(f"\nWorkflow '{workflow_id}' completed.")
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
    workflow_id = args.workflow_id
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
    
    print(f"\nResume with: raymond run <workflow_id>")
    return 0


def create_parser() -> argparse.ArgumentParser:
    """Create the argument parser."""
    parser = argparse.ArgumentParser(
        prog="raymond",
        description="Multi-agent orchestrator for Claude Code workflows",
    )
    parser.add_argument(
        "-v", "--verbose",
        action="store_true",
        help="Enable verbose logging",
    )
    parser.add_argument(
        "--state-dir",
        default=None,
        help="Custom state directory (default: .raymond/state)",
    )
    
    subparsers = parser.add_subparsers(dest="command", help="Available commands")
    
    # start command
    start_parser = subparsers.add_parser(
        "start",
        help="Start a new workflow",
    )
    start_parser.add_argument(
        "initial_file",
        help="Path to initial prompt file (e.g., workflows/test/START.md). The containing directory becomes the scope directory.",
    )
    start_parser.add_argument(
        "--workflow-id",
        dest="workflow_id",
        default=None,
        help="Unique identifier for the workflow (auto-generated if not provided)",
    )
    start_parser.add_argument(
        "--no-run",
        dest="no_run",
        action="store_true",
        help="Create the workflow without running it (default: runs immediately)",
    )
    start_parser.add_argument(
        "--budget",
        dest="budget",
        type=float,
        default=None,
        help="Cost budget limit in USD (default: 1.00). Workflow terminates when total cost exceeds this limit.",
    )
    start_parser.add_argument(
        "--debug",
        action="store_true",
        help="Enable debug mode: save Claude Code outputs and state transitions to .raymond/debug/",
    )
    start_parser.set_defaults(func=cmd_start)
    
    # run command
    run_parser = subparsers.add_parser(
        "run",
        help="Run/resume a workflow",
    )
    run_parser.add_argument(
        "workflow_id",
        help="Workflow identifier to run",
    )
    run_parser.add_argument(
        "--debug",
        action="store_true",
        help="Enable debug mode: save Claude Code outputs and state transitions to .raymond/debug/",
    )
    run_parser.set_defaults(func=cmd_run)
    
    # list command
    list_parser = subparsers.add_parser(
        "list",
        help="List all workflows",
    )
    list_parser.set_defaults(func=cmd_list)
    
    # status command
    status_parser = subparsers.add_parser(
        "status",
        help="Show status of a workflow",
    )
    status_parser.add_argument(
        "workflow_id",
        help="Workflow identifier",
    )
    status_parser.set_defaults(func=cmd_status)
    
    # recover command
    recover_parser = subparsers.add_parser(
        "recover",
        help="List in-progress workflows that can be resumed",
    )
    recover_parser.set_defaults(func=cmd_recover)
    
    return parser


def main() -> int:
    """Main entry point."""
    parser = create_parser()
    args = parser.parse_args()
    
    if args.command is None:
        parser.print_help()
        return 0
    
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
