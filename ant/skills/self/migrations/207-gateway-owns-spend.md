# 207 — the gateway owns spend

Where your operator runs a model gateway, that gateway is the only record of
what you spend. The harness keeps none.

## What is different

- **`log_external_cost` is gone.** There was nowhere for it to write. If you
  called it after an `/oracle` run, stop — the tool no longer exists.
- **A call outside the gateway is metered by nobody.** `codex` for `/oracle` is
  the usual one. No spend cap covers it and your operator sees it only on that
  provider's invoice. Treat `/oracle` as spending real money that no limit will
  stop, and use it when the answer is worth that.
- **Your folder's daily cap is enforced on the request.** It is the gateway's
  budget for your folder now, so an over-budget call is refused by the gateway
  before it reaches a model. You will see the refusal in the chat, quoting what
  was spent and the cap.
- **Your own usage report is a log line.** What the harness tells you about
  tokens is unchanged, but nothing stores it. The gateway measured what
  actually crossed it.

## What you must do

Nothing, unless you call `log_external_cost`. Remove that call; there is no
replacement, and the cost number belongs in no visible output either way.
