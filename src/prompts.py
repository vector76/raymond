from pathlib import Path
from typing import Dict


def load_prompt(scope_dir: str, filename: str) -> str:
    """Load a prompt file from the scope directory.
    
    Args:
        scope_dir: Directory containing prompt files
        filename: Name of the prompt file to load
        
    Returns:
        Contents of the prompt file as a string
        
    Raises:
        FileNotFoundError: If the prompt file does not exist
        ValueError: If filename contains path separators (defense in depth)
    """
    # Defense in depth: validate filename doesn't contain path separators
    if "/" in filename or "\\" in filename:
        raise ValueError(
            f"Filename '{filename}' contains path separator. "
            "Filenames must not contain / or \\"
        )
    
    prompt_path = Path(scope_dir) / filename
    
    if not prompt_path.exists():
        raise FileNotFoundError(f"Prompt file not found: {prompt_path}")
    
    with open(prompt_path, 'r', encoding='utf-8') as f:
        return f.read()


def render_prompt(template: str, variables: Dict[str, str]) -> str:
    """Replace {{key}} placeholders with values from variables dict.
    
    Args:
        template: Template string with {{key}} placeholders
        variables: Dictionary mapping placeholder keys to values
        
    Returns:
        Template with placeholders replaced. Missing keys leave placeholders unchanged.
    """
    result = template
    
    # Replace each variable in the template
    for key, value in variables.items():
        placeholder = f"{{{{{key}}}}}"
        result = result.replace(placeholder, value)
    
    return result
