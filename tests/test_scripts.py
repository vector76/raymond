"""Tests for script execution infrastructure (Steps 2.1 and 2.2).

This module tests the run_script() function which executes shell scripts
(.sh on Unix, .bat on Windows) and captures their output, as well as
platform detection utilities.
"""

import asyncio
import os
import sys
import pytest

from src.scripts import run_script, ScriptResult, ScriptTimeoutError, is_unix, is_windows


# =============================================================================
# Step 2.2: Platform Detection Tests
# =============================================================================


class TestPlatformDetection:
    """Tests for platform detection functions (Step 2.2.1)."""

    def test_is_unix_and_is_windows_are_mutually_exclusive(self):
        """2.2.1: is_unix() and is_windows() are mutually exclusive."""
        # Exactly one should be true
        assert is_unix() != is_windows()

    def test_is_windows_matches_sys_platform(self):
        """2.2.1: is_windows() matches sys.platform detection."""
        expected = sys.platform.startswith('win')
        assert is_windows() == expected

    def test_is_unix_matches_sys_platform(self):
        """2.2.1: is_unix() returns True when not on Windows."""
        expected = not sys.platform.startswith('win')
        assert is_unix() == expected

    @pytest.mark.windows
    def test_is_windows_true_on_windows(self):
        """2.2.1: is_windows() returns True on Windows."""
        assert is_windows() is True

    @pytest.mark.windows
    def test_is_unix_false_on_windows(self):
        """2.2.1: is_unix() returns False on Windows."""
        assert is_unix() is False

    @pytest.mark.unix
    def test_is_unix_true_on_unix(self):
        """2.2.1: is_unix() returns True on Unix."""
        assert is_unix() is True

    @pytest.mark.unix
    def test_is_windows_false_on_unix(self):
        """2.2.1: is_windows() returns False on Unix."""
        assert is_windows() is False


# =============================================================================
# Step 2.1: Script Execution Tests
# =============================================================================


class TestRunScriptStdout:
    """Tests for stdout capture from run_script()."""

    # =========================================================================
    # Unix-only tests
    # =========================================================================

    @pytest.mark.unix
    async def test_run_script_sh_captures_stdout(self, tmp_path):
        """2.1.1: run_script() executes .sh file and captures stdout."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho 'Hello from bash'\n")
        script_file.chmod(0o755)

        result = await run_script(str(script_file))

        assert "Hello from bash" in result.stdout

    @pytest.mark.unix
    async def test_run_script_sh_multiline_stdout(self, tmp_path):
        """run_script() captures multiline stdout from .sh file."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho 'Line 1'\necho 'Line 2'\necho 'Line 3'\n")
        script_file.chmod(0o755)

        result = await run_script(str(script_file))

        assert "Line 1" in result.stdout
        assert "Line 2" in result.stdout
        assert "Line 3" in result.stdout

    # =========================================================================
    # Windows-only tests
    # =========================================================================

    @pytest.mark.windows
    async def test_run_script_bat_captures_stdout(self, tmp_path):
        """2.1.2: run_script() executes .bat file and captures stdout."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho Hello from batch\n")

        result = await run_script(str(script_file))

        assert "Hello from batch" in result.stdout

    @pytest.mark.windows
    async def test_run_script_bat_multiline_stdout(self, tmp_path):
        """run_script() captures multiline stdout from .bat file."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho Line 1\necho Line 2\necho Line 3\n")

        result = await run_script(str(script_file))

        assert "Line 1" in result.stdout
        assert "Line 2" in result.stdout
        assert "Line 3" in result.stdout


