---
allowed_transitions:
  - { tag: result }
---
The human reviewer has provided their decision: {{result}}

Read `vendor_research.md` for the full research context.

Write a final recommendation report to `vendor_report.md` that includes:
1. **Vendor Summary** — from the research phase.
2. **Human Decision** — the approval or rejection with stated reason.
3. **Recommendation** — final recommendation incorporating both the analysis
   and the human decision.

STOP after writing the report file.

When the report is written, respond with exactly:
<result>Report written to vendor_report.md</result>
