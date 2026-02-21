"""Tests for ScriptExecutor zip scope support."""

import asyncio
import zipfile
from pathlib import Path
from unittest.mock import patch

import pytest

from src.orchestrator.executors import ScriptExecutor, ExecutionContext
from src.orchestrator.errors import ScriptError


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

class MockEventBus:
    """Minimal event bus for testing."""
    def __init__(self):
        self.events = []

    def emit(self, event):
        self.events.append(event)


def make_zip(tmp_path, entries: dict, zip_name: str = "workflow.zip") -> str:
    """Create a zip archive with given text entries. Returns the str path."""
    zip_path = str(tmp_path / zip_name)
    with zipfile.ZipFile(zip_path, "w") as zf:
        for name, content in entries.items():
            zf.writestr(name, content)
    return zip_path


def make_state(zip_path: str) -> dict:
    """Build a minimal workflow state dict for ScriptExecutor."""
    return {
        "scope_dir": zip_path,
        "workflow_id": "test-wf",
        "total_cost_usd": 0.0,
    }


def make_agent(current_state: str) -> dict:
    """Build a minimal agent dict for ScriptExecutor."""
    return {
        "id": "main",
        "current_state": current_state,
        "session_id": None,
        "stack": [],
    }


def make_context(zip_path: str) -> ExecutionContext:
    return ExecutionContext(
        bus=MockEventBus(),
        workflow_id="test-wf",
        scope_dir=zip_path,
    )


# ---------------------------------------------------------------------------
# Zip scope execution tests
# ---------------------------------------------------------------------------

class TestScriptExecutorZip:

    @pytest.mark.unix
    @pytest.mark.asyncio
    async def test_executes_sh_script_from_zip(self, tmp_path):
        """Script in zip scope executes and produces correct output."""
        zip_path = make_zip(tmp_path, {
            "RUN.sh": "#!/bin/bash\necho '<goto>NEXT.md</goto>'\n",
            "NEXT.md": "Next step",
        })
        agent = make_agent("RUN.sh")
        state = make_state(zip_path)
        context = make_context(zip_path)

        executor = ScriptExecutor()
        result = await executor.execute(agent, state, context)

        assert result.transition.tag == "goto"
        assert result.transition.target == "NEXT.md"

    @pytest.mark.unix
    @pytest.mark.asyncio
    async def test_temp_file_deleted_after_successful_execution(self, tmp_path):
        """Temp file is deleted after successful script execution."""
        zip_path = make_zip(tmp_path, {
            "RUN.sh": "#!/bin/bash\necho '<goto>NEXT.md</goto>'\n",
            "NEXT.md": "Next step",
        })
        agent = make_agent("RUN.sh")
        state = make_state(zip_path)
        context = make_context(zip_path)

        captured_tmp_path = []

        import src.orchestrator.executors.script as script_module
        original_extract_script = script_module.extract_script

        def capturing_extract(zip_p, filename):
            path = original_extract_script(zip_p, filename)
            captured_tmp_path.append(path)
            return path

        executor = ScriptExecutor()
        with patch("src.orchestrator.executors.script.extract_script", side_effect=capturing_extract):
            await executor.execute(agent, state, context)

        assert len(captured_tmp_path) == 1
        assert not Path(captured_tmp_path[0]).exists(), "Temp file should be deleted after execution"

    @pytest.mark.unix
    @pytest.mark.asyncio
    async def test_temp_file_deleted_after_failed_execution(self, tmp_path):
        """Temp file is deleted even when script execution raises an error."""
        zip_path = make_zip(tmp_path, {
            "FAIL.sh": "#!/bin/bash\nexit 1\n",
        })
        agent = make_agent("FAIL.sh")
        state = make_state(zip_path)
        context = make_context(zip_path)

        captured_tmp_path = []

        import src.orchestrator.executors.script as script_module
        original_extract_script = script_module.extract_script

        def capturing_extract(zip_p, filename):
            path = original_extract_script(zip_p, filename)
            captured_tmp_path.append(path)
            return path

        executor = ScriptExecutor()
        with patch("src.orchestrator.executors.script.extract_script", side_effect=capturing_extract):
            with pytest.raises(ScriptError):
                await executor.execute(agent, state, context)

        assert len(captured_tmp_path) == 1
        assert not Path(captured_tmp_path[0]).exists(), "Temp file should be deleted even after failure"

    @pytest.mark.unix
    @pytest.mark.asyncio
    async def test_concurrent_extractions_produce_distinct_temp_paths(self, tmp_path):
        """Two concurrent calls to extract_script produce distinct temp file paths."""
        zip_path = make_zip(tmp_path, {
            "WORK.sh": "#!/bin/bash\necho '<goto>DONE.md</goto>'\n",
            "DONE.md": "Done",
        })

        paths = []

        import src.orchestrator.executors.script as script_module
        original_extract_script = script_module.extract_script

        def capturing_extract(zip_p, filename):
            path = original_extract_script(zip_p, filename)
            paths.append(path)
            return path

        async def run_one():
            agent = make_agent("WORK.sh")
            state = make_state(zip_path)
            context = make_context(zip_path)
            executor = ScriptExecutor()
            with patch("src.orchestrator.executors.script.extract_script", side_effect=capturing_extract):
                return await executor.execute(agent, state, context)

        await asyncio.gather(run_one(), run_one())

        assert len(paths) == 2
        assert paths[0] != paths[1], "Concurrent extractions should produce distinct temp file paths"

    @pytest.mark.unix
    @pytest.mark.asyncio
    async def test_executes_sh_from_single_folder_zip(self, tmp_path):
        """Script in single-folder zip layout executes correctly."""
        zip_path = make_zip(tmp_path, {
            "mywf/RUN.sh": "#!/bin/bash\necho '<goto>DONE.md</goto>'\n",
            "mywf/DONE.md": "Done",
        })
        agent = make_agent("RUN.sh")
        state = make_state(zip_path)
        context = make_context(zip_path)

        executor = ScriptExecutor()
        result = await executor.execute(agent, state, context)

        assert result.transition.tag == "goto"
        assert result.transition.target == "DONE.md"


