import json
from pathlib import Path
from typing import Dict, List, Any


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
    """
    state_path = get_state_dir(state_dir) / f"{workflow_id}.json"
    
    if not state_path.exists():
        raise FileNotFoundError(f"State file not found: {state_path}")
    
    with open(state_path, 'r', encoding='utf-8') as f:
        return json.load(f)


def write_state(workflow_id: str, state: Dict[str, Any], state_dir: str = None) -> None:
    """Write workflow state to JSON file.
    
    Args:
        workflow_id: Unique identifier for the workflow
        state: Dictionary containing workflow state
        state_dir: Optional custom state directory. If None, uses default.
    """
    state_path = get_state_dir(state_dir) / f"{workflow_id}.json"
    
    # Create directory if it doesn't exist
    state_path.parent.mkdir(parents=True, exist_ok=True)
    
    with open(state_path, 'w', encoding='utf-8') as f:
        json.dump(state, f, indent=2)


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
