# Vendor Approval Skill

Evaluates a vendor against company policy and routes through human approval
before generating a recommendation report.

## Inputs

| Variable | Required | Description |
|----------|----------|-------------|
| `INPUT` | Yes | Vendor details — name, proposed budget, and any context for the evaluator. Passed via `--input` on initial run. |
| `BUDGET` | No | USD budget for LLM usage (default: `5.00`). |

Example:

```bash
INPUT="vendor_name=Acme Corp, budget_limit=50000, category=SaaS" \
  ./run.sh run
```

## Outputs

On success (exit code 0), the workflow writes:

- `vendor_report.md` — a structured recommendation report in the working
  directory, containing the research summary, approval status, and final
  recommendation.

## Human Input Expectations

This skill **pauses once** for human approval. After the research phase
completes, the workflow emits an `<await>` with a prompt summarizing the
vendor evaluation and asking for approval or rejection.

**Expected await cycle:**

1. **First run** exits with code 2. The JSON `awaiting.prompt` will contain the
   vendor summary and ask: "Approve or reject this vendor?"
2. **Resume** with the human's decision (e.g., `"approved"` or
   `"rejected: insufficient security documentation"`).
3. The workflow generates the final report and exits with code 0.

If the human rejects the vendor, the report reflects the rejection with the
stated reason. No further input is needed either way.

## Invocation

### Single run (no await)

If you want to skip human approval and let the agent decide autonomously, do
not use this skill — it is designed for human-in-the-loop workflows.

### Standard invocation with resume loop

```bash
# Initial run
INPUT="vendor_name=Acme Corp, budget_limit=50000" \
  ./run.sh run > output.json
exit_code=$?

# Resume loop for human input
while [ "$exit_code" -eq 2 ]; do
  prompt=$(jq -r '.awaiting.prompt' output.json)
  run_id=$(jq -r '.run_id' output.json)

  echo "Approval needed: $prompt"
  read -rp "Decision> " decision

  RUN_ID="$run_id" INPUT="$decision" \
    ./run.sh resume > output.json
  exit_code=$?
done

if [ "$exit_code" -eq 0 ]; then
  echo "Report written to vendor_report.md"
else
  echo "Workflow failed" >&2
fi
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Workflow completed — report written. |
| 1 | Error (invalid input, LLM failure, etc.). |
| 2 | Awaiting human approval — parse JSON from stdout and resume. |
