---
name: sql
description: >
  Write SQL queries, design schemas, or create database migrations.
  Use when working with tables, joins, or DDL.
---

# SQL

## Style

- No `AS` for column aliases: `MAX(rtime) max_rtime`
- `JOIN ... USING (col)` when columns share names
- Direct JOINs over `WHERE x IN (SELECT ...)` subqueries
- `WHERE enabled` not `WHERE enabled = true`
- `ON CONFLICT ... DO UPDATE SET` — each assignment on its own line
- `RETURNING` on its own line
- CTEs stacked before the main query

## Embedded SQL

- Clause keywords (SELECT, FROM, WHERE, GROUP BY) at the same indent
- Single column/condition: same line as the keyword
- Multiple: one per line, indented two spaces

## Migrations

- One migration per change, never modify existing
- User adds indexes (remind, don't add them)
- Stored-proc params suffix `_param`, locals suffix `_var`
- Dynamic SQL: `format()` with `%I` (identifiers), `%s` (values)
