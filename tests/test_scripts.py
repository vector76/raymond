"""Tests for script execution infrastructure (Steps 2.1, 2.2, 2.3, and 3.1).

This module tests the run_script() function which executes shell scripts
(.sh on Unix, .bat on Windows) and captures their output, as well as
platform detection utilities, output parsing, and environment variable injection.
"""

import asyncio
import os
import sys
import pytest

from src.scripts import run_script, ScriptResult, ScriptTimeoutError, is_unix, is_windows, build_script_env
from src.parsing import parse_transitions


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


# =============================================================================
# Step 2.3: Output Parsing Tests
# =============================================================================


class TestScriptOutputParsing:
    """Tests for parsing transition tags from script output (Step 2.3).
    
    These tests verify that the existing parse_transitions() function
    works correctly with stdout captured from script execution.
    """

    # =========================================================================
    # 2.3.1: Parse transition tag from script stdout
    # =========================================================================

    @pytest.mark.unix
    async def test_parse_goto_from_sh_stdout(self, tmp_path):
        """2.3.1: Parse <goto> transition from .sh script stdout."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho '<goto>NEXT.md</goto>'\n")
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "NEXT.md"

    @pytest.mark.windows
    async def test_parse_goto_from_bat_stdout(self, tmp_path):
        """2.3.1: Parse <goto> transition from .bat script stdout."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho ^<goto^>NEXT.md^</goto^>\n")

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "NEXT.md"

    @pytest.mark.unix
    async def test_parse_reset_from_sh_stdout(self, tmp_path):
        """2.3.1: Parse <reset> transition from .sh script stdout."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho '<reset>POLL.md</reset>'\n")
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "reset"
        assert transitions[0].target == "POLL.md"

    @pytest.mark.windows
    async def test_parse_reset_from_bat_stdout(self, tmp_path):
        """2.3.1: Parse <reset> transition from .bat script stdout."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho ^<reset^>POLL.md^</reset^>\n")

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "reset"
        assert transitions[0].target == "POLL.md"

    # =========================================================================
    # 2.3.2: Transition tag can appear anywhere in stdout
    # =========================================================================

    @pytest.mark.unix
    async def test_tag_with_preceding_output_sh(self, tmp_path):
        """2.3.2: Tag works when preceded by other output (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text(
            "#!/bin/bash\n"
            "echo 'Starting process...'\n"
            "echo 'Processing data...'\n"
            "echo '<goto>DONE.md</goto>'\n"
        )
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "DONE.md"

    @pytest.mark.windows
    async def test_tag_with_preceding_output_bat(self, tmp_path):
        """2.3.2: Tag works when preceded by other output (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text(
            "@echo off\n"
            "echo Starting process...\n"
            "echo Processing data...\n"
            "echo ^<goto^>DONE.md^</goto^>\n"
        )

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "DONE.md"

    @pytest.mark.unix
    async def test_tag_with_following_output_sh(self, tmp_path):
        """2.3.2: Tag works when followed by other output (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text(
            "#!/bin/bash\n"
            "echo '<goto>NEXT.md</goto>'\n"
            "echo 'Cleanup complete'\n"
            "echo 'Exiting...'\n"
        )
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "NEXT.md"

    @pytest.mark.windows
    async def test_tag_with_following_output_bat(self, tmp_path):
        """2.3.2: Tag works when followed by other output (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text(
            "@echo off\n"
            "echo ^<goto^>NEXT.md^</goto^>\n"
            "echo Cleanup complete\n"
            "echo Exiting...\n"
        )

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "NEXT.md"

    @pytest.mark.unix
    async def test_tag_in_middle_of_output_sh(self, tmp_path):
        """2.3.2: Tag works when in the middle of output (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text(
            "#!/bin/bash\n"
            "echo 'Setup phase'\n"
            "echo '<goto>MIDDLE.md</goto>'\n"
            "echo 'Teardown phase'\n"
        )
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "MIDDLE.md"

    @pytest.mark.windows
    async def test_tag_in_middle_of_output_bat(self, tmp_path):
        """2.3.2: Tag works when in the middle of output (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text(
            "@echo off\n"
            "echo Setup phase\n"
            "echo ^<goto^>MIDDLE.md^</goto^>\n"
            "echo Teardown phase\n"
        )

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "MIDDLE.md"

    # =========================================================================
    # 2.3.3: Extract tag attributes from script output
    # =========================================================================

    @pytest.mark.unix
    async def test_parse_call_with_return_attribute_sh(self, tmp_path):
        """2.3.3: Parse <call> with return attribute from .sh stdout."""
        script_file = tmp_path / "test.sh"
        script_file.write_text(
            '#!/bin/bash\necho \'<call return="RESUME.md">CHILD.md</call>\'\n'
        )
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "call"
        assert transitions[0].target == "CHILD.md"
        assert transitions[0].attributes == {"return": "RESUME.md"}

    @pytest.mark.windows
    async def test_parse_call_with_return_attribute_bat(self, tmp_path):
        """2.3.3: Parse <call> with return attribute from .bat stdout."""
        script_file = tmp_path / "test.bat"
        script_file.write_text(
            '@echo off\necho ^<call return="RESUME.md"^>CHILD.md^</call^>\n'
        )

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "call"
        assert transitions[0].target == "CHILD.md"
        assert transitions[0].attributes == {"return": "RESUME.md"}

    @pytest.mark.unix
    async def test_parse_fork_with_multiple_attributes_sh(self, tmp_path):
        """2.3.3: Parse <fork> with multiple attributes from .sh stdout."""
        script_file = tmp_path / "test.sh"
        script_file.write_text(
            '#!/bin/bash\necho \'<fork next="JOIN.md" item="task1">WORKER.md</fork>\'\n'
        )
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "fork"
        assert transitions[0].target == "WORKER.md"
        assert transitions[0].attributes == {"next": "JOIN.md", "item": "task1"}

    @pytest.mark.windows
    async def test_parse_fork_with_multiple_attributes_bat(self, tmp_path):
        """2.3.3: Parse <fork> with multiple attributes from .bat stdout."""
        script_file = tmp_path / "test.bat"
        script_file.write_text(
            '@echo off\necho ^<fork next="JOIN.md" item="task1"^>WORKER.md^</fork^>\n'
        )

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "fork"
        assert transitions[0].target == "WORKER.md"
        assert transitions[0].attributes == {"next": "JOIN.md", "item": "task1"}

    @pytest.mark.unix
    async def test_parse_function_with_return_attribute_sh(self, tmp_path):
        """2.3.3: Parse <function> with return attribute from .sh stdout."""
        script_file = tmp_path / "test.sh"
        script_file.write_text(
            '#!/bin/bash\necho \'<function return="CALLBACK.md">EVAL.md</function>\'\n'
        )
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "function"
        assert transitions[0].target == "EVAL.md"
        assert transitions[0].attributes == {"return": "CALLBACK.md"}

    @pytest.mark.windows
    async def test_parse_function_with_return_attribute_bat(self, tmp_path):
        """2.3.3: Parse <function> with return attribute from .bat stdout."""
        script_file = tmp_path / "test.bat"
        script_file.write_text(
            '@echo off\necho ^<function return="CALLBACK.md"^>EVAL.md^</function^>\n'
        )

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "function"
        assert transitions[0].target == "EVAL.md"
        assert transitions[0].attributes == {"return": "CALLBACK.md"}

    # =========================================================================
    # 2.3.4: Handle <result>payload</result> from scripts
    # =========================================================================

    @pytest.mark.unix
    async def test_parse_result_with_payload_sh(self, tmp_path):
        """2.3.4: Parse <result> with payload from .sh stdout."""
        script_file = tmp_path / "test.sh"
        script_file.write_text(
            "#!/bin/bash\necho '<result>Task completed successfully</result>'\n"
        )
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "result"
        assert transitions[0].target == ""
        assert transitions[0].payload == "Task completed successfully"

    @pytest.mark.windows
    async def test_parse_result_with_payload_bat(self, tmp_path):
        """2.3.4: Parse <result> with payload from .bat stdout."""
        script_file = tmp_path / "test.bat"
        script_file.write_text(
            "@echo off\necho ^<result^>Task completed successfully^</result^>\n"
        )

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "result"
        assert transitions[0].target == ""
        assert transitions[0].payload == "Task completed successfully"

    @pytest.mark.unix
    async def test_parse_result_empty_payload_sh(self, tmp_path):
        """2.3.4: Parse <result> with empty payload from .sh stdout."""
        script_file = tmp_path / "test.sh"
        script_file.write_text(
            "#!/bin/bash\necho '<result></result>'\n"
        )
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "result"
        assert transitions[0].payload == ""

    @pytest.mark.windows
    async def test_parse_result_empty_payload_bat(self, tmp_path):
        """2.3.4: Parse <result> with empty payload from .bat stdout."""
        script_file = tmp_path / "test.bat"
        script_file.write_text(
            "@echo off\necho ^<result^>^</result^>\n"
        )

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "result"
        assert transitions[0].payload == ""

    @pytest.mark.unix
    async def test_parse_result_json_payload_sh(self, tmp_path):
        """2.3.4: Parse <result> with JSON-like payload from .sh stdout."""
        script_file = tmp_path / "test.sh"
        script_file.write_text(
            '#!/bin/bash\necho \'<result>{"status": "ok", "count": 42}</result>\'\n'
        )
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "result"
        assert transitions[0].payload == '{"status": "ok", "count": 42}'

    @pytest.mark.windows
    async def test_parse_result_json_payload_bat(self, tmp_path):
        """2.3.4: Parse <result> with JSON-like payload from .bat stdout."""
        script_file = tmp_path / "test.bat"
        # JSON in batch - only < and > need escaping with ^
        script_file.write_text(
            '@echo off\necho ^<result^>{"status": "ok", "count": 42}^</result^>\n'
        )

        result = await run_script(str(script_file))
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "result"
        assert transitions[0].payload == '{"status": "ok", "count": 42}'

    # =========================================================================
    # 2.3.5: Verify parse_transitions() is reused for script output
    # =========================================================================

    @pytest.mark.unix
    async def test_reuses_parse_transitions_sh(self, tmp_path):
        """2.3.5: Script output parsing uses existing parse_transitions()."""
        script_file = tmp_path / "test.sh"
        # Use a complex tag to verify full parsing capability
        script_file.write_text(
            '#!/bin/bash\n'
            'echo "Log: starting"\n'
            'echo \'<fork next="MERGE.md" item="data1" priority="high">WORKER.md</fork>\'\n'
            'echo "Log: done"\n'
        )
        script_file.chmod(0o755)

        result = await run_script(str(script_file))
        
        # The exact same parse_transitions function works on script output
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "fork"
        assert transitions[0].target == "WORKER.md"
        assert transitions[0].attributes["next"] == "MERGE.md"
        assert transitions[0].attributes["item"] == "data1"
        assert transitions[0].attributes["priority"] == "high"

    @pytest.mark.windows
    async def test_reuses_parse_transitions_bat(self, tmp_path):
        """2.3.5: Script output parsing uses existing parse_transitions()."""
        script_file = tmp_path / "test.bat"
        # Use a complex tag to verify full parsing capability
        script_file.write_text(
            '@echo off\n'
            'echo Log: starting\n'
            'echo ^<fork next="MERGE.md" item="data1" priority="high"^>WORKER.md^</fork^>\n'
            'echo Log: done\n'
        )

        result = await run_script(str(script_file))
        
        # The exact same parse_transitions function works on script output
        transitions = parse_transitions(result.stdout)

        assert len(transitions) == 1
        assert transitions[0].tag == "fork"
        assert transitions[0].target == "WORKER.md"
        assert transitions[0].attributes["next"] == "MERGE.md"
        assert transitions[0].attributes["item"] == "data1"
        assert transitions[0].attributes["priority"] == "high"

    def test_parse_transitions_works_on_script_result_stdout(self):
        """2.3.5: parse_transitions() accepts ScriptResult.stdout directly."""
        # Simulate script output without actually running a script
        simulated_stdout = "Some logging\n<goto>TARGET.md</goto>\nMore output\n"
        
        transitions = parse_transitions(simulated_stdout)
        
        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "TARGET.md"


# =============================================================================
# Step 3.1: Core Environment Variables Tests
# =============================================================================


class TestBuildScriptEnv:
    """Tests for build_script_env() function (Step 3.1).
    
    This function builds the environment variables dict that should be
    passed to run_script() for workflow context.
    """

    def test_build_script_env_sets_workflow_id(self):
        """3.1.1: RAYMOND_WORKFLOW_ID is set in the environment."""
        env = build_script_env(
            workflow_id="wf-12345",
            agent_id="main",
            state_dir="/path/to/states",
            state_file="/path/to/states/CHECK.bat"
        )

        assert "RAYMOND_WORKFLOW_ID" in env
        assert env["RAYMOND_WORKFLOW_ID"] == "wf-12345"

    def test_build_script_env_sets_agent_id(self):
        """3.1.2: RAYMOND_AGENT_ID is set in the environment."""
        env = build_script_env(
            workflow_id="wf-12345",
            agent_id="worker_1",
            state_dir="/path/to/states",
            state_file="/path/to/states/PROCESS.bat"
        )

        assert "RAYMOND_AGENT_ID" in env
        assert env["RAYMOND_AGENT_ID"] == "worker_1"

    def test_build_script_env_sets_state_dir(self):
        """3.1.3: RAYMOND_STATE_DIR is set to scope directory."""
        env = build_script_env(
            workflow_id="wf-12345",
            agent_id="main",
            state_dir="/workflows/my_workflow/states",
            state_file="/workflows/my_workflow/states/CHECK.bat"
        )

        assert "RAYMOND_STATE_DIR" in env
        assert env["RAYMOND_STATE_DIR"] == "/workflows/my_workflow/states"

    def test_build_script_env_sets_state_file(self):
        """3.1.4: RAYMOND_STATE_FILE is set to state file path."""
        env = build_script_env(
            workflow_id="wf-12345",
            agent_id="main",
            state_dir="/workflows/my_workflow/states",
            state_file="/workflows/my_workflow/states/CHECK.bat"
        )

        assert "RAYMOND_STATE_FILE" in env
        assert env["RAYMOND_STATE_FILE"] == "/workflows/my_workflow/states/CHECK.bat"

    def test_build_script_env_includes_all_required_vars(self):
        """3.1.1-4: All four core environment variables are present."""
        env = build_script_env(
            workflow_id="workflow-abc",
            agent_id="agent-xyz",
            state_dir="/some/dir",
            state_file="/some/dir/STATE.sh"
        )

        required_vars = [
            "RAYMOND_WORKFLOW_ID",
            "RAYMOND_AGENT_ID",
            "RAYMOND_STATE_DIR",
            "RAYMOND_STATE_FILE"
        ]
        for var in required_vars:
            assert var in env, f"Missing required environment variable: {var}"

    def test_build_script_env_returns_dict(self):
        """build_script_env() returns a dict suitable for run_script()."""
        env = build_script_env(
            workflow_id="wf-1",
            agent_id="main",
            state_dir="/dir",
            state_file="/dir/file.bat"
        )

        assert isinstance(env, dict)
        # All values should be strings
        for key, value in env.items():
            assert isinstance(key, str)
            assert isinstance(value, str)


class TestEnvironmentVariablesInScripts:
    """Integration tests verifying environment variables reach the script."""

    @pytest.mark.windows
    async def test_script_receives_workflow_id_bat(self, tmp_path):
        """3.1.1: Script can access RAYMOND_WORKFLOW_ID (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho WORKFLOW_ID=%RAYMOND_WORKFLOW_ID%\n")

        env = build_script_env(
            workflow_id="test-workflow-123",
            agent_id="main",
            state_dir=str(tmp_path),
            state_file=str(script_file)
        )
        result = await run_script(str(script_file), env=env)

        assert "WORKFLOW_ID=test-workflow-123" in result.stdout

    @pytest.mark.windows
    async def test_script_receives_agent_id_bat(self, tmp_path):
        """3.1.2: Script can access RAYMOND_AGENT_ID (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho AGENT_ID=%RAYMOND_AGENT_ID%\n")

        env = build_script_env(
            workflow_id="wf-1",
            agent_id="worker_7",
            state_dir=str(tmp_path),
            state_file=str(script_file)
        )
        result = await run_script(str(script_file), env=env)

        assert "AGENT_ID=worker_7" in result.stdout

    @pytest.mark.windows
    async def test_script_receives_state_dir_bat(self, tmp_path):
        """3.1.3: Script can access RAYMOND_STATE_DIR (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho STATE_DIR=%RAYMOND_STATE_DIR%\n")

        env = build_script_env(
            workflow_id="wf-1",
            agent_id="main",
            state_dir=str(tmp_path),
            state_file=str(script_file)
        )
        result = await run_script(str(script_file), env=env)

        # Normalize path comparison for Windows
        assert str(tmp_path).lower() in result.stdout.lower()

    @pytest.mark.windows
    async def test_script_receives_state_file_bat(self, tmp_path):
        """3.1.4: Script can access RAYMOND_STATE_FILE (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text("@echo off\necho STATE_FILE=%RAYMOND_STATE_FILE%\n")

        env = build_script_env(
            workflow_id="wf-1",
            agent_id="main",
            state_dir=str(tmp_path),
            state_file=str(script_file)
        )
        result = await run_script(str(script_file), env=env)

        # Normalize path comparison for Windows
        assert str(script_file).lower() in result.stdout.lower()

    @pytest.mark.windows
    async def test_script_receives_all_env_vars_bat(self, tmp_path):
        """3.1.1-4: Script receives all environment variables (.bat)."""
        script_file = tmp_path / "test.bat"
        script_file.write_text(
            "@echo off\n"
            "echo WF=%RAYMOND_WORKFLOW_ID%\n"
            "echo AG=%RAYMOND_AGENT_ID%\n"
            "echo SD=%RAYMOND_STATE_DIR%\n"
            "echo SF=%RAYMOND_STATE_FILE%\n"
        )

        env = build_script_env(
            workflow_id="multi-test-wf",
            agent_id="multi-test-agent",
            state_dir=str(tmp_path),
            state_file=str(script_file)
        )
        result = await run_script(str(script_file), env=env)

        assert "WF=multi-test-wf" in result.stdout
        assert "AG=multi-test-agent" in result.stdout

    @pytest.mark.unix
    async def test_script_receives_workflow_id_sh(self, tmp_path):
        """3.1.1: Script can access RAYMOND_WORKFLOW_ID (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho \"WORKFLOW_ID=$RAYMOND_WORKFLOW_ID\"\n")
        script_file.chmod(0o755)

        env = build_script_env(
            workflow_id="test-workflow-123",
            agent_id="main",
            state_dir=str(tmp_path),
            state_file=str(script_file)
        )
        result = await run_script(str(script_file), env=env)

        assert "WORKFLOW_ID=test-workflow-123" in result.stdout

    @pytest.mark.unix
    async def test_script_receives_agent_id_sh(self, tmp_path):
        """3.1.2: Script can access RAYMOND_AGENT_ID (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho \"AGENT_ID=$RAYMOND_AGENT_ID\"\n")
        script_file.chmod(0o755)

        env = build_script_env(
            workflow_id="wf-1",
            agent_id="worker_7",
            state_dir=str(tmp_path),
            state_file=str(script_file)
        )
        result = await run_script(str(script_file), env=env)

        assert "AGENT_ID=worker_7" in result.stdout

    @pytest.mark.unix
    async def test_script_receives_state_dir_sh(self, tmp_path):
        """3.1.3: Script can access RAYMOND_STATE_DIR (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho \"STATE_DIR=$RAYMOND_STATE_DIR\"\n")
        script_file.chmod(0o755)

        env = build_script_env(
            workflow_id="wf-1",
            agent_id="main",
            state_dir=str(tmp_path),
            state_file=str(script_file)
        )
        result = await run_script(str(script_file), env=env)

        assert f"STATE_DIR={tmp_path}" in result.stdout

    @pytest.mark.unix
    async def test_script_receives_state_file_sh(self, tmp_path):
        """3.1.4: Script can access RAYMOND_STATE_FILE (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text("#!/bin/bash\necho \"STATE_FILE=$RAYMOND_STATE_FILE\"\n")
        script_file.chmod(0o755)

        env = build_script_env(
            workflow_id="wf-1",
            agent_id="main",
            state_dir=str(tmp_path),
            state_file=str(script_file)
        )
        result = await run_script(str(script_file), env=env)

        assert f"STATE_FILE={script_file}" in result.stdout

    @pytest.mark.unix
    async def test_script_receives_all_env_vars_sh(self, tmp_path):
        """3.1.1-4: Script receives all environment variables (.sh)."""
        script_file = tmp_path / "test.sh"
        script_file.write_text(
            "#!/bin/bash\n"
            "echo \"WF=$RAYMOND_WORKFLOW_ID\"\n"
            "echo \"AG=$RAYMOND_AGENT_ID\"\n"
            "echo \"SD=$RAYMOND_STATE_DIR\"\n"
            "echo \"SF=$RAYMOND_STATE_FILE\"\n"
        )
        script_file.chmod(0o755)

        env = build_script_env(
            workflow_id="multi-test-wf",
            agent_id="multi-test-agent",
            state_dir=str(tmp_path),
            state_file=str(script_file)
        )
        result = await run_script(str(script_file), env=env)

        assert "WF=multi-test-wf" in result.stdout
        assert "AG=multi-test-agent" in result.stdout
