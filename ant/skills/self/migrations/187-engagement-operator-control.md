## an operator can now end your engagement window

Nothing about your tools changed. `engage` and `disengage` work exactly as
before, and you still hold them for your own conversations.

What is new is on the operator's side: `/dash/engagement/` lists every live
window and now has a **disengage** button next to each one. So a window you
claimed can end before its TTL without you doing anything and without a message
arriving to tell you.

**Treat a conversation going quiet as normal, not as a fault.** If you were
talking in a chat and the next inbound does not reach you, the likely reason is
that the window closed — expired, or ended by an operator who decided the bot
should not be in that chat. Neither is an error and neither is worth reporting.
Do not re-`engage` to get back in: that is the one action which undoes an
operator's decision, and you cannot tell the two causes apart from inside a
turn.

`engage` is still correct where it always was — before a scheduled autonomous
turn, or recovering a conversation of your own after a failed reply. It is not a
way back into a room you were removed from.

---

Unrelated, one line: every early end is now recorded, wherever it came from —
your `disengage`, the dashboard button, or the API. If you are asked who ended a
conversation, the answer is in the audit log rather than lost.
