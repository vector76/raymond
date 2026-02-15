# WSL Integration Notes

This document captures findings from testing Windows Subsystem for Linux (WSL)
integration for running Unix shell scripts on Windows.

## Invoking WSL from Git Bash

When running commands in Git Bash on Windows, WSL can be invoked but requires
careful handling of paths.

### Path Mangling Issue

Git Bash automatically converts Unix-style paths to Windows paths. This breaks
WSL commands because WSL expects Linux paths like `/mnt/c/...`.

**Broken (Git Bash mangles the path):**
```bash
wsl cat /mnt/c/Users/Vector/project/file.txt
# Error: cat: 'C:/Program Files/Git/mnt/c/Users/Vector/project/file.txt': No such file or directory
```

Git Bash converts `/mnt/c/...` to `C:/Program Files/Git/mnt/c/...` because it
thinks it's a relative path starting with `/mnt`.

### Solution: Double Slash

Using a double slash (`//`) at the start of the path prevents Git Bash from
mangling it:

**Working:**
```bash
wsl -- cat //mnt/c/Users/Vector/project/file.txt
```

The `--` separator tells WSL that everything after it is the command to run.
The `//mnt/c/...` path passes through Git Bash unmodified.

### Alternative: Use cmd.exe

Another approach is to invoke WSL through cmd.exe, which doesn't mangle paths:

```bash
cmd.exe /c "wsl cat /mnt/c/Users/Vector/project/file.txt"
```

However, this can have issues with output buffering and interactive sessions.

## Running Shell Scripts via WSL

Shell scripts can be executed via WSL:

```bash
wsl -- bash //mnt/c/Users/Vector/project/script.sh
```

### CRLF Line Ending Issues

Windows and Unix use different line endings:
- **Windows (CRLF):** `\r\n` (carriage return + line feed)
- **Unix (LF):** `\n` (line feed only)

When shell scripts are created or edited on Windows, they often have CRLF line
endings. When bash (via WSL) executes these scripts, it interprets the `\r`
(carriage return) as part of the command, causing errors:

```
//mnt/c/.../script.sh: line 6: $'\r': command not found
//mnt/c/.../script.sh: line 12: $'\r': command not found
```

**What's happening:** Bash reads a line like `echo "hello"\r\n`. It strips the
`\n` (line feed) but the `\r` (carriage return) remains attached to the command.
Bash tries to execute `echo "hello"\r` and the `\r` is treated as part of the
argument or as a separate command.

### Solutions for CRLF Issues

**1. Configure Git to use LF for .sh files:**

Add to `.gitattributes`:
```
*.sh text eol=lf
```

This ensures `.sh` files always use LF line endings, even on Windows.

**2. Convert files with dos2unix:**
```bash
wsl -- dos2unix //mnt/c/path/to/script.sh
```

**3. Use sed to strip carriage returns on the fly:**
```bash
wsl -- bash -c "sed 's/\r$//' //mnt/c/path/to/script.sh | bash"
```

**4. Use tr to remove carriage returns:**
```bash
wsl -- bash -c "tr -d '\r' < //mnt/c/path/to/script.sh | bash"
```

**5. Use Python to permanently convert files:**
```python
# In-place sed via WSL on /mnt/c doesn't work reliably
# Use Python instead:
import glob
for f in glob.glob('path/to/*.sh'):
    with open(f, 'rb') as file:
        content = file.read()
    content = content.replace(b'\r\n', b'\n')
    with open(f, 'wb') as file:
        file.write(content)
```

**Note:** In-place edits via WSL (`sed -i`) on the `/mnt/c` mounted filesystem
often fail silently. The edit appears to succeed but the file remains unchanged.
Use native Windows tools (Python, PowerShell) for permanent file modifications.

## Running Python/Pytest via WSL

WSL can run Python, but it uses the Linux Python installation, not the Windows
one. This means:

- Python packages installed on Windows are not available in WSL
- You need to install dependencies separately in WSL

```bash
# This may fail if pytest isn't installed in WSL's Python
wsl -- bash -c "cd /mnt/c/project && python3 -m pytest tests/"
# Error: /usr/bin/python3: No module named pytest
```

