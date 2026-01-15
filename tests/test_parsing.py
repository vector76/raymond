import pytest
from src.parsing import parse_transitions, Transition, validate_single_transition


class TestParseTransitions:
    """Tests for parse_transitions() function."""

    def test_parse_goto(self):
        """Test parsing <goto>FILE.md</goto> tag."""
        output = "Some text here\n<goto>FILE.md</goto>"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "FILE.md"
        assert transitions[0].attributes == {}
        assert transitions[0].payload == ""

    def test_parse_reset(self):
        """Test parsing <reset>FILE.md</reset> tag."""
        output = "Some text\n<reset>FILE.md</reset>"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].tag == "reset"
        assert transitions[0].target == "FILE.md"
        assert transitions[0].attributes == {}
        assert transitions[0].payload == ""

    def test_parse_result_with_payload(self):
        """Test parsing <result>payload</result> tag."""
        output = "Work complete\n<result>Task finished successfully</result>"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].tag == "result"
        assert transitions[0].target == ""
        assert transitions[0].attributes == {}
        assert transitions[0].payload == "Task finished successfully"

    def test_parse_function_with_attributes(self):
        """Test parsing <function return="X.md">Y.md</function> tag."""
        output = "<function return=\"X.md\">Y.md</function>"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].tag == "function"
        assert transitions[0].target == "Y.md"
        assert transitions[0].attributes == {"return": "X.md"}
        assert transitions[0].payload == ""

    def test_parse_call_with_attributes(self):
        """Test parsing <call return="X.md">Y.md</call> tag."""
        output = "<call return=\"X.md\">Y.md</call>"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].tag == "call"
        assert transitions[0].target == "Y.md"
        assert transitions[0].attributes == {"return": "X.md"}
        assert transitions[0].payload == ""

    def test_parse_fork_with_attributes(self):
        """Test parsing <fork next="X.md" item="foo">Y.md</fork> tag."""
        output = "<fork next=\"X.md\" item=\"foo\">Y.md</fork>"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].tag == "fork"
        assert transitions[0].target == "Y.md"
        assert transitions[0].attributes == {"next": "X.md", "item": "foo"}
        assert transitions[0].payload == ""

    def test_tag_anywhere_in_text(self):
        """Test that tag can appear anywhere in text, not just last line."""
        output = "Beginning\n<goto>MIDDLE.md</goto>\nEnd of text"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "MIDDLE.md"

    def test_zero_tags(self):
        """Test that zero tags returns empty list."""
        output = "Just some text with no tags"
        transitions = parse_transitions(output)
        assert transitions == []

    def test_multiple_tags(self):
        """Test that multiple tags returns list with multiple items."""
        output = "<goto>A.md</goto>\n<goto>B.md</goto>"
        transitions = parse_transitions(output)
        assert len(transitions) == 2
        assert transitions[0].tag == "goto"
        assert transitions[0].target == "A.md"
        assert transitions[1].tag == "goto"
        assert transitions[1].target == "B.md"

    def test_path_safety_reject_relative_path(self):
        """Test that ../FILE.md is rejected."""
        output = "<goto>../FILE.md</goto>"
        with pytest.raises(ValueError, match="Path.*contains.*separator"):
            parse_transitions(output)

    def test_path_safety_reject_subdirectory(self):
        """Test that foo/bar.md is rejected."""
        output = "<goto>foo/bar.md</goto>"
        with pytest.raises(ValueError, match="Path.*contains.*separator"):
            parse_transitions(output)

    def test_path_safety_reject_windows_path(self):
        """Test that C:\\FILE.md is rejected."""
        output = "<goto>C:\\FILE.md</goto>"
        with pytest.raises(ValueError, match="Path.*contains.*separator"):
            parse_transitions(output)

    def test_path_safety_reject_forward_slash_in_target(self):
        """Test that forward slash in target is rejected."""
        output = "<goto>path/to/file.md</goto>"
        with pytest.raises(ValueError, match="Path.*contains.*separator"):
            parse_transitions(output)

    def test_path_safety_accepts_valid_filename(self):
        """Test that valid filenames are accepted."""
        output = "<goto>FILE.md</goto>"
        transitions = parse_transitions(output)
        assert transitions[0].target == "FILE.md"

    def test_path_safety_accepts_filename_with_dots(self):
        """Test that filenames with dots are accepted."""
        output = "<goto>my.file.name.md</goto>"
        transitions = parse_transitions(output)
        assert transitions[0].target == "my.file.name.md"

    def test_attributes_with_single_quotes(self):
        """Test parsing attributes with single quotes."""
        output = "<function return='X.md'>Y.md</function>"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].attributes == {"return": "X.md"}

    def test_multiple_attributes(self):
        """Test parsing tag with multiple attributes."""
        output = "<fork next=\"X.md\" item=\"foo\" priority=\"high\">Y.md</fork>"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].attributes == {"next": "X.md", "item": "foo", "priority": "high"}

    def test_empty_result_tag(self):
        """Test parsing empty <result></result> tag."""
        output = "<result></result>"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].tag == "result"
        assert transitions[0].payload == ""

    def test_result_with_multiline_payload(self):
        """Test parsing <result> with multiline payload."""
        output = "<result>Line 1\nLine 2\nLine 3</result>"
        transitions = parse_transitions(output)
        assert len(transitions) == 1
        assert transitions[0].payload == "Line 1\nLine 2\nLine 3"

    def test_empty_target_raises_for_goto(self):
        """Test that empty target for goto raises ValueError."""
        output = "<goto></goto>"
        with pytest.raises(ValueError, match="empty target"):
            parse_transitions(output)

    def test_empty_target_raises_for_reset(self):
        """Test that empty target for reset raises ValueError."""
        output = "<reset></reset>"
        with pytest.raises(ValueError, match="empty target"):
            parse_transitions(output)

    def test_empty_target_raises_for_function(self):
        """Test that empty target for function raises ValueError."""
        output = "<function return=\"X.md\"></function>"
        with pytest.raises(ValueError, match="empty target"):
            parse_transitions(output)

    def test_whitespace_only_target_raises(self):
        """Test that whitespace-only target raises ValueError."""
        output = "<goto>   </goto>"
        with pytest.raises(ValueError, match="empty target"):
            parse_transitions(output)


class TestValidateSingleTransition:
    """Tests for validate_single_transition() helper function."""

    def test_single_transition_passes(self):
        """Test that single transition passes validation."""
        transitions = [Transition("goto", "FILE.md", {}, "")]
        validate_single_transition(transitions)

    def test_zero_transitions_raises(self):
        """Test that zero transitions raises exception."""
        with pytest.raises(ValueError, match="Expected exactly one transition"):
            validate_single_transition([])

    def test_multiple_transitions_raises(self):
        """Test that multiple transitions raises exception."""
        transitions = [
            Transition("goto", "A.md", {}, ""),
            Transition("goto", "B.md", {}, "")
        ]
        with pytest.raises(ValueError, match="Expected exactly one transition"):
            validate_single_transition(transitions)
