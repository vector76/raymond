import asyncio
import json
import sys

from .cc_wrap import wrap_claude_code_stream


async def demo():
    """Demo that invokes claude with 'say hello' and streams output to console."""
    prompt = "say hello"

    print(f"Invoking claude with prompt: '{prompt}'")
    print("Streaming output:\n")

    try:
        async for json_obj in wrap_claude_code_stream(prompt):
            print(json.dumps(json_obj, indent=2))
            print()
    except RuntimeError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    asyncio.run(demo())
