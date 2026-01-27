"""Tests for configuration file management."""

import pytest
import sys
from pathlib import Path
from unittest.mock import patch
from src.config import (
    find_project_root,
    find_raymond_dir,
    find_config_file,
    load_config,
    validate_config,
    merge_config_and_args,
    init_config,
    ConfigError,
)


class TestFindProjectRoot:
    """Tests for find_project_root()."""

    def test_finds_git_directory(self, tmp_path):
        """Test that find_project_root finds .git directory."""
        # Create project structure
        project_root = tmp_path / "project"
        project_root.mkdir()
        (project_root / ".git").mkdir()
        
        subdir = project_root / "subdir" / "nested"
        subdir.mkdir(parents=True)
        
        # Should find project root from subdirectory
        result = find_project_root(subdir)
        assert result == project_root.resolve()

    def test_returns_cwd_if_no_git(self, tmp_path):
        """Test that find_project_root returns cwd if no .git found."""
        subdir = tmp_path / "subdir" / "nested"
        subdir.mkdir(parents=True)
        
        result = find_project_root(subdir)
        assert result == subdir.resolve()

    def test_stops_at_filesystem_root(self, tmp_path):
        """Test that search stops at filesystem root."""
        # Create a deep directory structure without .git
        deep_dir = tmp_path
        for i in range(10):
            deep_dir = deep_dir / f"level{i}"
        deep_dir.mkdir(parents=True)
        
        result = find_project_root(deep_dir)
        # Should return the original cwd (or filesystem root if we hit it)
        assert result.exists()


class TestFindRaymondDir:
    """Tests for find_raymond_dir()."""

    def test_finds_existing_raymond_dir(self, tmp_path):
        """Test that find_raymond_dir finds existing .raymond directory."""
        raymond_dir = tmp_path / ".raymond"
        raymond_dir.mkdir()
        
        subdir = tmp_path / "subdir"
        subdir.mkdir()
        
        result = find_raymond_dir(subdir)
        assert result == raymond_dir.resolve()

    def test_searches_upward_until_git(self, tmp_path):
        """Test that search stops at .git directory."""
        project_root = tmp_path / "project"
        project_root.mkdir()
        (project_root / ".git").mkdir()
        
        # Create .raymond in subdirectory
        subdir = project_root / "subdir"
        subdir.mkdir()
        raymond_dir = subdir / ".raymond"
        raymond_dir.mkdir()
        
        nested = subdir / "nested"
        nested.mkdir()
        
        # Should find .raymond in subdir (before reaching .git)
        result = find_raymond_dir(nested)
        assert result == raymond_dir.resolve()

    def test_creates_raymond_dir_at_project_root(self, tmp_path):
        """Test that create_if_missing creates .raymond at project root."""
        project_root = tmp_path / "project"
        project_root.mkdir()
        (project_root / ".git").mkdir()
        
        subdir = project_root / "subdir" / "nested"
        subdir.mkdir(parents=True)
        
        result = find_raymond_dir(subdir, create_if_missing=True)
        assert result == (project_root / ".raymond").resolve()
        assert result.exists()
        assert result.is_dir()

    def test_creates_raymond_dir_at_cwd_if_no_git(self, tmp_path):
        """Test that create_if_missing uses cwd if no .git found."""
        subdir = tmp_path / "subdir" / "nested"
        subdir.mkdir(parents=True)
        
        result = find_raymond_dir(subdir, create_if_missing=True)
        assert result == (subdir / ".raymond").resolve()
        assert result.exists()
        assert result.is_dir()

    def test_returns_none_if_not_found_and_not_creating(self, tmp_path):
        """Test that returns None if .raymond not found and create_if_missing=False."""
        subdir = tmp_path / "subdir"
        subdir.mkdir()
        
        result = find_raymond_dir(subdir, create_if_missing=False)
        assert result is None

    def test_ignores_file_named_raymond(self, tmp_path):
        """Test that continues searching if .raymond exists as a file, not directory."""
        # Create .raymond as a file
        raymond_file = tmp_path / ".raymond"
        raymond_file.write_text("not a directory")
        
        # Create actual .raymond directory in parent
        parent = tmp_path.parent
        if parent.exists():
            raymond_dir = parent / ".raymond"
            raymond_dir.mkdir(exist_ok=True)
        
        # Should continue searching (or return None if not found)
        result = find_raymond_dir(tmp_path)
        # Result depends on whether parent has .raymond, but should not return the file
        if result is not None:
            assert result.is_dir()


