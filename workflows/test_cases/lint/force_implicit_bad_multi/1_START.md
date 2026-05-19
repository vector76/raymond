---
allowed_transitions:
  - tag: goto
    target: A
  - tag: goto
    target: B
force_implicit: true
---
This is invalid: force_implicit requires exactly one allowed transition.
<goto>A</goto>
<goto>B</goto>
