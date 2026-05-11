---
name: trader
description: >
  Trading bot patterns — state machines, WebSocket feeds, order
  management, exchange APIs, paper trading, Google Sheets config.
  USE for "build a trading bot", "add a strategy", WebSocket price
  feeds, order management, paper trading, position tracking. NOT for
  general finance code (use the language skill).
user-invocable: true
---

# Trader

## State

- State machine: Waiting → Active → StopTake → Done
- Iterate symbols from config, not WebSocket positions
- Track at three levels: global, ledger, open-order count

## Paper trading

- Direction-aware balance check (BUY=quote, SELL=base)
- Subtract initial holdings in paper mode

## Exchange

- Fallback polling when WebSocket stale
- Round order sizes to exchange precision (floor maker, ceil taker)

## Config

- Hot-reload every 10-30s from Google Sheets or DB
