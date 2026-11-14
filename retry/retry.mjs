import policy from './policy.json' with { type: 'json' }

export function next(failures, failure, now, deadline = 0) {
  failures++
  if (!['transient', 'timeout'].includes(failure.kind) || failures >= policy.max_failures)
    return { failures, nextAt: 0, stop: true }
  const delay = Math.min(policy.base_ms * 2 ** (failures - 1), policy.max_ms)
  const nextAt = Math.max(now + delay, failure.retryAt || 0)
  if (deadline && nextAt >= deadline)
    return { failures, nextAt: 0, stop: true }
  return { failures, nextAt, stop: false }
}

export function httpFailure(status, retryAfter, error, now) {
  if (error?.name === 'AbortError' || error?.code === 'ABORT_ERR')
    return { kind: 'canceled', retryAt: 0 }
  const code = error?.code ?? error?.cause?.code
  if (error?.name === 'TimeoutError' || ['ETIMEDOUT', 'UND_ERR_CONNECT_TIMEOUT'].includes(code))
    return { kind: 'timeout', retryAt: 0 }
  const transport = ['ECONNRESET', 'ECONNREFUSED', 'EPIPE', 'ENOTFOUND', 'EAI_AGAIN', 'UND_ERR_SOCKET'].includes(code) || (error?.name === 'TypeError' && ['Failed to fetch', 'NetworkError when attempting to fetch resource.'].includes(error.message))
  if (!(status >= policy.transient_status_range[0] && status <= policy.transient_status_range[1]) && !policy.transient_statuses.includes(status) && !transport)
    return { kind: 'permanent', retryAt: 0 }
  const value = (retryAfter || '').trim()
  const retryAt = /^\d+$/.test(value) ? Math.min(now + Number(value) * 1000, policy.latest_retry_ms) : Date.parse(value)
  return { kind: 'transient', retryAt: Number.isFinite(retryAt) ? retryAt : 0 }
}

export function notice(cause, decision) {
  return policy.notices[decision.stop ? 'terminal' : 'retry'].replaceAll('{cause}', cause)
}