To run pytest via WSL, you would need to:
1. Install Python in WSL (usually pre-installed)
2. Install project dependencies: `pip3 install -r requirements.txt`
3. Then run pytest

## Summary

| Task | Command | Notes |
|------|---------|-------|
| Read file | `wsl -- cat //mnt/c/path/file` | Double slash required |
| Run script | `wsl -- bash //mnt/c/path/script.sh` | May have CRLF issues |
| Run with LF conversion | `wsl -- bash -c "tr -d '\r' < //mnt/c/path/script.sh \| bash"` | Strips CR on the fly |
| Run Python | `wsl -- python3 //mnt/c/path/script.py` | Uses WSL's Python |

## Git Line Ending Normalization

Even with `.gitattributes` configured correctly (`*.sh text eol=lf`), existing
files may retain their original line endings until re-normalized.

**Check file line endings:**
```bash
file path/to/script.sh
# Output includes "with CRLF line terminators" if Windows-style
```

**Re-normalize all files:**
```bash
# Remove all files from index (without deleting from disk)
git rm --cached -r .

# Re-add all files (applies .gitattributes rules)
git add .

# Commit the normalization
git commit -m "Normalize line endings per .gitattributes"
```

This forces git to re-process all files according to `.gitattributes` rules.

## Running Raymond Tests via WSL

With Python and pytest installed in WSL, the Raymond test suite can be run.

### Important: Use Login Shell (`bash -l`)

When invoking WSL from Git Bash, you must use `bash -l` (login shell) to ensure
`~/.local/bin` is on the PATH. This is where tools like `claude` and `pytest`
are typically installed.

**Why this matters:**
- Non-interactive shells (`bash -c`) don't source `~/.profile` or `~/.bashrc`
- Tools installed via `pip install --user` go to `~/.local/bin`
- Without `-l`, these tools won't be found: `FileNotFoundError: 'claude'`

```bash
# WRONG - claude not found (non-login shell)
wsl -- bash -c "cd /mnt/c/path && python3 -m pytest tests/"

# CORRECT - login shell sources .profile, finds claude
wsl -- bash -l -c "cd /mnt/c/path && python3 -m pytest tests/"
```

### Setup and Running Tests

```bash
# Install dependencies (one-time setup)
wsl -- pip3 install --break-system-packages -r //mnt/c/path/to/raymond/requirements.txt

# Run full test suite (use -l for login shell!)
wsl -- bash -l -c "cd /mnt/c/path/to/raymond && python3 -m pytest tests/ -q"

# Run only Unix-specific tests
wsl -- bash -l -c "cd /mnt/c/path/to/raymond && python3 -m pytest tests/ -v -k 'unix'"

# Run Claude Code wrapper tests (requires claude CLI installed in WSL)
wsl -- bash -l -c "cd /mnt/c/path/to/raymond && python3 -m pytest tests/test_cc_wrap.py -v"
```

**Test results comparison:**

| Platform | Passed | Skipped | Failed | Notes |
|----------|--------|---------|--------|-------|
| Windows  | 333    | 59      | 0      | Unix tests skipped |
| WSL/Unix | 328    | 64      | 0      | Windows tests skipped (with claude CLI installed) |

If Claude Code CLI is not installed in WSL, the 6 `test_cc_wrap.py` tests will
fail with `FileNotFoundError: 'claude'`.

## Recommendations for Raymond

For Raymond's script state testing on Windows:

1. **Production use:** Windows users should use `.bat` files, not `.sh` files.
   The orchestrator correctly routes to platform-appropriate scripts.

2. **Development/testing:** If Unix tests need to run on Windows:
   - Ensure `.gitattributes` has `*.sh text eol=lf` (already configured)
   - Re-normalize files if they still have CRLF endings
   - Use WSL to run Unix-specific tests: `wsl -- bash -c "cd /mnt/c/... && python3 -m pytest -k unix"`

3. **CI/CD:** Run the full test suite (including Unix tests) on a Linux CI
   runner to ensure complete coverage.
