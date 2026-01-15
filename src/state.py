import json
import os
import tempfile
from pathlib import Path
from typing import Dict, List, Any, Union


class StateFileError(Exception):
    """Raised when state file operations fail."""
    pass


def get_state_dir(state_dir: str = None) -> Path:
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


def read_state(workflow_id: str, state_dir: str = None) -> Dict[str, Any]:
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


def write_state(workflow_id: str, state: Dict[str, Any], state_dir: str = None) -> None:
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


def list_workflows(state_dir: str = None) -> List[str]:
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


def create_initial_state(workflow_id: str, scope_dir: str, initial_state: str) -> Dict[str, Any]:
    """Create initial state structure for a new workflow.
    
    Args:
        workflow_id: Unique identifier for the workflow
        scope_dir: Directory containing prompt files for this workflow
        initial_state: Initial prompt filename to start from
        
    Returns:
        Dictionary containing initial workflow state
    """
    return {
        "workflow_id": workflow_id,
        "scope_dir": scope_dir,
        "agents": [
            {
                "id": "main",
                "current_state": initial_state,
                "session_id": None,
                "stack": []
            }
        ]
    }


def recover_workflows(state_dir: str = None) -> List[str]:
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
                workflow_id = file_path.stem
                state = read_state(workflow_id, state_dir=state_dir)
                
                # Check if workflow has active agents
                agents = state.get("agents", [])
                if agents:  # At least one agent means in-progress
                    in_progress.append(workflow_id)
            except (FileNotFoundError, StateFileError, json.JSONDecodeError):
                # Skip malformed or unreadable state files
                continue
    
    return in_progress
