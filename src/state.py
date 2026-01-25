import json
import os
import tempfile
from datetime import datetime
from pathlib import Path
from typing import Dict, List, Any, Optional


class StateFileError(Exception):
    """Raised when state file operations fail."""
    pass


def get_state_dir(state_dir: Optional[str] = None) -> Path:
    """Get the state directory path.
    
    Args:
        state_dir: Optional custom state directory. If None, uses default.
        
    Returns:
        Path object for the state directory
    """
    if state_dir is None:
        # Default: .raymond/state in current working directory
        return Path(".raymond") / "state"
    return Path(state_dir)


def read_state(workflow_id: str, state_dir: Optional[str] = None) -> Dict[str, Any]:
    """Read workflow state from JSON file.
    
    Args:
        workflow_id: Unique identifier for the workflow
        state_dir: Optional custom state directory. If None, uses default.
        
    Returns:
        Dictionary containing workflow state
        
    Raises:
        FileNotFoundError: If the state file does not exist
        StateFileError: If the state file is malformed (invalid JSON)
    """
    state_path = get_state_dir(state_dir) / f"{workflow_id}.json"
    
    if not state_path.exists():
        raise FileNotFoundError(f"State file not found: {state_path}")
    
    try:
        with open(state_path, 'r', encoding='utf-8') as f:
            return json.load(f)
    except json.JSONDecodeError as e:
        raise StateFileError(
            f"Malformed state file {state_path}: {e}"
        ) from e


def delete_state(workflow_id: str, state_dir: Optional[str] = None) -> None:
    """Delete a workflow state file.
    
    Args:
        workflow_id: Unique identifier for the workflow
        state_dir: Optional custom state directory. If None, uses default.
        
    Raises:
        OSError: If the file cannot be deleted
    """
    state_path = get_state_dir(state_dir) / f"{workflow_id}.json"
    
    if state_path.exists():
        state_path.unlink()


def write_state(workflow_id: str, state: Dict[str, Any], state_dir: Optional[str] = None) -> None:
    """Write workflow state to JSON file atomically.
    
    Uses atomic write pattern: write to temp file, then rename. This prevents
    state corruption if the process crashes mid-write.
    
    Args:
        workflow_id: Unique identifier for the workflow
        state: Dictionary containing workflow state
        state_dir: Optional custom state directory. If None, uses default.
    """
    state_path = get_state_dir(state_dir) / f"{workflow_id}.json"
    
    # Create directory if it doesn't exist
    state_path.parent.mkdir(parents=True, exist_ok=True)
    
    # Write to temp file in same directory, then rename (atomic on most filesystems)
    fd, tmp_path = tempfile.mkstemp(
        suffix='.tmp',
        prefix=f'{workflow_id}_',
        dir=state_path.parent
    )
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as f:
            json.dump(state, f, indent=2)
        # Atomic rename
        os.replace(tmp_path, state_path)
    except Exception:
        # Clean up temp file on failure
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)
        raise


def generate_workflow_id(state_dir: Optional[str] = None) -> str:
    """Generate a unique workflow ID based on timestamp.
    
    The ID format is: workflow_YYYY-MM-DD_HH-MM-SS-ffffff
    Includes microseconds to ensure uniqueness. If a collision still occurs,
    appends a counter.
    
    Args:
        state_dir: Optional custom state directory. If None, uses default.
        
    Returns:
        A unique workflow ID string
    """
    existing = set(list_workflows(state_dir=state_dir))
    
    # Generate base ID from timestamp with microseconds
    timestamp = datetime.now().strftime("%Y-%m-%d_%H-%M-%S-%f")
    base_id = f"workflow_{timestamp}"
    
    # If collision occurs, append counter
    workflow_id = base_id
    counter = 1
    while workflow_id in existing:
        workflow_id = f"{base_id}_{counter}"
        counter += 1
    
    return workflow_id


def list_workflows(state_dir: Optional[str] = None) -> List[str]:
    """List all workflow IDs from existing state files.
    
    Args:
        state_dir: Optional custom state directory. If None, uses default.
        
    Returns:
        List of workflow IDs (filenames without .json extension)
    """
    state_path = get_state_dir(state_dir)
    
    if not state_path.exists():
        return []
    
    workflows = []
    for file_path in state_path.iterdir():
        if file_path.is_file() and file_path.suffix == ".json":
            # Extract workflow_id from filename (remove .json extension)
            workflow_id = file_path.stem
            workflows.append(workflow_id)
    
    return workflows


def create_initial_state(workflow_id: str, scope_dir: str, initial_state: str, budget_usd: float = 10.0, initial_input: Optional[str] = None) -> Dict[str, Any]:
    """Create initial state structure for a new workflow.

    Args:
        workflow_id: Unique identifier for the workflow
        scope_dir: Directory containing prompt files for this workflow
        initial_state: Initial prompt filename to start from
        budget_usd: Cost budget limit in USD (default: 10.0)
        initial_input: Optional initial input passed to first state as {{result}}

    Returns:
        Dictionary containing initial workflow state
    """
    main_agent = {
        "id": "main",
        "current_state": initial_state,
        "session_id": None,
        "stack": []
    }

    # If initial_input is provided, set it as pending_result so it gets
    # templated into {{result}} in the first state's prompt
    if initial_input is not None:
        main_agent["pending_result"] = initial_input

    return {
        "workflow_id": workflow_id,
        "scope_dir": scope_dir,
        "total_cost_usd": 0.0,
        "budget_usd": budget_usd,
        "agents": [main_agent]
    }


def recover_workflows(state_dir: Optional[str] = None) -> List[str]:
    """Find all in-progress workflows (workflows with at least one active agent).
    
    A workflow is considered "in-progress" if it has at least one agent
    in the agents array. This function scans the state directory and returns
    workflow IDs for workflows that can be resumed.
    
    Args:
        state_dir: Optional custom state directory. If None, uses default.
        
    Returns:
        List of workflow IDs that are in-progress (have active agents)
    """
    state_path = get_state_dir(state_dir)
    
    if not state_path.exists():
        return []
    
    in_progress = []
    
    for file_path in state_path.iterdir():
        if file_path.is_file() and file_path.suffix == ".json":
            try:
                # Read file directly for efficiency (avoid re-constructing path in read_state)
                with open(file_path, 'r', encoding='utf-8') as f:
                    state = json.load(f)
                
                # Check if workflow has active agents
                agents = state.get("agents", [])
                if agents:  # At least one agent means in-progress
                    in_progress.append(file_path.stem)
            except (json.JSONDecodeError, OSError):
                # Skip malformed or unreadable state files
                continue
    
    return in_progress
