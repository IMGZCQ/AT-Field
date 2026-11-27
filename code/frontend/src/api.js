// 统一网关会把应用挂在 /app/xxx/ 子路径下，用页面路径推导 API 前缀
const base = new URL('.', window.location.href).pathname.replace(/\/$/, '')

async function request(path, options) {
  const res = await fetch(base + path, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  })
  const text = await res.text()
  let data = {}
  try {
    data = text ? JSON.parse(text) : {}
  } catch (_) {}
  if (!res.ok) {
    throw new Error(data.error || `请求失败 (${res.status})`)
  }
  return data
}

export const api = {
  overview: () => request('/api/overview'),
  streamUrl: () => base + '/api/stream',
  history: (range = '7d') => request(`/api/health/history?range=${range}`),
  net: (range = '1h') => request(`/api/net?range=${range}`),
  procTraffic: (range = '1d') => request(`/api/net/proctraffic?range=${range}`),
  webSummary: (range = '1d') => request(`/api/web/summary?range=${range}`),
  webPathSources: (path, range = '1d') =>
    request(`/api/web/pathsources?path=${encodeURIComponent(path)}&range=${range}`),
  ipinfo: (ip) => request(`/api/ipinfo?ip=${encodeURIComponent(ip)}`),
  banIp: (ip, duration = '1h') =>
    request('/api/ip/ban', { method: 'POST', body: JSON.stringify({ ip, duration }) }),
  unbanIp: (ip) => request('/api/ip/unban', { method: 'POST', body: JSON.stringify({ ip }) }),
  connections: () => request('/api/net/connections'),
  killConnection: (local, remote) =>
    request('/api/net/connections/kill', { method: 'POST', body: JSON.stringify({ local, remote }) }),
  logs: (params = {}) => request('/api/logs?' + new URLSearchParams({ n: 50, ...params }).toString()),
  alerts: (n = 20) => request(`/api/alerts?n=${n}`),
  alertTest: () => request('/api/alert/test', { method: 'POST' }),
  diagnosticsUrl: () => base + '/api/diagnostics',
  getConfig: () => request('/api/config'),
  saveConfig: (cfg) =>
    request('/api/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(cfg),
    }),
  resetConfig: () => request('/api/config/reset', { method: 'POST' }),
  enable: () => request('/api/shield/enable', { method: 'POST' }),
  disable: () => request('/api/shield/disable', { method: 'POST' }),
}
