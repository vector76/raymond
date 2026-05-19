---
allowed_transitions:
  - tag: ask
    next: DONE
force_implicit: true
---
Invalid: ask cannot be implicit (LLM must compose the prompt).
<ask next="DONE">prompt</ask>
