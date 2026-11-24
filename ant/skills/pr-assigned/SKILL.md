---
name: pr-assigned
description: >
  The routine for a GitHub pull request that is assigned to you. USE when a
  message says "assigned PR #<n>" or "review_requested PR #<n>" with verb
  `mention`, and when a person asks for the routine: "adversarial review",
  "make it minimal", "defluff", "review, fix and merge this PR", "take this
  PR to merge". NOT for a change that has no pull request (use refine).
user-invocable: true
---

# PR assigned

An assignment makes the pull request yours until you merge it or report what
blocks it. The same text with a verb other than `mention` names another
assignee: observe it only.

- The assignment is the instruction for steps 1 to 5 (`CLAUDE.md` § Task
  discipline). NEVER ask for an approval or a go-ahead for one of them.
- A request that names some of the steps asks for those steps, in this order.
- A review request alone asks for step 1. ALWAYS post the findings as review
  comments, and push nothing.
- ALWAYS read the repository's instruction files before step 1. A repository
  rule replaces the step that it contradicts.

1. **Adversarial review.** ALWAYS start one agent that did not write the
   change. It reads the full diff against the base branch and each open review
   thread. Tell it to prove the change wrong. It looks for a description claim
   that the code contradicts, an input that breaks the change, and a smaller
   change that does the same job. When its model fails or is over its limit,
   ALWAYS use another model that you can reach, and name both models in the
   reply. NEVER ask which model to use.
2. **Refine.** ALWAYS run `/refine` on the full diff and keep the smallest
   change that does the job. ALWAYS cut:
   - a comment that tells history or repeats the code;
   - a test that cannot fail, and a test that only helped you write the change;
   - an option, a rename, a move or a feature that nobody asked for;
   - code for a case that does not occur.
3. **Fix.** ALWAYS fix each finding and each open review comment, then answer
   its thread and resolve it. When a fix changes behaviour, do step 1 one more
   time on the new diff. Two rounds are the limit.
4. **Verify.** ALWAYS run the repository's tests, type checks and lint on the
   final head, and fix a CI failure that the change causes. Where the
   repository has a mutation run, ALWAYS run it on the changed lines. ALWAYS
   make the title and the description true for the final head.
5. **Release.** When the repository's rules require a version or a changelog
   entry for this change, ALWAYS add it with `/release` before the merge.
6. **Merge.** ALWAYS merge when the instruction says so, or when the
   repository's rules allow a merge without one. `CLAUDE.md` § Task discipline
   says which checks do not stop a merge. Without an instruction or such a
   rule, say that the pull request is ready.
7. **Reply.** ALWAYS send one reply in the form `CLAUDE.md` § Response size +
   medium gives for GitHub: the head commit, what you cut, the checks that
   ran, and "merged" or the one thing that blocks the merge.