class TestFindConfigFile:
    """Tests for find_config_file()."""

    def test_finds_config_file_in_raymond_dir(self, tmp_path):
        """Test that find_config_file finds config.toml in .raymond directory."""
        raymond_dir = tmp_path / ".raymond"
        raymond_dir.mkdir()
        config_file = raymond_dir / "config.toml"
        config_file.write_text("[raymond]\nbudget = 50.0\n")
        
        result = find_config_file(tmp_path)
        assert result == config_file.resolve()

    def test_returns_none_if_no_raymond_dir(self, tmp_path):
        """Test that returns None if .raymond directory doesn't exist."""
        result = find_config_file(tmp_path)
        assert result is None

    def test_returns_none_if_no_config_file(self, tmp_path):
        """Test that returns None if .raymond exists but config.toml doesn't."""
        raymond_dir = tmp_path / ".raymond"
        raymond_dir.mkdir()
        
        result = find_config_file(tmp_path)
        assert result is None


class TestLoadConfig:
    """Tests for load_config()."""

    def test_loads_valid_config(self, tmp_path):
        """Test that load_config loads valid TOML config."""
        raymond_dir = tmp_path / ".raymond"
        raymond_dir.mkdir()
        config_file = raymond_dir / "config.toml"
        config_file.write_text(
            "[raymond]\n"
            "budget = 50.0\n"
            "dangerously_skip_permissions = true\n"
            "model = \"sonnet\"\n"
            "timeout = 300.0\n"
            "no_debug = true\n"
            "verbose = true\n"
        )
        
        config = load_config(tmp_path)
        assert config["budget"] == 50.0
        assert config["dangerously_skip_permissions"] is True
        assert config["model"] == "sonnet"
        assert config["timeout"] == 300.0
        assert config["no_debug"] is True
        assert config["verbose"] is True

    def test_returns_empty_dict_if_no_config(self, tmp_path):
        """Test that load_config returns empty dict if config file doesn't exist."""
        config = load_config(tmp_path)
        assert config == {}

    def test_returns_empty_dict_if_missing_raymond_section(self, tmp_path):
        """Test that returns empty dict if [raymond] section is missing."""
        raymond_dir = tmp_path / ".raymond"
        raymond_dir.mkdir()
        config_file = raymond_dir / "config.toml"
        config_file.write_text("[other]\nkey = \"value\"\n")
        
        config = load_config(tmp_path)
        assert config == {}

    def test_raises_on_invalid_toml(self, tmp_path):
        """Test that load_config raises ConfigError on invalid TOML."""
        raymond_dir = tmp_path / ".raymond"
        raymond_dir.mkdir()
        config_file = raymond_dir / "config.toml"
        config_file.write_text("[raymond]\nbudget = invalid syntax\n")
        
        with pytest.raises(ConfigError) as exc_info:
            load_config(tmp_path)
        assert "Failed to parse" in str(exc_info.value)
        assert "config.toml" in str(exc_info.value)

    def test_raises_on_file_read_error(self, tmp_path):
        """Test that raises ConfigError on file read errors."""
        raymond_dir = tmp_path / ".raymond"
        raymond_dir.mkdir()
        config_file = raymond_dir / "config.toml"
        config_file.write_text("[raymond]\nbudget = 50.0\n")
        
        # Make file unreadable (on Unix)
        if sys.platform != "win32":
            config_file.chmod(0o000)
            try:
                with pytest.raises(ConfigError) as exc_info:
                    load_config(tmp_path)
                assert "Failed to read" in str(exc_info.value)
            finally:
                config_file.chmod(0o644)

    def test_ignores_unknown_keys(self, tmp_path):
        """Test that unknown keys in [raymond] section are ignored."""
        raymond_dir = tmp_path / ".raymond"
        raymond_dir.mkdir()
        config_file = raymond_dir / "config.toml"
        config_file.write_text(
            "[raymond]\n"
            "budget = 50.0\n"
            "unknown_key = \"value\"\n"
            "another_unknown = 123\n"
        )
        
        config = load_config(tmp_path)
        assert config["budget"] == 50.0
        assert "unknown_key" not in config
        assert "another_unknown" not in config


