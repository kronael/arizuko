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

export function providerFailure(reason, resetAt, now) {
  const rule = Object.hasOwn(policy.provider_reasons, reason) ? policy.provider_reasons[reason] : undefined
  if (!rule) return { kind: 'permanent', retryAt: 0 }
  const future = Number.isFinite(resetAt) && resetAt > now
  const kind = future && rule.kind_with_future_reset ? rule.kind_with_future_reset : rule.kind
  return { kind, retryAt: kind === 'transient' && future ? resetAt : 0 }
}

export function httpFailure(status, retryAfter, error, now) {
  let timeout = false
  let transport = false
  for (let current = error; current; current = current.cause) {
    if (current.name === 'AbortError' || current.code === 'ABORT_ERR')
      return { kind: 'canceled', retryAt: 0 }
    timeout ||= current.name === 'TimeoutError' || ['ETIMEDOUT', 'UND_ERR_CONNECT_TIMEOUT'].includes(current.code)
    transport ||= ['ECONNRESET', 'ECONNREFUSED', 'EPIPE', 'ENOTFOUND', 'EAI_AGAIN', 'UND_ERR_SOCKET'].includes(current.code) || (current.name === 'TypeError' && ['Failed to fetch', 'NetworkError when attempting to fetch resource.'].includes(current.message))
  }
  if (!status && timeout)
    return { kind: 'timeout', retryAt: 0 }
  if (!(status >= policy.transient_status_range[0] && status <= policy.transient_status_range[1]) && !policy.transient_statuses.includes(status) && !(!status && transport))
    return { kind: 'permanent', retryAt: 0 }
  const value = (retryAfter || '').trim()
  const retryAt = /^\d+$/.test(value) ? Math.min(now + Number(value) * 1000, policy.latest_retry_ms) : httpDate(value, now)
  return { kind: 'transient', retryAt: Number.isFinite(retryAt) ? retryAt : 0 }
}

function httpDate(value, now) {
  const months = ['jan', 'feb', 'mar', 'apr', 'may', 'jun', 'jul', 'aug', 'sep', 'oct', 'nov', 'dec']
  const shortDay = '(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)'
  const longDay = '(?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday)'
  let match = value.match(new RegExp(`^${shortDay}, (\\d{2}) (\\w{3}) (\\d{4}) (\\d{2}):(\\d{2}):(\\d{2}) GMT$`, 'i'))
  let rfc850 = false
  if (!match) {
    match = value.match(new RegExp(`^${longDay}, (\\d{2})-(\\w{3})-(\\d{2}) (\\d{2}):(\\d{2}):(\\d{2}) GMT$`, 'i'))
    rfc850 = !!match
  }
  if (!match) {
    const ascii = value.match(new RegExp(`^${shortDay} (\\w{3}) ( [1-9]|[0-2]\\d|3[01]) (\\d{2}):(\\d{2}):(\\d{2}) (\\d{4})$`, 'i'))
    if (!ascii) return 0
    match = [ascii[0], ascii[2].trim(), ascii[1], ascii[6], ascii[3], ascii[4], ascii[5]]
  }
  const [, dayText, monthText, yearText, hourText, minuteText, secondText] = match
  if (value.includes(',') && !value.endsWith(' GMT')) return 0
  const month = months.indexOf(monthText.toLowerCase()), day = Number(dayText), hour = Number(hourText), minute = Number(minuteText), second = Number(secondText)
  let year = Number(yearText)
  if (month < 0 || day < 1 || hour > 23 || minute > 59 || second > 59) return 0
  const cutoff = new Date(now)
  cutoff.setUTCFullYear(cutoff.getUTCFullYear() + 50)
  if (rfc850) {
    year += Math.floor(cutoff.getUTCFullYear() / 100) * 100
  }
  const build = () => { const date = new Date(0); date.setUTCFullYear(year, month, day); date.setUTCHours(hour, minute, second, 0); return date }
  let date = build()
  if (rfc850 && date > cutoff) { year -= 100; date = build() }
  if (date.getUTCFullYear() !== year || date.getUTCMonth() !== month || date.getUTCDate() !== day) return 0
  return date.getTime()
}

export function notice(cause, decision) {
  return policy.notices[decision.stop ? 'terminal' : 'retry'].replaceAll('{cause}', cause)
}
