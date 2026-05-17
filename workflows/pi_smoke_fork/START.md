---
allowed_transitions:
  - { tag: fork, target: WORKER.md, next: JOIN.md }
---
Spawn one parallel worker and advance to the join state.

Respond with exactly:

<fork next="JOIN.md">WORKER.md</fork>
