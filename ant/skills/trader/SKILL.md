---
name: trader
description: Trading bot patterns — state machines, order management, exchange APIs, WebSocket feeds, paper mode.
when_to_use: Use when building or modifying trading bots.
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
