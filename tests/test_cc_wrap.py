import json
import pytest

from src.cc_wrap import wrap_claude_code, wrap_claude_code_stream


# All tests in this file are integration tests that require the Claude CLI
pytestmark = pytest.mark.integration


@pytest.mark.asyncio
async def test_claude_stream():
    """
    Integration test that invokes claude with "say hello" and streams output.
    Equivalent to: claude -p "say hello" --output-format stream-json --verbose
    """
    prompt = "say hello"

    print(f"Invoking claude with prompt: '{prompt}'")
    print("Streaming output:\n")

    results = []
    async for json_obj in wrap_claude_code_stream(prompt):
        print(json.dumps(json_obj, indent=2))
        print()
        results.append(json_obj)

    assert len(results) > 0, "Expected at least one JSON object from stream"


@pytest.mark.asyncio
async def test_claude_batch():
    """
    Integration test that invokes claude and collects all output at once.
    """
    prompt = "say hello"

    results, session_id = await wrap_claude_code(prompt)

    assert len(results) > 0, "Expected at least one JSON object"


@pytest.mark.asyncio
async def test_claude_stream_haiku():
    """
    Integration test that invokes claude with haiku model and streams output.
    """
    prompt = "say hello"

    results = []
    async for json_obj in wrap_claude_code_stream(prompt, model="haiku"):
        results.append(json_obj)

    assert len(results) > 0, "Expected at least one JSON object from stream"


@pytest.mark.asyncio
async def test_claude_stream_sonnet():
    """
    Integration test that invokes claude with sonnet model and streams output.
    """
    prompt = "say hello"

    results = []
    async for json_obj in wrap_claude_code_stream(prompt, model="sonnet"):
        results.append(json_obj)

    assert len(results) > 0, "Expected at least one JSON object from stream"


@pytest.mark.asyncio
async def test_claude_batch_haiku():
    """
    Integration test that invokes claude with haiku model and collects all output.
    """
    prompt = "say hello"

    results, session_id = await wrap_claude_code(prompt, model="haiku")

    assert len(results) > 0, "Expected at least one JSON object"


@pytest.mark.asyncio
async def test_claude_batch_sonnet():
    """
    Integration test that invokes claude with sonnet model and collects all output.
    """
    prompt = "say hello"

    results, session_id = await wrap_claude_code(prompt, model="sonnet")

    assert len(results) > 0, "Expected at least one JSON object"
