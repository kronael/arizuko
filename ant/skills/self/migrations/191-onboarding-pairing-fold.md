# 191 — the greeting a new chat gets is now a pairing link

There is **no new tool for you** in this one, and one existing tool behaves
slightly differently. It is worth knowing because it changes what you should
expect to see in a chat that has just arrived.

## What landed

When someone messages an unrouted chat on an onboarding platform, the greeting
they receive is now a **pairing link** — `/pair/<token>`, the same mechanism
your `issue_pairing_link` mints. It used to be a separate onboarding token with
its own 24-hour clock and its own redemption page.

Three things follow from that:

- **One writer owns the identity edge.** Every `acl_membership` claim, whether
  it came from your `issue_pairing_link` or from a greeting, is written by the
  same redemption and stamped `pairing`. That means `unpair` can now reach the
  edges onboarding created. Before, it could not — those were stamped
  differently and were effectively permanent.
- **A greeting link expires in ten minutes, not a day.** Short on purpose: the
  defence is the consent page, not the clock. If someone tells you their link
  stopped working, they do not need an operator — the next message they send in
  that chat gets them a fresh one.
- **The chat now hears the outcome.** Where an admission gate refuses an
  account, the refusal used to be an HTTP error on a page nobody was looking at
  by then. It arrives in the chat instead.

## What this changes for you

`issue_pairing_link` is unchanged — same arguments, same rule that the JID must
already route to your folder, same ten-minute window.

The thing to watch for: a chat can be **linked but not routed**. Pairing binds
an identity; it does not decide where a chat's messages go. A user who has just
paired still has to choose a world in the browser, and until they do, their
chat stays silent — that is expected, not a fault. If someone says "I clicked
the link and nothing happened," the answer is usually that they closed the page
before the last step.

Specs: `specs/5/31-identity-pairing.md`, `specs/5/18-onboarding-model.md`;
BUGS `P1b`.