class TestValidateConfig:
    """Tests for validate_config()."""

    def test_validates_budget_type(self, tmp_path):
        """Test that validate_config checks budget is a number."""
        config_file = tmp_path / "config.toml"
        config = {"budget": "50.0"}  # String instead of number
        
        with pytest.raises(ConfigError) as exc_info:
            validate_config(config, config_file)
        assert "budget" in str(exc_info.value)
        assert "expected number" in str(exc_info.value)

    def test_validates_budget_positive(self, tmp_path):
        """Test that validate_config checks budget is positive."""
        config_file = tmp_path / "config.toml"
        config = {"budget": -10.0}
        
        with pytest.raises(ConfigError) as exc_info:
            validate_config(config, config_file)
        assert "budget" in str(exc_info.value)
        assert "must be positive" in str(exc_info.value)

    def test_validates_timeout_type(self, tmp_path):
        """Test that validate_config checks timeout is a number."""
        config_file = tmp_path / "config.toml"
        config = {"timeout": "600"}  # String instead of number
        
        with pytest.raises(ConfigError) as exc_info:
            validate_config(config, config_file)
        assert "timeout" in str(exc_info.value)
        assert "expected number" in str(exc_info.value)

    def test_validates_timeout_non_negative(self, tmp_path):
        """Test that validate_config checks timeout is non-negative."""
        config_file = tmp_path / "config.toml"
        config = {"timeout": -1.0}
        
        with pytest.raises(ConfigError) as exc_info:
            validate_config(config, config_file)
        assert "timeout" in str(exc_info.value)
        assert "must be non-negative" in str(exc_info.value)

    def test_validates_boolean_flags(self, tmp_path):
        """Test that validate_config checks boolean flags are booleans."""
        config_file = tmp_path / "config.toml"
        
        for flag in ["dangerously_skip_permissions", "no_debug", "verbose"]:
            config = {flag: "true"}  # String instead of boolean
            with pytest.raises(ConfigError) as exc_info:
                validate_config(config, config_file)
            assert flag in str(exc_info.value)
            assert "expected boolean" in str(exc_info.value)

    def test_validates_model_type(self, tmp_path):
        """Test that validate_config checks model is a string."""
        config_file = tmp_path / "config.toml"
        config = {"model": 123}  # Number instead of string
        
        with pytest.raises(ConfigError) as exc_info:
            validate_config(config, config_file)
        assert "model" in str(exc_info.value)
        assert "expected string" in str(exc_info.value)

    def test_validates_model_choices(self, tmp_path):
        """Test that validate_config checks model is one of allowed choices."""
        config_file = tmp_path / "config.toml"
        config = {"model": "invalid"}
        
        with pytest.raises(ConfigError) as exc_info:
            validate_config(config, config_file)
        assert "model" in str(exc_info.value)
        assert "must be one of" in str(exc_info.value)

    def test_validates_all_valid_values(self, tmp_path):
        """Test that validate_config passes for all valid values."""
        config_file = tmp_path / "config.toml"
        config = {
            "budget": 50.0,
            "timeout": 300.0,
            "dangerously_skip_permissions": True,
            "no_debug": False,
            "verbose": True,
            "model": "opus",
        }

        # Should not raise
        validate_config(config, config_file)


