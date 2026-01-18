import sys
import pytest
from pathlib import Path
from src.prompts import load_prompt, render_prompt, resolve_state


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


class TestResolveState:
    """Tests for resolve_state() function - Abstract State Name Resolution (Step 1.1)."""

    # =========================================================================
    # Cross-platform tests (1.1.1 - 1.1.6)
    # =========================================================================

    def test_resolve_state_finds_md_when_exists(self, tmp_path):
        """1.1.1: resolve_state("NEXT") finds NEXT.md when it exists."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()
        (scope_dir / "NEXT.md").write_text("# Next state")

        result = resolve_state(str(scope_dir), "NEXT")
        assert result == "NEXT.md"

    def test_resolve_state_explicit_md_extension(self, tmp_path):
        """1.1.2: resolve_state("NEXT.md") returns NEXT.md (explicit extension)."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()
        (scope_dir / "NEXT.md").write_text("# Next state")

        result = resolve_state(str(scope_dir), "NEXT.md")
        assert result == "NEXT.md"

    def test_resolve_state_raises_when_no_file_exists(self, tmp_path):
        """1.1.3: resolve_state("NEXT") raises when no matching file exists."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()
        # No files created

        with pytest.raises(FileNotFoundError):
            resolve_state(str(scope_dir), "NEXT")

    def test_resolve_state_explicit_md_raises_when_missing(self, tmp_path):
        """1.1.4: resolve_state("NEXT.md") raises when NEXT.md doesn't exist (explicit, no fallback)."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()
        # Create .sh and .bat but NOT .md
        (scope_dir / "NEXT.sh").write_text("echo goto")
        (scope_dir / "NEXT.bat").write_text("echo goto")

        with pytest.raises(FileNotFoundError):
            resolve_state(str(scope_dir), "NEXT.md")

    def test_resolve_state_respects_scope_dir(self, tmp_path):
        """1.1.5: resolution respects scope_dir parameter."""
        # Create two directories with different files
        dir_a = tmp_path / "dir_a"
        dir_b = tmp_path / "dir_b"
        dir_a.mkdir()
        dir_b.mkdir()

        (dir_a / "STATE.md").write_text("# State A")
        (dir_b / "OTHER.md").write_text("# Other B")

        # STATE.md exists in dir_a but not dir_b
        result = resolve_state(str(dir_a), "STATE")
        assert result == "STATE.md"

        with pytest.raises(FileNotFoundError):
            resolve_state(str(dir_b), "STATE")

    def test_resolve_state_with_both_sh_and_bat(self, tmp_path):
        """1.1.6: resolve_state("NEXT") succeeds when .sh and .bat both exist (uses platform-appropriate)."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()
        # Create both script types (but no .md)
        (scope_dir / "NEXT.sh").write_text("echo goto")
        (scope_dir / "NEXT.bat").write_text("echo goto")

        result = resolve_state(str(scope_dir), "NEXT")
        
        # Should return the platform-appropriate script
        if sys.platform.startswith('win'):
            assert result == "NEXT.bat"
        else:
            assert result == "NEXT.sh"

    def test_resolve_state_raises_for_path_separator(self, tmp_path):
        """resolve_state() raises if state_name contains path separators (defense in depth)."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()

        with pytest.raises(ValueError, match="path separator"):
            resolve_state(str(scope_dir), "../SECRET")

        with pytest.raises(ValueError, match="path separator"):
            resolve_state(str(scope_dir), "subdir/STATE")

        with pytest.raises(ValueError, match="path separator"):
            resolve_state(str(scope_dir), "C:\\STATE")

    # =========================================================================
    # Unix-only tests (1.1.7 - 1.1.11)
    # TODO: Implement these tests
    # =========================================================================

    # 1.1.7: resolve_state("NEXT") finds NEXT.sh when .md doesn't exist
    # 1.1.8: resolve_state("NEXT.sh") returns NEXT.sh (explicit extension)
    # 1.1.9: resolve_state("NEXT") raises when only .bat exists (no .md or .sh)
    # 1.1.10: resolve_state("NEXT") raises when .md and .sh both exist (ambiguous)
    # 1.1.11: resolve_state("NEXT.bat") raises (wrong platform)

    # =========================================================================
    # Windows-only tests (1.1.12 - 1.1.16)
    # =========================================================================

    @pytest.mark.windows
    def test_resolve_state_finds_bat_when_md_missing(self, tmp_path):
        """1.1.12: resolve_state("NEXT") finds NEXT.bat when .md doesn't exist."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()
        (scope_dir / "NEXT.bat").write_text("echo goto")

        result = resolve_state(str(scope_dir), "NEXT")
        assert result == "NEXT.bat"

    @pytest.mark.windows
    def test_resolve_state_explicit_bat_extension(self, tmp_path):
        """1.1.13: resolve_state("NEXT.bat") returns NEXT.bat (explicit extension)."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()
        (scope_dir / "NEXT.bat").write_text("echo goto")

        result = resolve_state(str(scope_dir), "NEXT.bat")
        assert result == "NEXT.bat"

    @pytest.mark.windows
    def test_resolve_state_raises_when_only_sh_exists(self, tmp_path):
        """1.1.14: resolve_state("NEXT") raises when only .sh exists (no .md or .bat)."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()
        (scope_dir / "NEXT.sh").write_text("echo goto")

        with pytest.raises(FileNotFoundError):
            resolve_state(str(scope_dir), "NEXT")

    @pytest.mark.windows
    def test_resolve_state_raises_when_md_and_bat_both_exist(self, tmp_path):
        """1.1.15: resolve_state("NEXT") raises when .md and .bat both exist (ambiguous)."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()
        (scope_dir / "NEXT.md").write_text("# Next state")
        (scope_dir / "NEXT.bat").write_text("echo goto")

        with pytest.raises(ValueError, match="[Aa]mbiguous"):
            resolve_state(str(scope_dir), "NEXT")

    @pytest.mark.windows
    def test_resolve_state_explicit_sh_raises_wrong_platform(self, tmp_path):
        """1.1.16: resolve_state("NEXT.sh") raises (wrong platform)."""
        scope_dir = tmp_path / "workflows"
        scope_dir.mkdir()
        (scope_dir / "NEXT.sh").write_text("echo goto")

        with pytest.raises(ValueError, match="[Pp]latform|[Ww]indows|[Uu]nix"):
            resolve_state(str(scope_dir), "NEXT.sh")
