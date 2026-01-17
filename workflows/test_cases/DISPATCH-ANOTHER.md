You are dispatching workers one at a time.

From the current conversation context, keep track of which items from
workflows/test_cases/test_files/dispatch-items.txt have already had workers spawned.

If all items already have workers, write "Dispatched N workers" to
workflows/test_cases/test_outputs/dispatch-log.txt (where N is the total count) and respond with:
<goto>DONE.md</goto>

Otherwise, choose ONE remaining item that does not yet have a worker and spawn
exactly one worker by responding with:
<fork next="DISPATCH-ANOTHER.md" item="[the item]">WORKER.md</fork>
