---
allowed_transitions:
  - { tag: ask, next: 3_REPORT.md }
---
Read `vendor_research.md` and prepare a concise approval request for a human
reviewer.

Summarize the key findings in 3-5 bullet points, then state the recommended
action (approve or reject) with reasoning.

STOP after preparing the summary. Do not write the final report — that happens
in a later step.

Present the summary and emit an ask tag to pause for human approval:
<ask next="3_REPORT.md">Vendor evaluation complete. Please review and respond with "approved" or "rejected: [reason]".</ask>
