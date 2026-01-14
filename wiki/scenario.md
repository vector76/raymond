# Introduction

This project is a harness to help automate AI agent programs like Claude Code
or Amp or similar.

Claude Code is good at executing toward a single goal, but it is not built to
chain multiple tasks or run continuous loops without human interaction.

A key piece of the design is to intelligently control the context window. For
example, multiple revisions of a plan should ideally be evicted from the
context window, keeping only the final best plan. A test result could be
evaluated in its own context window, returning only the relevant chunk to the
parent context window.

Restarting frequently from scratch, as Ralph does, is one approach, but this
always rebuilds the context rather than selectively keeping the good parts. To
be honest, I don't have extensive experience with Ralph, but my expectation is
that this slows it down and wastes tokens to reestablish the context for each
iteration. Ralph is also responsible for the orchestration layer (as far as I
can tell) and may not be amenable to micro-managing the workflow, like revising
the plan multiple times and refining the implementation multiple times.

# Scenario 1

- A human interacts with Claude Code, prompting with "Start work on issue
  bd-195 and create a high-level plan of attack in plan-195.md." Let's assume
  the system prompt provides the context so Claude Code knows how to claim the
  issue and what is desirable in the plan file, e.g. mostly strategy and no
  code in the plan. Claude Code generates the file as requested.
- The human then says "refine and improve the plan, thinking carefully about
  consequences and potential pitfalls, and update the file plan-195.md to
  reflect the improved plan." Claude Code reviews plan-195.md and finds some
  opportunities for improvement and updates the plan.
- The human then says again "refine and improve the plan, thinking carefully
  about consequences and potential pitfalls, and update the file plan-195.md
  to reflect the improved plan." Claude Code reviews plan-195.md and finds a
  few additional opportunities for improvement and updates the plan again.
- The human then says "implement the feature according to the plan laid out in
  plan-195.md." Claude Code makes an attempt at implementing the feature and
  executes the tests, and the tests fail. Claude Code detects the failures and
  continues working until the tests pass. Claude Code declares success.
- The human then says "review the code and look for any mistakes or areas
  where it could be better." Claude Code reviews the implementation and finds
  one significant bug and a couple edge cases and corrects them.
- The human then says "review the code and look for any mistakes or areas
  where it could be better." Claude Code reviews the implementation and finds
  a couple places where the comments are slightly confusing and corrects them.
- The human is satisfied and says "commit and push and close the issue."
  Claude code generates a commit message based on the implementation, and
  performs a commit and push and closes the issue (bd-195).

## Observations

- In this scenario, supposing the steps were fixed, a single prompt at the
  beginning could specify all the steps but would not produce results that are
  as good as multiple prompts.
- The multiple revisions of the plan is wasted context when performing the
  implementation. Given the choice, we would prefer to discard the iterations
  of plan refinement and keep only the final document.
- The multiple revisions of the implementation should be kept within the
  context, as they are needed for creation of the commit message.

## Additional

Now consider an additional step after each code review step: based on the last 
message from Claude Code (i.e. the review/fix step), there is a conditional 
step that decides whether to stop, run another iteration, or if Claude Code 
had terminated with a question like "shall I fix these", the next user message 
should be "yes fix them".

An LLM prompt could pose this problem to evaluate the final message and guide 
the next steps in the workflow.  This prompt would not need the entire context 
and would be invoked as a context-free function.  This evaluation could 
potentially use a different model like Haiku for cost and speed.

It's very desirable to have this sort of feedback that evaluates the 
trajectory and takes action accordingly.

Another situation would be if the implementation is getting "stuck" and going 
in circles, an evaluator could detect this fact and break out to an error 
path, perhaps wiping the entire task and starting fresh, or escalating to a 
human.

Or even if Claude Code is not stuck and failing, it might uncover a question 
where it would prefer to have input from a user.  If the orchestrator allows 
it, it could pose the question to the human and then continue.
