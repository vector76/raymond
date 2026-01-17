import pytest
from pathlib import Path
from typing import Dict, Any, List, Optional
from src.prompts import load_prompt
from src.policy import parse_frontmatter, Policy, validate_transition_policy, PolicyViolationError
from src.parsing import Transition


class TestParseFrontmatter:
    """Tests for parse_frontmatter() function."""

    def test_parse_frontmatter_simple(self):
        """Test parsing simple frontmatter with allowed_transitions."""
        content = """---
allowed_transitions:
  - { tag: goto, target: REVIEW.md }
  - { tag: goto, target: DONE.md }
  - { tag: result }
---
# Prompt Content
This is the actual prompt."""
        
        policy, body = parse_frontmatter(content)
        
        assert policy is not None
        assert len(policy.allowed_transitions) == 3
        assert {"tag": "goto", "target": "REVIEW.md"} in policy.allowed_transitions
        assert {"tag": "goto", "target": "DONE.md"} in policy.allowed_transitions
        assert {"tag": "result"} in policy.allowed_transitions
        assert body.strip() == "# Prompt Content\nThis is the actual prompt."

    def test_parse_frontmatter_multiline_format(self):
        """Test parsing frontmatter with multi-line format."""
        content = """---
allowed_transitions:
  - tag: goto
    target: REVIEW.md
  - tag: goto
    target: DONE.md
  - tag: result
---
# Prompt Content
This is the actual prompt."""
        
        policy, body = parse_frontmatter(content)
        
        assert policy is not None
        assert len(policy.allowed_transitions) == 3
        assert {"tag": "goto", "target": "REVIEW.md"} in policy.allowed_transitions
        assert {"tag": "goto", "target": "DONE.md"} in policy.allowed_transitions
        assert {"tag": "result"} in policy.allowed_transitions

    def test_parse_frontmatter_complex_call(self):
        """Test parsing frontmatter with complex call structure."""
        content = """---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
  - tag: call
    target: RESEARCH.md
    return: SUMMARIZE.md
  - { tag: result }
---
# Complex Prompt
This prompt allows call transitions."""
        
        policy, body = parse_frontmatter(content)
        
        assert policy is not None
        assert len(policy.allowed_transitions) == 3
        assert {"tag": "goto", "target": "NEXT.md"} in policy.allowed_transitions
        assert {"tag": "call", "target": "RESEARCH.md", "return": "SUMMARIZE.md"} in policy.allowed_transitions
        assert {"tag": "result"} in policy.allowed_transitions
        assert "Complex Prompt" in body

    def test_parse_frontmatter_no_frontmatter(self):
        """Test parsing content without frontmatter."""
        content = "# Plain Prompt\n\nThis has no frontmatter."
        
        policy, body = parse_frontmatter(content)
        
        assert policy is None
        assert body == content

    def test_parse_frontmatter_empty_frontmatter(self):
        """Test parsing content with empty frontmatter."""
        content = """---
---
# Prompt
Content here."""
        
        policy, body = parse_frontmatter(content)
        
        assert policy is None
        assert body.strip() == "# Prompt\nContent here."

    def test_parse_frontmatter_mixed_format(self):
        """Test parsing frontmatter with mixed compact and multi-line formats."""
        content = """---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
  - tag: call
    target: RESEARCH.md
    return: SUMMARIZE.md
  - { tag: result }
---
# Prompt
Content."""
        
        policy, body = parse_frontmatter(content)
        
        assert policy is not None
        assert len(policy.allowed_transitions) == 3

    def test_parse_frontmatter_function_and_fork(self):
        """Test parsing frontmatter with function and fork transitions."""
        content = """---
allowed_transitions:
  - tag: function
    target: EVAL.md
    return: NEXT.md
  - tag: fork
    target: WORKER.md
    next: CONTINUE.md
---
# Prompt
Content."""
        
        policy, body = parse_frontmatter(content)
        
        assert policy is not None
        assert len(policy.allowed_transitions) == 2
        assert {"tag": "function", "target": "EVAL.md", "return": "NEXT.md"} in policy.allowed_transitions
        assert {"tag": "fork", "target": "WORKER.md", "next": "CONTINUE.md"} in policy.allowed_transitions

    def test_parse_frontmatter_malformed_yaml(self):
        """Test that malformed YAML frontmatter raises appropriate error."""
        content = """---
allowed_transitions: [goto
---
# Prompt
Content."""
        
        with pytest.raises(ValueError, match="Invalid YAML frontmatter"):
            parse_frontmatter(content)

    def test_parse_frontmatter_invalid_entry_skipped(self):
        """Test that invalid entries (missing tag) are skipped."""
        content = """---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
  - { target: MISSING_TAG.md }
  - { tag: result }
---
# Prompt
Content."""
        
        policy, body = parse_frontmatter(content)
        
        assert policy is not None
        # Should only have 2 valid entries (goto and result)
        assert len(policy.allowed_transitions) == 2
        assert {"tag": "goto", "target": "NEXT.md"} in policy.allowed_transitions
        assert {"tag": "result"} in policy.allowed_transitions


