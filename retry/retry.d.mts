export type Failure = { kind: string; retryAt: number }
export type Decision = { failures: number; nextAt: number; stop: boolean }
export function next(failures: number, failure: Failure, now: number, deadline?: number): Decision
export function httpFailure(status: number, retryAfter: string, error: unknown, now: number): Failure
export function notice(cause: string, decision: Decision): string
