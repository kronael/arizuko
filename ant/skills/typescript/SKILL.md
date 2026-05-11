---
name: typescript
description: >
  TypeScript and Node.js patterns — React, Next.js App Router, Bun,
  Tailwind theming, Playwright, class-validator. USE for "write
  TypeScript", "fix this .ts file", .ts/.tsx files, package.json, bun
  scripts, Next.js, React components, Playwright tests. NOT for plain
  shell scripts (use bash).
user-invocable: true
---

# TypeScript Style

## Code style

- Arrow functions: `const f = (args): Result => { ... }`
- Match existing code style when changing code
- Braces ALWAYS on `if` bodies (no single-line `if (x) return`)
- Name your types — no inline/anonymous object types (tests exempt):

  ```ts
  // Bad
  cache.get<{ status: number; message: string }>()
  // Good
  interface CachedError { status: number; message: string }
  cache.get<CachedError>()
  ```

- Minimize type proliferation: reuse existing types

## Array ops

Methods like `filter()`, `map()`, `slice()` already return new arrays — don't
spread them again:

```ts
// Wrong
const sorted = [...validators].filter(v => v.active).sort(compare)
// Right
const sorted = validators.filter(v => v.active).sort(compare)
```

NEVER `arr.push(...otherArr)` — blows the call stack at 65k+ items. Use
`concat`, `flat`, `flatMap`, or a loop.

## Design

- Inline single-use one-liners; don't wrap `x?.map(fn)` in a named function
- No JSDoc on self-explanatory functions
- Library barrel `index.ts`: `export * from './module'`

## Validation

Never trust external I/O with `as Type`. Validate with class-validator:

```ts
class ApiResponse {
  @IsString() status: string
}
const validated = await validateAndReturn(data, ApiResponse)
```

For nested objects: `@Type(() => Nested)` + `@ValidateNested()`.

## Runtime & packaging

- `bun` for install and unit tests
- Next.js 15 App Router, React Server Components default, `"use client"` only
  when needed
- Tailwind via theme variables (`bg-card`, `text-foreground`, `border-border`);
  never hardcode hex or palette colors

## Testing

- Unit: `*.test.ts` next to code (Bun)
- E2E: `*.spec.ts` in `playwright/` (Playwright)
- `bunfig.toml` → `[test] root = "src"` so Bun doesn't pick up `*.spec.ts`
