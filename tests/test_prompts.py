import pytest
from pathlib import Path
from src.prompts import load_prompt, render_prompt


class TestLoadPrompt:
    """Tests for load_prompt() function."""

    def test_load_prompt_returns_file_contents(self, tmp_path):
        """Test that load_prompt() returns file contents."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        
        prompt_file = scope_dir / "START.md"
        expected_content = "# Start\n\nThis is the start prompt."
        prompt_file.write_text(expected_content)
        
        content, policy = load_prompt(str(scope_dir), "START.md")
        assert content == expected_content
        assert policy is None  # No frontmatter

    def test_load_prompt_raises_for_missing_file(self, tmp_path):
        """Test that load_prompt() raises for missing file."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        
        with pytest.raises(FileNotFoundError):
            load_prompt(str(scope_dir), "NONEXISTENT.md")

    def test_load_prompt_raises_for_path_separator(self, tmp_path):
        """Test that load_prompt() raises if filename contains path separators."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        
        # Defense in depth: reject filenames with path separators
        with pytest.raises(ValueError, match="Filename.*contains.*separator"):
            load_prompt(str(scope_dir), "../SECRET.md")
        
        with pytest.raises(ValueError, match="Filename.*contains.*separator"):
            load_prompt(str(scope_dir), "subdir/file.md")
        
        with pytest.raises(ValueError, match="Filename.*contains.*separator"):
            load_prompt(str(scope_dir), "C:\\file.md")


class TestRenderPrompt:
    """Tests for render_prompt() function."""

    def test_render_prompt_replaces_placeholder(self):
        """Test that render_prompt() replaces {{key}} with value."""
        template = "Hello {{name}}, welcome to {{place}}!"
        variables = {"name": "Alice", "place": "Raymond"}
        
        result = render_prompt(template, variables)
        assert result == "Hello Alice, welcome to Raymond!"

    def test_render_prompt_multiple_placeholders(self):
        """Test that multiple placeholders in same template are replaced."""
        template = "{{greeting}} {{name}}, your task is {{task}}."
        variables = {"greeting": "Hi", "name": "Bob", "task": "testing"}
        
        result = render_prompt(template, variables)
        assert result == "Hi Bob, your task is testing."

    def test_render_prompt_missing_key_leaves_placeholder(self):
        """Test that missing key in variables leaves placeholder unchanged."""
        template = "Hello {{name}}, status: {{status}}"
        variables = {"name": "Charlie"}
        
        result = render_prompt(template, variables)
        assert result == "Hello Charlie, status: {{status}}"

    def test_render_prompt_result_placeholder(self):
        """Test that {{result}} placeholder is replaced (common case)."""
        template = "Previous result: {{result}}\n\nContinue with next step."
        variables = {"result": "Task completed successfully"}
        
        result = render_prompt(template, variables)
        assert "Task completed successfully" in result
        assert "{{result}}" not in result

    def test_render_prompt_no_placeholders(self):
        """Test that template with no placeholders is unchanged."""
        template = "This is a plain template with no variables."
        variables = {"key": "value"}
        
        result = render_prompt(template, variables)
        assert result == template

    def test_render_prompt_empty_variables(self):
        """Test that empty variables dict leaves all placeholders."""
        template = "Hello {{name}}, from {{place}}"
        variables = {}
        
        result = render_prompt(template, variables)
        assert result == template

    def test_render_prompt_multiple_same_placeholder(self):
        """Test that same placeholder appearing multiple times is replaced."""
        template = "{{name}} says hello. {{name}} is happy."
        variables = {"name": "David"}
        
        result = render_prompt(template, variables)
        assert result == "David says hello. David is happy."

    def test_render_prompt_nested_braces(self):
        """Test that nested braces are handled correctly."""
        template = "Value: {{value}}, JSON: {{json}}"
        variables = {"value": "test", "json": '{"key": "value"}'}
        
        result = render_prompt(template, variables)
        assert "test" in result
        assert '{"key": "value"}' in result