# ---------------------------------------------------------------------------
# Directory scope regression tests
# ---------------------------------------------------------------------------

class TestScriptExecutorDirectoryScopeUnchanged:

    @pytest.mark.unix
    @pytest.mark.asyncio
    async def test_directory_scope_execution_unaffected(self, tmp_path):
        """Directory-scope execution is unaffected by zip changes."""
        scope_dir = tmp_path / "workflow"
        scope_dir.mkdir()
        script_file = scope_dir / "RUN.sh"
        script_file.write_text("#!/bin/bash\necho '<goto>NEXT.md</goto>'\n")
        script_file.chmod(0o755)
        (scope_dir / "NEXT.md").write_text("Next")

        agent = make_agent("RUN.sh")
        state = {
            "scope_dir": str(scope_dir),
            "workflow_id": "test-wf",
            "total_cost_usd": 0.0,
        }
        context = ExecutionContext(
            bus=MockEventBus(),
            workflow_id="test-wf",
            scope_dir=str(scope_dir),
        )

        executor = ScriptExecutor()
        result = await executor.execute(agent, state, context)

        assert result.transition.tag == "goto"
        assert result.transition.target == "NEXT.md"

    @pytest.mark.unix
    @pytest.mark.asyncio
    async def test_directory_scope_does_not_create_temp_file(self, tmp_path):
        """Directory-scope execution never creates a temp file."""
        scope_dir = tmp_path / "workflow"
        scope_dir.mkdir()
        script_file = scope_dir / "RUN.sh"
        script_file.write_text("#!/bin/bash\necho '<goto>NEXT.md</goto>'\n")
        script_file.chmod(0o755)
        (scope_dir / "NEXT.md").write_text("Next")

        agent = make_agent("RUN.sh")
        state = {
            "scope_dir": str(scope_dir),
            "workflow_id": "test-wf",
            "total_cost_usd": 0.0,
        }
        context = ExecutionContext(
            bus=MockEventBus(),
            workflow_id="test-wf",
            scope_dir=str(scope_dir),
        )

        executor = ScriptExecutor()
        import src.orchestrator.executors.script as script_module
        with patch.object(script_module, "extract_script", wraps=script_module.extract_script) as mock_extract:
            await executor.execute(agent, state, context)
            mock_extract.assert_not_called()
