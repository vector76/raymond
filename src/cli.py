"""Command-line interface for the Raymond orchestrator."""

import argparse
import asyncio
import logging
import sys
from pathlib import Path

try:
    from .orchestrator import run_all_agents
    from .state import (
        create_initial_state,
        write_state,
        read_state,
        list_workflows,
        recover_workflows,
    )
except ImportError:
    from orchestrator import run_all_agents
    from state import (
        create_initial_state,
        write_state,
        read_state,
        list_workflows,
        recover_workflows,
    )


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
    workflow_id = args.workflow_id
    scope_dir = args.scope_dir
    initial_state = args.initial_state
    state_dir = args.state_dir
    
    # Validate scope_dir exists
    if not Path(scope_dir).is_dir():
        print(f"Error: Scope directory does not exist: {scope_dir}", file=sys.stderr)
        return 1
    
    # Validate initial state file exists
    initial_path = Path(scope_dir) / initial_state
    if not initial_path.is_file():
        print(f"Error: Initial state file does not exist: {initial_path}", file=sys.stderr)
        return 1
    
    # Check if workflow already exists
    existing = list_workflows(state_dir=state_dir)
    if workflow_id in existing:
        print(f"Error: Workflow '{workflow_id}' already exists. Use 'resume' to continue it.", file=sys.stderr)
        return 1
    
    # Create and write initial state
    state = create_initial_state(workflow_id, scope_dir, initial_state)
    write_state(workflow_id, state, state_dir=state_dir)
    
    print(f"Created workflow '{workflow_id}'")
    print(f"  Scope directory: {scope_dir}")
    print(f"  Initial state: {initial_state}")
    
    if args.run:
        print("\nStarting orchestrator...")
        return cmd_run_workflow(workflow_id, state_dir, args.verbose)
    
    print(f"\nRun with: raymond run {workflow_id}")
    return 0


def cmd_run(args: argparse.Namespace) -> int:
    """Run/resume a workflow."""
    return cmd_run_workflow(args.workflow_id, args.state_dir, args.verbose)


def cmd_run_workflow(workflow_id: str, state_dir: str, verbose: bool) -> int:
    """Run a workflow by ID."""
    setup_logging(verbose)
    
    try:
        asyncio.run(run_all_agents(workflow_id, state_dir=state_dir))
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
        "workflow_id",
        help="Unique identifier for the workflow",
    )
    start_parser.add_argument(
        "scope_dir",
        help="Directory containing prompt files",
    )
    start_parser.add_argument(
        "initial_state",
        help="Initial prompt filename (e.g., START.md)",
    )
    start_parser.add_argument(
        "--run",
        action="store_true",
        help="Immediately run the workflow after creating it",
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