class TestLoadPromptWithPolicy:
    """Tests for load_prompt() returning policy information."""

    def test_load_prompt_with_frontmatter(self, tmp_path):
        """Test that load_prompt() parses frontmatter correctly."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        
        prompt_file = scope_dir / "START.md"
        prompt_file.write_text("""---
allowed_transitions:
  - { tag: goto, target: NEXT.md }
  - { tag: result }
---
# Start Prompt
This is the start.""")
        
        content, policy = load_prompt(str(scope_dir), "START.md")
        
        assert policy is not None
        assert len(policy.allowed_transitions) == 2
        assert "Start Prompt" in content

    def test_load_prompt_without_frontmatter(self, tmp_path):
        """Test that load_prompt() handles files without frontmatter."""
        scope_dir = tmp_path / "workflows" / "test"
        scope_dir.mkdir(parents=True)
        
        prompt_file = scope_dir / "PLAIN.md"
        prompt_file.write_text("# Plain Prompt\n\nNo frontmatter here.")
        
        content, policy = load_prompt(str(scope_dir), "PLAIN.md")
        
        assert policy is None
        assert content == "# Plain Prompt\n\nNo frontmatter here."


class TestValidateTransitionPolicy:
    """Tests for validate_transition_policy() function."""

    def test_validate_allowed_transition(self):
        """Test that allowed transitions pass validation."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "goto", "target": "NEXT.md"},
                {"tag": "result"}
            ]
        )
        transition = Transition(tag="goto", target="NEXT.md", attributes={}, payload="")
        
        # Should not raise
        validate_transition_policy(transition, policy)

    def test_validate_disallowed_tag_raises(self):
        """Test that disallowed tags raise PolicyViolationError."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "goto", "target": "NEXT.md"},
                {"tag": "result"}
            ]
        )
        transition = Transition(tag="fork", target="WORKER.md", attributes={}, payload="")
        
        with pytest.raises(PolicyViolationError, match="Tag 'fork' is not allowed"):
            validate_transition_policy(transition, policy)

    def test_validate_disallowed_target_raises(self):
        """Test that disallowed targets raise PolicyViolationError."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "goto", "target": "NEXT.md"},
                {"tag": "goto", "target": "DONE.md"}
            ]
        )
        transition = Transition(tag="goto", target="OTHER.md", attributes={}, payload="")
        
        with pytest.raises(PolicyViolationError, match="is not allowed"):
            validate_transition_policy(transition, policy)

    def test_validate_result_tag_no_target_restriction(self):
        """Test that result tag doesn't require target validation."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "result"}
            ]
        )
        transition = Transition(tag="result", target="", attributes={}, payload="Done")
        
        # Should not raise (result has no target)
        validate_transition_policy(transition, policy)

    def test_validate_call_with_structured_attributes(self):
        """Test validation of call transitions with return attribute."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "call", "target": "RESEARCH.md", "return": "SUMMARIZE.md"}
            ]
        )
        transition = Transition(
            tag="call",
            target="RESEARCH.md",
            attributes={"return": "SUMMARIZE.md"},
            payload=""
        )
        
        # Should not raise
        validate_transition_policy(transition, policy)

    def test_validate_call_with_wrong_target_raises(self):
        """Test that call with wrong target raises."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "call", "target": "RESEARCH.md", "return": "SUMMARIZE.md"}
            ]
        )
        transition = Transition(
            tag="call",
            target="WRONG.md",
            attributes={"return": "SUMMARIZE.md"},
            payload=""
        )
        
        with pytest.raises(PolicyViolationError, match="is not allowed"):
            validate_transition_policy(transition, policy)

    def test_validate_call_with_wrong_return_raises(self):
        """Test that call with wrong return attribute raises."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "call", "target": "RESEARCH.md", "return": "SUMMARIZE.md"}
            ]
        )
        transition = Transition(
            tag="call",
            target="RESEARCH.md",
            attributes={"return": "WRONG.md"},
            payload=""
        )
        
        with pytest.raises(PolicyViolationError, match="is not allowed"):
            validate_transition_policy(transition, policy)

    def test_validate_no_policy_allows_all(self):
        """Test that None policy allows all transitions."""
        transition = Transition(tag="goto", target="ANY.md", attributes={}, payload="")
        
        # Should not raise (no policy = no restrictions)
        validate_transition_policy(transition, None)

    def test_validate_empty_policy_allows_all(self):
        """Test that empty policy allows all transitions."""
        policy = Policy(allowed_transitions=[])
        transition = Transition(tag="goto", target="ANY.md", attributes={}, payload="")
        
        # Should not raise (empty policy = no restrictions)
        validate_transition_policy(transition, policy)

    def test_validate_function_with_return_attribute(self):
        """Test validation of function transitions with return attribute."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "function", "target": "EVAL.md", "return": "NEXT.md"}
            ]
        )
        transition = Transition(
            tag="function",
            target="EVAL.md",
            attributes={"return": "NEXT.md"},
            payload=""
        )
        
        # Should not raise
        validate_transition_policy(transition, policy)

    def test_validate_fork_with_next_attribute(self):
        """Test validation of fork transitions with next attribute."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "fork", "target": "WORKER.md", "next": "CONTINUE.md"}
            ]
        )
        transition = Transition(
            tag="fork",
            target="WORKER.md",
            attributes={"next": "CONTINUE.md", "item": "data"},
            payload=""
        )
        
        # Should not raise (fork attributes like 'item' are metadata, not validated)
        validate_transition_policy(transition, policy)

    def test_validate_fork_with_wrong_next_raises(self):
        """Test that fork with wrong next attribute raises."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "fork", "target": "WORKER.md", "next": "CONTINUE.md"}
            ]
        )
        transition = Transition(
            tag="fork",
            target="WORKER.md",
            attributes={"next": "WRONG.md"},
            payload=""
        )
        
        with pytest.raises(PolicyViolationError, match="is not allowed"):
            validate_transition_policy(transition, policy)

    def test_validate_multiple_allowed_combinations(self):
        """Test that multiple allowed combinations work correctly."""
        policy = Policy(
            allowed_transitions=[
                {"tag": "goto", "target": "NEXT.md"},
                {"tag": "goto", "target": "DONE.md"},
                {"tag": "reset", "target": "START.md"},
                {"tag": "result"}
            ]
        )
        
        # All of these should pass
        validate_transition_policy(
            Transition(tag="goto", target="NEXT.md", attributes={}, payload=""),
            policy
        )
        validate_transition_policy(
            Transition(tag="goto", target="DONE.md", attributes={}, payload=""),
            policy
        )
        validate_transition_policy(
            Transition(tag="reset", target="START.md", attributes={}, payload=""),
            policy
        )
        validate_transition_policy(
            Transition(tag="result", target="", attributes={}, payload=""),
            policy
        )