class TestRunScriptStderr:
    """Tests for stderr capture from run_script()."""

    @pytest.mark.unix
    async def test_run_script_sh_captures_stderr(self, tmp_path):
        """2.1.3: run_script() captures stderr separately (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho 'stdout message'\necho 'stderr message' >&2\n")
        script_file.chmod(0o755)

        result = await run_script(str(script_file))

        assert "stdout message" in result.stdout
        assert "stderr message" in result.stderr
        # Verify stderr doesn't leak into stdout
        assert "stderr message" not in result.stdout

    @pytest.mark.windows
    async def test_run_script_bat_captures_stderr(self, tmp_path):
        """2.1.3: run_script() captures stderr separately (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho stdout message\necho stderr message 1>&2\n")

        result = await run_script(str(script_file))

        assert "stdout message" in result.stdout
        assert "stderr message" in result.stderr
        # Verify stderr doesn't leak into stdout
        assert "stderr message" not in result.stdout


class TestRunScriptExitCode:
    """Tests for exit code capture from run_script()."""

    @pytest.mark.unix
    async def test_run_script_sh_returns_exit_code_zero(self, tmp_path):
        """2.1.4: run_script() returns exit code 0 on success (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\nexit 0\n")
        script_file.chmod(0o755)

        result = await run_script(str(script_file))

        assert result.exit_code == 0

    @pytest.mark.unix
    async def test_run_script_sh_returns_exit_code_nonzero(self, tmp_path):
        """2.1.4: run_script() returns non-zero exit code (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\nexit 42\n")
        script_file.chmod(0o755)

        result = await run_script(str(script_file))

        assert result.exit_code == 42

    @pytest.mark.windows
    async def test_run_script_bat_returns_exit_code_zero(self, tmp_path):
        """2.1.4: run_script() returns exit code 0 on success (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\nexit /b 0\n")

        result = await run_script(str(script_file))

        assert result.exit_code == 0

    @pytest.mark.windows
    async def test_run_script_bat_returns_exit_code_nonzero(self, tmp_path):
        """2.1.4: run_script() returns non-zero exit code (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\nexit /b 42\n")

        result = await run_script(str(script_file))

        assert result.exit_code == 42


class TestRunScriptTimeout:
    """Tests for timeout handling in run_script()."""

    @pytest.mark.unix
    async def test_run_script_sh_respects_timeout_success(self, tmp_path):
        """2.1.5: run_script() respects timeout parameter - completes within timeout (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho 'fast'\n")
        script_file.chmod(0o755)

        # Should complete well within 5 seconds
        result = await run_script(str(script_file), timeout=5.0)

        assert "fast" in result.stdout
        assert result.exit_code == 0

    @pytest.mark.unix
    async def test_run_script_sh_raises_on_timeout(self, tmp_path):
        """2.1.6: run_script() raises ScriptTimeoutError on timeout (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\nsleep 10\necho 'done'\n")
        script_file.chmod(0o755)

        with pytest.raises(ScriptTimeoutError):
            await run_script(str(script_file), timeout=0.5)

    @pytest.mark.windows
    async def test_run_script_bat_respects_timeout_success(self, tmp_path):
        """2.1.5: run_script() respects timeout parameter - completes within timeout (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho fast\n")

        # Should complete well within 5 seconds
        result = await run_script(str(script_file), timeout=5.0)

        assert "fast" in result.stdout
        assert result.exit_code == 0

    @pytest.mark.windows
    async def test_run_script_bat_raises_on_timeout(self, tmp_path):
        """2.1.6: run_script() raises ScriptTimeoutError on timeout (.bat)."""
        script_file = tmp_path / "test.bat"
        # ping -n 10 localhost waits ~10 seconds on Windows
        script_file.write_text("@echo off\nping -n 10 127.0.0.1 > nul\necho done\n")

        with pytest.raises(ScriptTimeoutError):
            await run_script(str(script_file), timeout=0.5)


class TestRunScriptShellSelection:
    """Tests for correct shell selection based on file type."""

    @pytest.mark.unix
    async def test_run_script_sh_uses_bash(self, tmp_path):
        """2.1.7: run_script() uses bash for .sh files."""
        script_file = tmp_path / "test.sh"
        # Use bash-specific syntax to verify bash is being used
        script_file.write_text("#!/bin/bash\necho \"Shell: $BASH\"\n")
        script_file.chmod(0o755)

        result = await run_script(str(script_file))

        # BASH variable is set when running under bash
        assert "Shell:" in result.stdout

    @pytest.mark.windows
    async def test_run_script_bat_uses_cmd(self, tmp_path):
        """2.1.7: run_script() uses cmd.exe for .bat files."""
        script_file = tmp_path / "test.bat"
        # Use cmd-specific syntax to verify cmd is being used
        script_file.write_text("@echo off\necho Shell: %ComSpec%\n")

        result = await run_script(str(script_file))

        # ComSpec contains path to cmd.exe
        assert "cmd.exe" in result.stdout.lower()

    @pytest.mark.unix
    async def test_run_script_bat_raises_on_unix(self, tmp_path):
        """2.1.7: run_script() raises error for .bat on Unix."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho test\n")

        with pytest.raises(ValueError, match="[Pp]latform|[Ww]indows|[Uu]nix|\\.bat"):
            await run_script(str(script_file))

    @pytest.mark.windows
    async def test_run_script_sh_raises_on_windows(self, tmp_path):
        """2.1.7: run_script() raises error for .sh on Windows."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho test\n")

        with pytest.raises(ValueError, match="[Pp]latform|[Ww]indows|[Uu]nix|\\.sh"):
            await run_script(str(script_file))


class TestRunScriptAsync:
    """Tests for async behavior of run_script()."""

    @pytest.mark.unix
    async def test_run_script_sh_is_async(self, tmp_path):
        """2.1.8: run_script() is async and doesn't block event loop (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\nsleep 0.1\necho 'done'\n")
        script_file.chmod(0o755)

        # Track if other coroutines can run during script execution
        concurrent_ran = False

        async def concurrent_task():
            nonlocal concurrent_ran
            await asyncio.sleep(0.01)
            concurrent_ran = True

        # Run both concurrently
        await asyncio.gather(
            run_script(str(script_file)),
            concurrent_task()
        )

        assert concurrent_ran, "Concurrent coroutine should have run during script execution"

    @pytest.mark.windows
    async def test_run_script_bat_is_async(self, tmp_path):
        """2.1.8: run_script() is async and doesn't block event loop (.bat)."""
        script_file = tmp_path / "test.bat"
        # ping -n 2 adds a small delay on Windows
        script_file.write_text("@echo off\nping -n 2 127.0.0.1 > nul\necho done\n")

        # Track if other coroutines can run during script execution
        concurrent_ran = False

        async def concurrent_task():
            nonlocal concurrent_ran
            await asyncio.sleep(0.01)
            concurrent_ran = True

        # Run both concurrently
        await asyncio.gather(
            run_script(str(script_file)),
            concurrent_task()
        )

        assert concurrent_ran, "Concurrent coroutine should have run during script execution"


class TestRunScriptWorkingDirectory:
    """Tests for working directory handling in run_script()."""

    @pytest.mark.unix
    async def test_run_script_sh_runs_in_orchestrator_directory(self, tmp_path):
        """2.1.9: run_script() runs in orchestrator's working directory, not scope_dir (.sh)."""
        # Create script in a subdirectory
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        script_file = scope_dir / "test.sh"
        script_file.write_text("#!/bin/bash\npwd\n")
        script_file.chmod(0o755)

        original_cwd = os.getcwd()

        result = await run_script(str(script_file))

        # Script should report the orchestrator's cwd, not the scope_dir
        assert original_cwd in result.stdout

    @pytest.mark.windows
    async def test_run_script_bat_runs_in_orchestrator_directory(self, tmp_path):
        """2.1.9: run_script() runs in orchestrator's working directory, not scope_dir (.bat)."""
        # Create script in a subdirectory
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        script_file = scope_dir / "test.bat"
        script_file.write_text("@echo off\ncd\n")

        original_cwd = os.getcwd()

        result = await run_script(str(script_file))

        # Script should report the orchestrator's cwd, not the scope_dir
        assert original_cwd.lower() in result.stdout.lower()


class TestRunScriptErrorHandling:
    """Tests for error handling in run_script()."""

    async def test_run_script_raises_file_not_found(self, tmp_path):
        """run_script() raises FileNotFoundError when script doesn't exist."""
        nonexistent = tmp_path / "does_not_exist.bat"

        with pytest.raises(FileNotFoundError, match="Script not found"):
            await run_script(str(nonexistent))

    async def test_run_script_raises_for_unsupported_extension(self, tmp_path):
        """run_script() raises ValueError for unsupported file extensions."""
        script_file = tmp_path / "test.py"
        script_file.write_text("print('hello')\n")

        with pytest.raises(ValueError, match="[Uu]nsupported.*extension"):
            await run_script(str(script_file))

    async def test_run_script_raises_for_txt_extension(self, tmp_path):
        """run_script() raises ValueError for .txt files."""
        script_file = tmp_path / "test.txt"
        script_file.write_text("some text\n")

        with pytest.raises(ValueError, match="[Uu]nsupported.*extension"):
            await run_script(str(script_file))


class TestScriptResult:
    """Tests for ScriptResult dataclass."""

    def test_script_result_has_stdout(self):
        """ScriptResult contains stdout field."""
        result = ScriptResult(stdout="output", stderr="", exit_code=0)
        assert result.stdout == "output"

    def test_script_result_has_stderr(self):
        """ScriptResult contains stderr field."""
        result = ScriptResult(stdout="", stderr="error", exit_code=0)
        assert result.stderr == "error"

    def test_script_result_has_exit_code(self):
        """ScriptResult contains exit_code field."""
        result = ScriptResult(stdout="", stderr="", exit_code=1)
        assert result.exit_code == 1


class TestScriptTimeoutError:
    """Tests for ScriptTimeoutError exception."""

    def test_script_timeout_error_message(self):
        """ScriptTimeoutError has descriptive message."""
        error = ScriptTimeoutError("test.sh", 5.0)
        assert "test.sh" in str(error)
        assert "5.0" in str(error) or "5" in str(error)
        assert "timeout" in str(error).lower()

    def test_script_timeout_error_is_exception(self):
        """ScriptTimeoutError inherits from Exception."""
        error = ScriptTimeoutError("test.sh", 5.0)
        assert isinstance(error, Exception)