class TestMergeConfigAndArgs:
    """Tests for merge_config_and_args()."""

    def test_cli_args_override_config(self):
        """Test that CLI args take precedence over config values."""
        import argparse
        
        config = {"budget": 50.0, "model": "sonnet", "verbose": True}
        args = argparse.Namespace(
            budget=100.0,  # CLI overrides config
            model="haiku",  # CLI overrides config
            timeout=None,
            dangerously_skip_permissions=False,
            no_debug=False,
            verbose=True,  # CLI explicitly set to True, overrides config
        )
        
        result = merge_config_and_args(config, args)
        assert result.budget == 100.0  # CLI value
        assert result.model == "haiku"  # CLI value
        assert result.verbose is True  # CLI value (explicitly set)

    def test_config_fills_missing_cli_args(self):
        """Test that config values fill in when CLI args are None/False."""
        import argparse
        
        config = {
            "budget": 50.0,
            "model": "sonnet",
            "timeout": 300.0,
            "dangerously_skip_permissions": True,
            "no_debug": True,
            "verbose": True,
        }
        args = argparse.Namespace(
            budget=None,  # Not specified, use config
            model=None,  # Not specified, use config
            timeout=None,  # Not specified, use config
            dangerously_skip_permissions=False,  # Not specified, use config
            no_debug=False,  # Not specified, use config
            verbose=False,  # Not specified, use config
        )
        
        result = merge_config_and_args(config, args)
        assert result.budget == 50.0  # From config
        assert result.model == "sonnet"  # From config
        assert result.timeout == 300.0  # From config
        assert result.dangerously_skip_permissions is True  # From config
        assert result.no_debug is True  # From config
        assert result.verbose is True  # From config

    def test_boolean_flags_only_set_if_false(self):
        """Test that boolean flags are only set from config if CLI value is False."""
        import argparse
        
        config = {"dangerously_skip_permissions": True, "verbose": True}
        
        # CLI explicitly set to True - should not be overridden
        args = argparse.Namespace(
            dangerously_skip_permissions=True,
            no_debug=False,
            verbose=True,
        )
        result = merge_config_and_args(config, args)
        assert result.dangerously_skip_permissions is True
        assert result.verbose is True
        
        # CLI set to False - should be overridden by config
        args = argparse.Namespace(
            dangerously_skip_permissions=False,
            no_debug=False,
            verbose=False,
        )
        result = merge_config_and_args(config, args)
        assert result.dangerously_skip_permissions is True  # From config
        assert result.verbose is True  # From config


class TestInitConfig:
    """Tests for init_config()."""

    def test_creates_config_file(self, tmp_path):
        """Test that init_config creates config file with all options commented."""
        # Create a project root with .git to control where config is created
        project_root = tmp_path / "project"
        project_root.mkdir()
        (project_root / ".git").mkdir()
        
        subdir = project_root / "subdir"
        subdir.mkdir()
        
        result = init_config(subdir)
        assert result == 0
        
        config_file = project_root / ".raymond" / "config.toml"
        assert config_file.exists()
        
        content = config_file.read_text()
        assert "[raymond]" in content
        assert "# budget = 10.0" in content
        assert "# dangerously_skip_permissions = false" in content
        assert "# model = \"sonnet\"" in content
        assert "# timeout = 600.0" in content
        assert "# no_debug = false" in content
        assert "# verbose = false" in content

    def test_creates_raymond_dir_if_missing(self, tmp_path):
        """Test that init_config creates .raymond directory if it doesn't exist."""
        # Create a project root with .git to control where config is created
        project_root = tmp_path / "project"
        project_root.mkdir()
        (project_root / ".git").mkdir()
        
        subdir = project_root / "subdir"
        subdir.mkdir()
        
        result = init_config(subdir)
        assert result == 0
        
        raymond_dir = project_root / ".raymond"
        assert raymond_dir.exists()
        assert raymond_dir.is_dir()

    def test_creates_at_project_root(self, tmp_path):
        """Test that init_config creates config at project root."""
        project_root = tmp_path / "project"
        project_root.mkdir()
        (project_root / ".git").mkdir()
        
        subdir = project_root / "subdir" / "nested"
        subdir.mkdir(parents=True)
        
        with patch('src.config.Path.cwd', return_value=subdir):
            result = init_config(subdir)
            assert result == 0
            
            config_file = project_root / ".raymond" / "config.toml"
            assert config_file.exists()

    def test_refuses_if_config_exists(self, tmp_path):
        """Test that init_config refuses if config file already exists."""
        raymond_dir = tmp_path / ".raymond"
        raymond_dir.mkdir()
        config_file = raymond_dir / "config.toml"
        config_file.write_text("[raymond]\nbudget = 50.0\n")
        
        with patch('src.config.Path.cwd', return_value=tmp_path):
            result = init_config(tmp_path)
            assert result == 1
            
            # Should not have modified existing file
            content = config_file.read_text()
            assert "budget = 50.0" in content

    def test_reports_existing_file_location(self, tmp_path, capsys):
        """Test that init_config reports location of existing config file."""
        raymond_dir = tmp_path / ".raymond"
        raymond_dir.mkdir()
        config_file = raymond_dir / "config.toml"
        config_file.write_text("[raymond]\nbudget = 50.0\n")
        
        with patch('src.config.Path.cwd', return_value=tmp_path):
            result = init_config(tmp_path)
            assert result == 1
            
            captured = capsys.readouterr()
            assert "already exists" in captured.err
            assert str(config_file) in captured.err
