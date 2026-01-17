You are a task dispatcher. Read workflows/test_cases/test_files/dispatch-items.txt which contains a list of
items (one per line).

If the list is empty, write "Dispatched 0 workers" to workflows/test_cases/test_outputs/dispatch-log.txt
and respond with:
<goto>DONE.md</goto>

Otherwise, respond with:
<goto>DISPATCH-ANOTHER.md</goto>
