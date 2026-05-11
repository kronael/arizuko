---
name: rust
description: >
  Rust code patterns — tokio, clap, eyre, Cargo, testing,
  panic/unwrap discipline. USE for "write Rust", "fix this .rs file",
  .rs files, Cargo.toml, cargo clippy, async/await with tokio, DashMap,
  testcontainers. NOT for system scripts (use bash).
user-invocable: true
---

# Rust

## Imports

One per line (cleaner diffs):

```rust
use tracing::info;
use tracing::debug;
```

## Naming

- Full words for params: `value` not `v`, `count` not `c`
- Single letters OK for loop indices and math (`i`, `n`, `k`)
- Macro meta-vars: `$a`, `$b`, `$key`, `$rest` fine

## Design

- Explicit enum states, not implicit flags
- Direct field access with interior mutability; no accessor methods
- Document lock acquisition order to prevent deadlocks
- `#[repr(i16)]` enums map to smallint DB columns

## Unwrap safety

- NEVER `unwrap()` on hot path without `// SAFETY:` comment
- Startup: `expect("descriptive msg")` is fine (fail-fast)
- Mutex: `.lock().unwrap_or_else(|e| e.into_inner())` to recover poison

## Panic handling

- Set panic hook to `exit(1)` in every binary; wrap `main` in a retry loop:

```rust
fn main() {
    std::panic::set_hook(Box::new(|_| std::process::exit(1)));
    loop {
        match run() {
            Ok(()) => break,
            Err(e) => {
                tracing::error!("crashed: {e}, restarting in 5s");
                std::thread::sleep(Duration::from_secs(5));
            }
        }
    }
}
```

## Async

Name coroutines as functions; spawn as a one-liner:

```rust
async fn fetch_and_process(client: Client) { ... }
tokio::spawn(fetch_and_process(client));
```

Named coros are greppable and show in backtraces.

## Testing

- Unit: `#[cfg(test)]` module in same file
- Integration: `tests/` dir, shared setup in `tests/common/mod.rs`
- `--test-threads=1` if using global state

## Build

- Debug builds by default (3x faster, better errors)
- `cargo check` fastest for error-only iteration
