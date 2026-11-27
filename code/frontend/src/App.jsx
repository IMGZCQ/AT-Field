import React, { useEffect, useRef, useState, useCallback } from 'react'
import { api } from './api.js'
import worldMap from './worldMap.js'
import chinaMap from './chinaMap.js'

import appIcon from './assets/icon.png'
import appIconHd from './assets/ui/app_icon_hd.png'
import domeImg from './assets/ui/hero_shield.webp'
import badgeOn from './assets/ui/badge_on.png'
import siSync from './assets/ui/ci_sync.png'
import siStatus from './assets/ui/ci_status.png'
import siPower from './assets/ui/ci_power.png'
import siTime from './assets/ui/ci_time.png'
import wmSync from './assets/ui/wm_sync.png'
import wmStatus from './assets/ui/wm_status.png'
import wmPower from './assets/ui/wm_power.png'
import wmTime from './assets/ui/wm_time.png'
import fci1 from './assets/ui/shield_bolt.png'
import fci2 from './assets/ui/radar.png'
import fci3 from './assets/ui/sq_db.png'
import fci4 from './assets/ui/sq_bolt.png'

/* ---------- 线性图标（仅导航等小元素使用） ---------- */
const Icon = ({ name, size = 20 }) => {
  const paths = {
    list: <path d="M4 6h16M4 12h16M4 18h10" />,
    chart: <path d="M4 20V10M10 20V4M16 20v-8M22 20H2" />,
    gear: (
      <path d="M12 15a3 3 0 100-6 3 3 0 000 6zm7.4-3a7.4 7.4 0 00-.1-1.2l2-1.6-2-3.4-2.4 1a7.5 7.5 0 00-2-1.2L14.5 3h-4l-.4 2.6a7.5 7.5 0 00-2 1.2l-2.4-1-2 3.4 2 1.6a7.4 7.4 0 000 2.4l-2 1.6 2 3.4 2.4-1a7.5 7.5 0 002 1.2l.4 2.6h4l.4-2.6a7.5 7.5 0 002-1.2l2.4 1 2-3.4-2-1.6c.06-.4.1-.8.1-1.2z" />
    ),
    info: <path d="M12 2a10 10 0 100 20 10 10 0 000-20zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z" />,
    net: (
      <>
        <path d="M2 11a10 10 0 0120 0" />
        <path d="M6 11a6 6 0 0112 0" />
        <circle cx="12" cy="11" r="1.6" />
        <path d="M12 13v8M8.5 21h7" />
      </>
    ),
    check: <path d="M9 16.2l-3.5-3.5L4 14.2 9 19.2 20 8.2l-1.5-1.5z" />,
    expand: <path d="M9 4H4v5M15 4h5v5M4 15v5h5M20 15v5h-5" />,
  }
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
      {paths[name]}
    </svg>
  )
}

/* ---------- 可放大的面板标题 ---------- */
const PanelTitle = ({ icon, onZoom, children }) => (
  <div
    className={`panel-title${onZoom ? ' zoomable' : ''}`}
    role={onZoom ? 'button' : undefined}
    tabIndex={onZoom ? 0 : undefined}
    title={onZoom ? '点击放大查看' : undefined}
    onClick={onZoom}
    onKeyDown={onZoom ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onZoom() } } : undefined}
  >
    <Icon name={icon} size={16} /> {children}
    {onZoom && <span className="zoom-hint"><Icon name="expand" size={13} /></span>}
  </div>
)

/* ---------- 图表放大弹层（小屏也能看清） ---------- */
const ZoomModal = ({ title, icon = 'chart', onClose, headRight, children }) => {
  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return (
    <div className="zoom-mask" onClick={onClose}>
      <div
        className="zoom-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="zoom-head">
          <strong><Icon name={icon} size={17} /> {title}</strong>
          <div className="zoom-head-right">
            {headRight}
            <button type="button" className="btn btn-mini" onClick={onClose}>关闭</button>
          </div>
        </div>
        <div className="zoom-body">{children}</div>
      </div>
    </div>
  )
}

/* ---------- 折线图 ---------- */

// 把折线点转成柔和曲线：单调三次插值（Fritsch–Carlson）转贝塞尔。
// 既平滑，又不会在两个数据点之间「冲过头」造出并不存在的峰值或负值；
// yMin / yMax 再把控制点夹在绘图区内，防止贴边时穿出画布。
function smoothPath(pts, yMin = -Infinity, yMax = Infinity) {
  const n = pts.length
  if (n < 2) return ''
  const f = (v) => v.toFixed(1)
  const cy = (v) => Math.max(yMin, Math.min(yMax, v))
  if (n === 2) return `M${f(pts[0].x)},${f(pts[0].y)} L${f(pts[1].x)},${f(pts[1].y)}`
  const d = []
  for (let i = 0; i < n - 1; i++) d.push((pts[i + 1].y - pts[i].y) / (pts[i + 1].x - pts[i].x))
  const m = new Array(n)
  m[0] = d[0]
  m[n - 1] = d[n - 2]
  for (let i = 1; i < n - 1; i++) {
    if (d[i - 1] * d[i] <= 0) {
      m[i] = 0
    } else {
      m[i] = (d[i - 1] + d[i]) / 2
      const lim = 3 * Math.min(Math.abs(d[i - 1]), Math.abs(d[i]))
      if (Math.abs(m[i]) > lim) m[i] = Math.sign(m[i]) * lim
    }
  }
  let path = `M${f(pts[0].x)},${f(pts[0].y)}`
  for (let i = 0; i < n - 1; i++) {
    const h = pts[i + 1].x - pts[i].x
    const c1x = pts[i].x + h / 3
    const c1y = cy(pts[i].y + (m[i] * h) / 3)
    const c2x = pts[i + 1].x - h / 3
    const c2y = cy(pts[i + 1].y - (m[i + 1] * h) / 3)
    path += ` C${f(c1x)},${f(c1y)} ${f(c2x)},${f(c2y)} ${f(pts[i + 1].x)},${f(pts[i + 1].y)}`
  }
  return path
}

// 按 Y 轴最长标签估算左侧留白，避免数值被裁掉（viewBox 单位，字号 12.5px）
function axisPad(labels) {
  const longest = labels.reduce((m, s) => Math.max(m, String(s).length), 0)
  return Math.ceil(longest * 7 + 16)
}

// 用 memo 包住：父级每 5 秒被 SSE 触发重渲染时，只要点位/目标值没变就不再重建整棵 SVG
const LineChart = React.memo(function LineChart({ points, height = 220, yMax = 100, target, range = '7d', refs = null }) {
  if (!points || points.length < 2) return <div className="chart-empty">暂无数据</div>
  const W = 620, H = height, padR = 16, padT = 14, padB = 28
  const gridVals = [0, 25, 50, 75, 100].filter((v) => v <= yMax)
  const padL = Math.max(44, axisPad(gridVals.map((v) => `${v}%`)))
  const iw = W - padL - padR, ih = H - padT - padB
  const xs = (i) => padL + (iw * i) / (points.length - 1)
  const ys = (v) => padT + ih - (ih * Math.min(v, yMax)) / yMax
  const pts = points.map((p, i) => ({ x: xs(i), y: ys(p.percent) }))
  const line = smoothPath(pts, padT, padT + ih)
  const area = `${line} L${xs(points.length - 1)},${padT + ih} L${padL},${padT + ih} Z`
  const last = points[points.length - 1]
  const fmt = (ts) => {
    const d = new Date(ts * 1000)
    const p = (n) => String(n).padStart(2, '0')
    if (range === '7d') return `${d.getMonth() + 1}/${d.getDate()}`
    return `${p(d.getHours())}:${p(d.getMinutes())}`
  }
  const lastX = Math.min(xs(points.length - 1), W - padR - 4)
  const lastY = ys(last.percent)
  // 末点标签改成 HTML 元素，字号固定（不随图表缩放变细），贴近顶部时画到点的下方
  const badgeBelow = lastY < 56
  return (
    <div className="chart-wrap">
    <svg viewBox={`0 0 ${W} ${H}`} className="line-chart" preserveAspectRatio="xMidYMid meet">
      <defs>
        <linearGradient id="areaFill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#7c5cff" stopOpacity="0.35" />
          <stop offset="100%" stopColor="#7c5cff" stopOpacity="0" />
        </linearGradient>
      </defs>
      {gridVals.map((v) => (
        <g key={v}>
          <line x1={padL} y1={ys(v)} x2={W - padR} y2={ys(v)} stroke="rgba(255,255,255,0.08)" strokeDasharray="3 4" />
          <text x={padL - 8} y={ys(v) + 4} textAnchor="end" className="axis-label">{v}%</text>
        </g>
      ))}
      {target != null && (
        <line x1={padL} y1={ys(target)} x2={W - padR} y2={ys(target)} stroke="#43e7ad" strokeDasharray="5 5" opacity="0.6" />
      )}
      <path d={area} fill="url(#areaFill)" />
      {refs && refs.filter((v) => v <= yMax).map((v) => (
        <g key={`ref-${v}`}>
          <line x1={padL} y1={ys(v)} x2={W - padR} y2={ys(v)} stroke="rgba(255,255,255,0.22)" strokeDasharray="4 6" />
          {/* 标签贴在绘图区左上，避开 Y 轴刻度与右下角末点数值胶囊；与网格刻度重合的值不再重复标注 */}
          {!gridVals.includes(v) && (
            <text x={padL + 4} y={ys(v) - 5} textAnchor="start" className="ref-label">{v}%</text>
          )}
        </g>
      ))}
      <path d={line} fill="none" stroke="#9b79ff" strokeWidth="3.5" strokeLinejoin="round" />
      {points.map((p, i) => (
        <circle key={i} cx={xs(i)} cy={ys(p.percent)} r={i === points.length - 1 ? 5 : 4} fill={i === points.length - 1 ? '#c3b6ff' : '#a9b5ff'} stroke="#1c1440" strokeWidth="2" />
      ))}
      {points.map((p, i) =>
        i % Math.ceil(points.length / 7) === 0 || i === points.length - 1 ? (
          <text key={i} x={xs(i)} y={H - 8} textAnchor="middle" className="axis-label">{fmt(p.ts)}</text>
        ) : null
      )}
    </svg>
      <div
        className={`chart-current ${badgeBelow ? 'badge-below' : ''}`}
        style={{ '--cx': `${(lastX / W) * 100}%`, top: `${(lastY / H) * 100}%` }}
      >
        {last.percent}%
      </div>
    </div>
  )
})

/* ---------- 安全基线得分构成环形图 ---------- */
const BASELINE_COLORS = ['#43e7ad', '#8b6bff', '#6c9cff', '#f0b45c', '#e0566f', '#b3aada']

const BaselineDonut = React.memo(function BaselineDonut({ items = [] }) {
  const list = (items || []).filter((it) => Number(it.max) > 0)
  if (!list.length) return <div className="chart-empty">暂无数据</div>
  const total = list.reduce((s, it) => s + Number(it.max), 0)
  const earned = list.reduce((s, it) => s + Math.min(Number(it.score), Number(it.max)), 0)
  const C = 2 * Math.PI * 70
  let acc = 0
  return (
    <div className="donut-wrap">
      <div className="donut-chart">
        <div className="donut-figure">
          <svg viewBox="0 0 200 200" className="donut" role="img" aria-label="安全基线得分构成">
            <circle cx="100" cy="100" r="70" fill="none" stroke="rgba(180,189,255,0.12)" strokeWidth="22" />
            {list.map((it, i) => {
              const val = Math.min(Number(it.score), Number(it.max))
              const len = (val / total) * C
              const draw = Math.max(0, len - 1.5)
              const seg = draw > 0 ? (
                <circle
                  key={it.name}
                  cx="100" cy="100" r="70" fill="none"
                  stroke={BASELINE_COLORS[i % BASELINE_COLORS.length]} strokeWidth="22"
                  strokeDasharray={`${draw} ${C - draw}`}
                  strokeDashoffset={-acc}
                  transform="rotate(-90 100 100)"
                />
              ) : null
              acc += len
              return seg
            })}
          </svg>
          <div className="donut-center">
            <b>{earned}<span> / {total}</span></b>
          </div>
        </div>
        <em className="donut-caption">安全基线 · 扣分 {total - earned}</em>
      </div>
      <div className="donut-legend">
        {list.map((it, i) => (
          <span key={it.name}>
            <i style={{ background: BASELINE_COLORS[i % BASELINE_COLORS.length] }} />
            <em className="donut-name">{it.name}</em>
            <b>{Math.min(Number(it.score), Number(it.max))}/{Number(it.max)}</b>
          </span>
        ))}
        {total > earned && (
          <span>
            <i style={{ background: 'rgba(180,189,255,0.25)' }} />
            <em className="donut-name">失分</em>
            <b>{total - earned}</b>
          </span>
        )}
      </div>
    </div>
  )
})

/* ---------- 进度条 ---------- */
const ProgressBar = ({ value, max = 100, tone = 'green' }) => (
  <div className="progress">
    <div
      className={`progress-inner tone-${tone}`}
      style={{ width: `${Math.min(100, (Math.max(0, value || 0) / (max || 100)) * 100)}%` }}
    />
  </div>
)

/* ---------- 状态点 ---------- */
const StatusDot = ({ active }) => <span className={`dot ${active ? 'dot-on' : 'dot-off'}`} />

/* ---------- 统计卡 ---------- */
const StatCard = ({ icon, title, hint, children, watermark, foot, className = '', onClick }) => (
  <div
    className={`stat-card ${className}${onClick ? ' stat-click' : ''}`}
    onClick={onClick}
    role={onClick ? 'button' : undefined}
    tabIndex={onClick ? 0 : undefined}
    onKeyDown={onClick ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onClick() } } : undefined}
  >
    {watermark && <img className="stat-wm" src={watermark} alt="" />}
    <div className="stat-title">
      <img className="stat-icon" src={icon} alt="" /> {title}
      {hint && <span className="stat-hint">?</span>}
    </div>
    {children}
    {foot && <div className="stat-foot">{foot}</div>}
  </div>
)

/* ---------- 防护功能卡 ---------- */
const FeatureCard = ({ img, title, desc, enabled, onClick }) => (
  <button className={`feature-card ${enabled ? 'feature-on' : ''}`} onClick={onClick}>
    <img className="feature-img" src={img} alt="" />
    <div className="feature-title">{title}</div>
    <div className="feature-desc">{desc}</div>
  </button>
)

/* ---------- 时间范围选择 ---------- */
const RANGES = [['1h', '1小时'], ['1d', '1天'], ['7d', '7天']]
// 访问分析额外支持 30 天（聚合数据保留 30 天）
const WEB_RANGES = [['1h', '1小时'], ['1d', '1天'], ['7d', '7天'], ['30d', '30天']]
const RangePicker = ({ value, onChange, items = RANGES }) => (
  <div className="range-picker">
    {items.map(([v, label]) => (
      <button key={v} className={`range-btn ${value === v ? 'range-on' : ''}`} onClick={() => onChange(v)}>{label}</button>
    ))}
  </div>
)

/* ---------- 分段标签 ---------- */
const SegTabs = ({ value, onChange, items }) => (
  <div className="seg-tabs" role="tablist">
    {items.map(([v, label]) => (
      <button
        key={v}
        type="button"
        role="tab"
        aria-selected={value === v}
        className={`seg-btn ${value === v ? 'on' : ''}`}
        onClick={() => onChange(v)}
      >
        {label}
      </button>
    ))}
  </div>
)

/* ---------- 封禁时长选择 ---------- */
const BAN_DURS = [['1h', '1小时'], ['24h', '24小时'], ['7d', '一周'], ['365d', '一年']]
const banDurLabel = (v) => (BAN_DURS.find(([k]) => k === v) || BAN_DURS[0])[1]

/* ---------- IP 归属地展示 ---------- */
// approx：第三方库只查到注册机构级数据（常见于中国 IPv6），省/市是机构地址，不代表实际位置
const GEO_APPROX_NOTE = '地址可能不准'
const geoRegionText = (i = {}) => {
  if (i.private) return i.country || '内网/保留地址'
  if (i.approx) return i.country || '未知'
  return [i.country, i.region, i.city].filter(Boolean).join(' · ') || '未知'
}
const geoLabelText = (i = {}, withNote = true) => {
  const internal = i.private || i.internal
  const base = internal ? i.country || '内网/保留地址' : geoRegionText(i)
  const op = i.isp ? ` · ${i.isp}` : ''
  return `${base}${op}${withNote && i.approx && !internal ? `（${GEO_APPROX_NOTE}）` : ''}`
}

/* ---------- 双线网络速率图 ---------- */
const NetChart = React.memo(function NetChart({ points, range = '1d', height = 210 }) {
  if (!points || points.length < 2) return <div className="chart-empty">暂无数据</div>
  const W = 620, H = height, padR = 16, padT = 14, padB = 28
  const max = Math.max(1, ...points.map((p) => Math.max(p.rx, p.tx)))
  const fmtKB = (v) => (v >= 1024 ? `${(v / 1024).toFixed(1)} MB/s` : `${Math.round(v)} KB/s`)
  const ticks = [1, 0.5, 0]
  const padL = Math.max(54, axisPad(ticks.map((r) => fmtKB(max * r))))
  const iw = W - padL - padR, ih = H - padT - padB
  const xs = (i) => padL + (iw * i) / (points.length - 1)
  const ys = (v) => padT + ih - (ih * Math.min(v, max)) / max
  const curve = (key) => smoothPath(points.map((pt, i) => ({ x: xs(i), y: ys(pt[key]) })), padT, padT + ih)
  const p = (n) => String(n).padStart(2, '0')
  // 7 天以上按「月/日」标注，避免每天同一时刻看起来像重复刻度
  const fmtT = (ts) => {
    const d = new Date(ts * 1000)
    if (range === '7d' || range === '30d') return `${d.getMonth() + 1}/${d.getDate()}`
    return `${p(d.getHours())}:${p(d.getMinutes())}`
  }
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="line-chart" preserveAspectRatio="xMidYMid meet">
      {ticks.map((r) => (
        <g key={r}>
          <line x1={padL} y1={ys(max * r)} x2={W - padR} y2={ys(max * r)} stroke="rgba(255,255,255,0.08)" strokeDasharray="3 4" />
          <text x={padL - 10} y={ys(max * r) + 4} textAnchor="end" className="axis-label">{fmtKB(max * r)}</text>
        </g>
      ))}
      <path d={curve('rx')} fill="none" stroke="#43e7ad" strokeWidth="2.2" strokeLinejoin="round" strokeLinecap="round" />
      <path d={curve('tx')} fill="none" stroke="#8b6bff" strokeWidth="2.2" strokeLinejoin="round" strokeLinecap="round" />
      {points.map((pt, i) =>
        i % Math.ceil(points.length / 7) === 0 || i === points.length - 1 ? (
          <text key={i} x={xs(i)} y={H - 8} textAnchor="middle" className="axis-label">{fmtT(pt.ts)}</text>
        ) : null
      )}
    </svg>
  )
})

/* ---------- 流量用量柱状图（每桶下行 / 上行堆叠） ---------- */
const fmtTraffic = (n) => {
  if (!n) return '0 B'
  if (n >= 1024 ** 4) return `${(n / 1024 ** 4).toFixed(2)} TB`
  if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(2)} GB`
  if (n >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MB`
  if (n >= 1024) return `${Math.round(n / 1024)} KB`
  return `${n} B`
}

const TrafficChart = React.memo(function TrafficChart({ points, range = '1d', height = 210 }) {
  if (!points || points.length === 0) return <div className="chart-empty">暂无数据</div>
  const W = 620, H = height, padR = 16, padT = 14, padB = 28
  const max = Math.max(1, ...points.map((p) => (p.rx || 0) + (p.tx || 0)))
  const ticks = [1, 0.5, 0]
  const padL = Math.max(52, axisPad(ticks.map((r) => fmtTraffic(Math.round(max * r)))))
  const iw = W - padL - padR, ih = H - padT - padB
  const slot = iw / points.length
  const bw = Math.max(4, Math.min(30, slot * 0.56))
  const cx = (i) => padL + slot * i + slot / 2
  const ys = (v) => padT + ih - (ih * Math.min(v, max)) / max
  const barH = (v) => Math.max(v > 0 ? 2 : 0, padT + ih - ys(v))
  const p = (n) => String(n).padStart(2, '0')
  const fmtT = (ts) => {
    const d = new Date(ts * 1000)
    if (range === '7d' || range === '30d') return `${d.getMonth() + 1}/${d.getDate()}`
    return `${p(d.getHours())}:${p(d.getMinutes())}`
  }
  const step = Math.ceil(points.length / 7)
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="line-chart" preserveAspectRatio="xMidYMid meet">
      {ticks.map((r) => (
        <g key={r}>
          <line x1={padL} y1={ys(max * r)} x2={W - padR} y2={ys(max * r)} stroke="rgba(255,255,255,0.08)" strokeDasharray="3 4" />
          <text x={padL - 10} y={ys(max * r) + 4} textAnchor="end" className="axis-label">{fmtTraffic(Math.round(max * r))}</text>
        </g>
      ))}
      {points.map((pt, i) => {
        const rx = Math.max(0, pt.rx || 0)
        const tx = Math.max(0, pt.tx || 0)
        const hRx = barH(rx)
        const hTx = barH(rx + tx) - hRx
        return (
          <g key={i}>
            {hRx > 0 && <rect x={cx(i) - bw / 2} y={padT + ih - hRx} width={bw} height={hRx} rx="2" fill="#43e7ad" opacity="0.92" />}
            {hTx > 0 && <rect x={cx(i) - bw / 2} y={padT + ih - hRx - hTx} width={bw} height={hTx} rx="2" fill="#8b6bff" opacity="0.92" />}
          </g>
        )
      })}
      {points.map((pt, i) =>
        i % step === 0 || i === points.length - 1 ? (
          <text key={i} x={cx(i)} y={H - 8} textAnchor="middle" className="axis-label">{fmtT(pt.ts)}</text>
        ) : null
      )}
    </svg>
  )
})

/* ---------- 访问量柱状图 ---------- */
const WebChart = React.memo(function WebChart({ points, range = '1d', height = 220 }) {
  if (!points || points.length === 0) return <div className="chart-empty">暂无数据</div>
  const hasBad = points.some((pt) => (pt.bad || 0) > 0)
  const W = 620, H = height, padR = 16, padT = 16, padB = 28
  const max = Math.max(1, ...points.map((p) => p.count))
  const ticks = [1, 0.5, 0]
  const padL = Math.max(44, axisPad(ticks.map((r) => String(Math.round(max * r)))))
  const iw = W - padL - padR, ih = H - padT - padB
  const slot = iw / points.length
  // 有异常数据时并排画两根细柱（总请求 / 异常），否则保持单柱观感
  const bw = Math.max(3, Math.min(hasBad ? 22 : 34, slot * (hasBad ? 0.34 : 0.6)))
  const cx = (i) => padL + slot * i + slot / 2
  const ys = (v) => padT + ih - (ih * Math.min(v, max)) / max
  const barH = (v) => Math.max(v > 0 ? 2 : 0, padT + ih - ys(v))
  const p = (n) => String(n).padStart(2, '0')
  const label = (ts) => {
    const d = new Date(ts * 1000)
    if (range === '7d' || range === '30d') return `${d.getMonth() + 1}/${d.getDate()}`
    return `${p(d.getHours())}:${p(d.getMinutes())}`
  }
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="line-chart" preserveAspectRatio="xMidYMid meet">
      <defs>
        <linearGradient id="webBar" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#9b79ff" />
          <stop offset="100%" stopColor="#4a58d9" />
        </linearGradient>
        <linearGradient id="webBarBad" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#ff8a9a" />
          <stop offset="100%" stopColor="#b93250" />
        </linearGradient>
      </defs>
      {ticks.map((r) => (
        <g key={r}>
          <line x1={padL} y1={ys(max * r)} x2={W - padR} y2={ys(max * r)} stroke="rgba(255,255,255,0.08)" strokeDasharray="3 4" />
          <text x={padL - 9} y={ys(max * r) + 4} textAnchor="end" className="axis-label">{Math.round(max * r)}</text>
        </g>
      ))}
      {points.map((pt, i) => {
        const y = ys(pt.count)
        const h = barH(pt.count)
        const bx = hasBad ? cx(i) - bw - 1 : cx(i) - bw / 2
        return (
          <g key={pt.ts}>
            <rect x={bx} y={y} width={bw} height={h} rx="3" fill="url(#webBar)">
              <title>{`${label(pt.ts)} · 总请求 ${pt.count} 次`}</title>
            </rect>
            {hasBad && (pt.bad || 0) > 0 && (
              <rect x={cx(i) + 1} y={ys(pt.bad)} width={bw} height={barH(pt.bad)} rx="3" fill="url(#webBarBad)">
                <title>{`${label(pt.ts)} · 异常 ${pt.bad} 次`}</title>
              </rect>
            )}
            {!hasBad && pt.count > 0 && points.length <= 12 && (
              <text x={cx(i)} y={y - 6} textAnchor="middle" className="bar-value">{pt.count}</text>
            )}
          </g>
        )
      })}
      {points.map((pt, i) =>
        points.length <= 8 || i % Math.ceil(points.length / 7) === 0 || i === points.length - 1 ? (
          <text key={`t${pt.ts}`} x={cx(i)} y={H - 8} textAnchor="middle" className="axis-label">{label(pt.ts)}</text>
        ) : null
      )}
    </svg>
  )
})

/* ---------- 来源地区地图（世界按国家填色，可下钻中国省级） ---------- */
// 主色 #88D450；无数据国家用深底，保证大陆轮廓仍可辨认
const MAP_RGB = '136,212,80'
const MAP_BASE = '#252b4d'

// 34 个省级短名，与 chinaMap.js 的 name 保持一致
const PROVINCES = ['北京', '天津', '河北', '山西', '内蒙古', '辽宁', '吉林', '黑龙江', '上海', '江苏', '浙江', '安徽', '福建', '江西', '山东', '河南', '湖北', '湖南', '广东', '广西', '海南', '重庆', '四川', '贵州', '云南', '西藏', '陕西', '甘肃', '青海', '宁夏', '新疆', '台湾', '香港', '澳门']
// ip-api 的 regionName 可能是英文 / 拼音，补一份别名映射
const PROVINCE_ALIAS = {
  beijing: '北京', tianjin: '天津', hebei: '河北', shanxi: '山西', innermongolia: '内蒙古', mongolia: '内蒙古', neimenggu: '内蒙古', neimongol: '内蒙古',
  liaoning: '辽宁', jilin: '吉林', heilongjiang: '黑龙江', shanghai: '上海', jiangsu: '江苏', zhejiang: '浙江', anhui: '安徽',
  fujian: '福建', jiangxi: '江西', shandong: '山东', henan: '河南', hubei: '湖北', hunan: '湖南', guangdong: '广东', guangxi: '广西',
  hainan: '海南', chongqing: '重庆', sichuan: '四川', guizhou: '贵州', yunnan: '云南', tibet: '西藏', xizang: '西藏', shaanxi: '陕西',
  gansu: '甘肃', qinghai: '青海', ningxia: '宁夏', xinjiang: '新疆', taiwan: '台湾', hongkong: '香港', macau: '澳门', macao: '澳门',
}

// 把 ip-api 的 regionName 归一化为省级短名；无法识别时返回空串
function provinceName(region) {
  if (!region) return ''
  let s = String(region).trim()
  // 去掉行政区后缀，如「广东省」「内蒙古自治区」「香港特别行政区」
  s = s.replace(/(壮族自治区|回族自治区|维吾尔自治区|特别行政区|自治区|省|市)$/, '')
  s = s.replace(/(壮族|回族|维吾尔)$/, '')
  if (PROVINCES.includes(s)) return s
  const key = s.toLowerCase().replace(/[\s_-]/g, '')
  if (PROVINCE_ALIAS[key]) return PROVINCE_ALIAS[key]
  // 兜底：以省份名开头（如「广东深圳」这类拼接值）
  return PROVINCES.find((p) => s.startsWith(p)) || ''
}

const WorldMap = React.memo(function WorldMap({ items = [], provinces = [], mode = 'world', onModeChange, cnTotal = 0 }) {
  // 点击国家 / 省份后固定显示次数（移动端没有悬停，点击更直观）
  const [picked, setPicked] = useState('')
  // 切换世界 / 中国视图时清掉选中项，避免气泡残留
  useEffect(() => { setPicked('') }, [mode])
  const isCN = mode === 'cn'
  // 世界模式按 ISO alpha-2 汇总；内网 / 未知没有国家代码，天然不进入地图
  const byCode = new Map()
  const nameByCode = new Map()
  if (!isCN) {
    items.forEach((it) => {
      const code = (it.code || '').toLowerCase()
      if (!code) return
      byCode.set(code, (byCode.get(code) || 0) + it.count)
      nameByCode.set(code, it.name)
    })
  }
  // 中国模式按省级短名汇总
  const byProv = new Map()
  if (isCN) provinces.forEach((it) => byProv.set(it.name, (byProv.get(it.name) || 0) + it.count))
  const data = isCN ? chinaMap : worldMap
  const max = Math.max(1, ...(isCN ? byProv.values() : byCode.values()))
  const pick = picked ? data.locations.find((l) => l.id === picked || l.name === picked) : null
  const pickName = pick ? (isCN ? pick.name : (nameByCode.get(pick.id) || pick.name)) : ''
  const pickN = pick ? (isCN ? (byProv.get(pick.name) || 0) : (byCode.get(pick.id) || 0)) : 0
  const countOf = (loc) => (isCN ? (byProv.get(loc.name) || 0) : (byCode.get(loc.id) || 0))
  return (
    <>
      <svg
        viewBox={data.viewBox}
        className={`world-map${isCN ? ' china-map' : ''}`}
        preserveAspectRatio="xMidYMid meet"
        role="img"
        aria-label={isCN ? '中国省级来源分布地图' : '来源地区分布地图'}
      >
        {data.locations.map((loc) => {
          const n = countOf(loc)
          // 开方让少量访问也能看出颜色，又不至于高访问量国家一片死白
          const fill = n ? `rgba(${MAP_RGB},${(0.16 + 0.78 * Math.sqrt(n / max)).toFixed(3)})` : MAP_BASE
          if (!isCN) {
            const label = nameByCode.get(loc.id) || loc.name
            const canDrill = loc.id === 'cn' && !!onModeChange
            return (
              <path
                key={loc.id}
                d={loc.d}
                fill={fill}
                stroke={canDrill ? 'rgba(136,212,80,0.55)' : (picked === loc.id ? 'rgba(255,255,255,0.55)' : 'rgba(255,255,255,0.07)')}
                strokeWidth="1"
                vectorEffect="non-scaling-stroke"
                onClick={() => (canDrill ? onModeChange('cn') : setPicked(loc.id))}
              >
                <title>{canDrill ? '点击查看中国省级分布' : (n ? `${label} · ${n} 次` : `${label} · 暂无记录`)}</title>
              </path>
            )
          }
          return (
            <path
              key={loc.name}
              d={loc.d}
              fill={fill}
              stroke={picked === loc.name ? 'rgba(255,255,255,0.55)' : 'rgba(255,255,255,0.07)'}
              strokeWidth="1"
              vectorEffect="non-scaling-stroke"
              onClick={() => setPicked(loc.name)}
            >
              <title>{n ? `${loc.name} · ${n} 次` : `${loc.name} · 暂无记录`}</title>
            </path>
          )
        })}
      </svg>
      {isCN && onModeChange && (
        <button type="button" className="map-back" onClick={() => onModeChange('world')}>← 返回世界</button>
      )}
      {isCN ? (
        <div className="map-pick">
          {pick ? (
            <>
              <span>{pickName} · {pickN ? `${pickN} 次` : '暂无记录'}</span>
              <button type="button" className="map-pick-x" aria-label="关闭" onClick={() => setPicked('')}>×</button>
            </>
          ) : (
            <span>中国 · {cnTotal ? `${cnTotal} 次` : '暂无记录'}</span>
          )}
        </div>
      ) : pick ? (
        <div className="map-pick">
          <span>{pickName} · {pickN ? `${pickN} 次` : '暂无记录'}</span>
          <button type="button" className="map-pick-x" aria-label="关闭" onClick={() => setPicked('')}>×</button>
        </div>
      ) : null}
    </>
  )
})

/* ---------- 日志级别 ---------- */
const LEVELS = {
  info: { label: '信息', cls: 'lv-info' },
  warn: { label: '警告', cls: 'lv-warn' },
  block: { label: '拦截', cls: 'lv-block' },
}

const fmtTime = (ts) => {
  const d = new Date(ts * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getMonth() + 1}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

// 导出 CSV 用：带年份与秒，Excel 打开不会歧义
const fmtTimeFull = (ts) => {
  const d = new Date(ts * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

const fmtPct = (v) => (v == null || v < 0 ? '—' : `${v}%`)

const fmtRate = (kbps) => {
  if (kbps == null || kbps < 0) return '—'
  if (kbps >= 1024) return `${(kbps / 1024).toFixed(1)} MB/s`
  return `${kbps} KB/s`
}

/* CPU 规格：区分物理核心与逻辑线程（超线程时 2 核 4 线程） */
const cpuLabel = (cores, physical) => {
  const l = cores || 0
  const p = physical || 0
  if (p > 0 && l > 0 && p < l) return `${p} 核 ${l} 线程`
  if (p > 0) return `${p} 核`
  if (l > 0) return `${l} 线程`
  return '—'
}

/* ---------- 页面：总览 ---------- */
function Overview({ data, history, range, onRange, onFeature, onEnableAll }) {
  if (!data) return null
  const { config, active } = data
  // 同步率：（健康指数 + 防护能力）× 1.3，量程 260%，200% 为「绝对领域」门槛
  const SYNC_MAX = 260
  const syncRate = data.syncRate ?? Math.min(SYNC_MAX, Math.round(((data.healthIndex || 0) + (data.defensePower || 0)) * 1.3))
  const syncPct = Math.max(0, Math.min(100, (syncRate / SYNC_MAX) * 100))
  const syncTier =
    syncRate >= 230 ? 'burst' : syncRate >= 200 ? 'field' : syncRate >= 160 ? 'steady' : syncRate >= 100 ? 'weak' : 'low'
  const syncTag =
    syncRate >= 230 ? '暴走临界' : syncRate >= 200 ? '绝对领域展开中' : syncRate >= 160 ? '同步稳定' : syncRate >= 100 ? '共鸣偏弱' : '同步率过低'
  return (
    <div className="page">
      <section className="hero">
        <div className="hero-left">
          <div className="hero-head">
            <img className="app-badge" src={appIconHd} alt="飞牛AT力场" />
            <div className="hero-copy">
              <h1>飞牛AT力场</h1>
              <p className="hero-desc">
                基于 AT 力场的硬件防护方案，为您的 NAS 提供全方位的安全防护，守护数字资产安全。
              </p>
            </div>
          </div>
          <div className="hero-actions">
            {active ? (
              <img className="hero-badge" src={badgeOn} alt="防护已开启" />
            ) : (
              <span className="status-badge badge-off">防护未开启</span>
            )}
          </div>
          <div className={`sync-bar sync-${syncTier}`}>
            <div className="sync-head">
              <span className="sync-title">同步率</span>
              <span className="sync-tag">{syncTag}</span>
              <span className="sync-num">
                {syncRate}
                <small>%</small>
              </span>
            </div>
            <div className="sync-track">
              <div className="sync-fill" style={{ width: `${syncPct}%` }} />
              <span
                className="sync-mark"
                style={{ left: `${(200 / SYNC_MAX) * 100}%` }}
                title="200% 绝对领域门槛"
              />
            </div>
          </div>
        </div>
        <div className="hero-right">
          <img className="dome-img" src={domeImg} alt="AT 力场防护" />
        </div>
      </section>

      <section className="stat-grid">
        <StatCard icon={siSync} title="健康指数" hint watermark={wmSync}
          foot={<span className="foot-up">↑ {data.healthText}</span>}>
          <div className="stat-value">{data.healthIndex}<span className="stat-unit">%</span></div>
          <ProgressBar value={data.healthIndex} max={100} tone="green" />
        </StatCard>

        <StatCard icon={siStatus} title="防护状态" watermark={wmStatus}
          foot={data.statusText}>
          <div className="stat-value-lg"><StatusDot active={active} /> {active ? '正常运行' : '未运行'}</div>
        </StatCard>

        <StatCard icon={siPower} title="防护能力" watermark={wmPower}
          foot="安全基线得分（100 分制）">
          <div className="stat-value">{data.defensePower}<span className="stat-unit">分</span></div>
          <ProgressBar value={data.defensePower} max={100} tone="blue" />
        </StatCard>

        <StatCard icon={siTime} title="运行时长" watermark={wmTime}
          foot="持续守护中">
          <div className="stat-value-lg">{data.uptimeText}</div>
        </StatCard>
      </section>

      <section className="two-col">
        <div className="panel">
          <div className="panel-title"><img className="panel-ic" src={fci1} alt="" /> 防护功能</div>
          <div className="feature-grid">
            <FeatureCard img={fci1} title="AT 力场防护" desc="构建安全屏障" enabled={config.atDefense} onClick={() => onFeature('atDefense', !config.atDefense)} />
            <FeatureCard img={fci2} title="实时入侵检测" desc="智能拦截异常行为" enabled={config.invadeScan} onClick={() => onFeature('invadeScan', !config.invadeScan)} />
            <FeatureCard img={fci3} title="数据安全加固" desc="多重防护机制" enabled={config.dataSeal} onClick={() => onFeature('dataSeal', !config.dataSeal)} />
            <FeatureCard img={fci4} title="一键启用" desc="轻松开启全方位防护" enabled={active} onClick={onEnableAll} />
          </div>
        </div>
        <div className="panel">
          <div className="panel-head">
            <div className="panel-title"><img className="panel-ic" src={siPower} alt="" /> 健康指数趋势</div>
            <RangePicker value={range} onChange={onRange} />
          </div>
          <LineChart points={history} target={data.healthIndex} range={range} />
        </div>
      </section>
    </div>
  )
}

/* ---------- 页面：防护日志（支持搜索与分页） ---------- */
function Logs() {
  const [level, setLevel] = useState('')
  const [keyword, setKeyword] = useState('')
  const [q, setQ] = useState('')
  const [ipInput, setIpInput] = useState('')
  const [ip, setIp] = useState('')
  const [open, setOpen] = useState('')
  const [page, setPage] = useState(0)
  const [rows, setRows] = useState([])
  const [total, setTotal] = useState(0)
  const [exporting, setExporting] = useState(false)
  const [note, setNote] = useState('')
  const [bodyH, setBodyH] = useState(0) // 列表可视区高度
  const [rowH, setRowH] = useState(0) // 单行实际高度
  const bodyRef = useRef(null)

  // 每页条数按窗口高度自动算：列表铺满可视区；窗口再矮也至少给 10 条，
  // 放不下时由 log-body 内部滚动，避免一页只剩三四条、翻页翻到手软
  const pageSize = Math.max(10, bodyH > 0 && rowH > 0 ? Math.floor(bodyH / rowH) : 12)

  // 监听列表区域高度变化（窗口缩放、侧栏变化都会触发）
  useEffect(() => {
    const el = bodyRef.current
    if (!el) return undefined
    const sync = () => setBodyH(el.clientHeight)
    sync()
    if (!window.ResizeObserver) {
      window.addEventListener('resize', sync)
      return () => window.removeEventListener('resize', sync)
    }
    const ro = new ResizeObserver(sync)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // 渲染后量一次「单行固有高度」，供分页计算。
  // 只用 offsetHeight（内容盒 + 分隔线），不取相邻行的 offsetTop 差：
  // 满页时行间隙会被均摊拉开，差值会随之变大，导致每页条数越算越少（死循环）。
  useEffect(() => {
    const el = bodyRef.current
    if (!el) return
    const list = el.querySelectorAll('.log-row')
    if (!list.length) return
    // 末行没有下边框，样本里排除掉；取前 5 行的最小值，避免个别折行行把基准抬高
    const pool = Array.prototype.slice.call(list, 0, Math.max(1, list.length - 1)).slice(0, 5)
    const h = Math.min.apply(null, pool.map((n) => n.offsetHeight))
    if (h > 0) setRowH(list.length === 1 ? h + 1 : h)
  }, [rows])

  // 搜索防抖，避免逐字请求
  useEffect(() => {
    const t = setTimeout(() => { setQ(keyword); setPage(0) }, 400)
    return () => clearTimeout(t)
  }, [keyword])

  useEffect(() => {
    const t = setTimeout(() => { setIp(ipInput.trim()); setPage(0) }, 400)
    return () => clearTimeout(t)
  }, [ipInput])

  useEffect(() => {
    let alive = true
    api.logs({ n: pageSize, offset: page * pageSize, level, q, ip })
      .then((r) => { if (alive) { setRows(r.logs || []); setTotal(r.total || 0) } })
      .catch(() => {})
    return () => { alive = false }
  }, [level, q, ip, page, pageSize])

  const pages = Math.max(1, Math.ceil(total / pageSize))

  // 每页条数变化（窗口变大/变小）后，页码可能越界，夹紧到最后一页
  useEffect(() => {
    if (page > pages - 1) setPage(pages - 1)
  }, [pages, page])

  // 导出当前筛选条件下的全部日志为 CSV（按 200 条一页拉取，最多 5000 条）
  const exportCsv = async () => {
    if (exporting) return
    setExporting(true)
    setNote('')
    try {
      let all = []
      let offset = 0
      for (;;) {
        const r = await api.logs({ n: 200, offset, level, q, ip })
        const batch = r.logs || []
        all = all.concat(batch)
        offset += batch.length
        if (batch.length === 0 || offset >= (r.total || 0) || offset >= 5000) break
      }
      if (all.length === 0) {
        setNote('当前筛选条件下没有可导出的日志')
        return
      }
      const esc = (v) => `"${String(v == null ? '' : v).replace(/"/g, '""')}"`
      const lines = [['时间', '级别', '模块', '内容', '对象', '来源', '操作者', '详情']]
      all.forEach((l) => lines.push([
        fmtTimeFull(l.time), LEVELS[l.level]?.label || l.level, l.module, l.msg,
        l.object || '', l.source || '', l.actor || '', l.detail || '',
      ]))
      // BOM 让 Excel 正确识别 UTF-8 中文
      const csv = '\ufeff' + lines.map((r) => r.map(esc).join(',')).join('\r\n')
      const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }))
      const a = document.createElement('a')
      a.href = url
      const d = new Date()
      const p = (n) => String(n).padStart(2, '0')
      a.download = `飞牛AT力场_日志_${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}.csv`
      a.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      setNote(`已导出 ${all.length} 条日志`)
    } catch (e) {
      setNote('导出失败：' + e.message)
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className="page logs-page">
      <div className="page-head logs-head">
        <div className="logs-head-top">
          <h2>防护日志</h2>
          <div className="filter-bar">
            {[['', '全部'], ['info', '信息'], ['warn', '警告'], ['block', '拦截']].map(([v, label]) => (
              <button key={v} className={`chip ${level === v ? 'chip-on' : ''}`} onClick={() => { setLevel(v); setPage(0) }}>{label}</button>
            ))}
            <input
              className="search-input"
              placeholder="搜索模块或内容…"
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
            />
            <input
              className="search-input"
              placeholder="按 IP 过滤…"
              value={ipInput}
              onChange={(e) => setIpInput(e.target.value)}
            />
            <button className="btn btn-mini" onClick={exportCsv} disabled={exporting}>
              {exporting ? '导出中…' : '导出 CSV'}
            </button>
          </div>
        </div>
        <div className="page-sub">记录防护启用/关闭、配置变更、阈值告警与拦截事件；可搜索、按级别或 IP 筛选，点击任意一行查看结构化详情</div>
      </div>
      <div className="panel log-panel">
        {/* 只有「整页条数都放得下」时才用 space-between 把余量均摊到行间隙；
            窗口太矮、10 条放不下时改为正常排布 + 内部滚动 */}
        <div
          className={`log-body${rows.length > 0 && rows.length >= pageSize && rowH * rows.length <= bodyH + 1 ? ' fill' : ''}`}
          ref={bodyRef}
        >
          {rows.length === 0 && (
            <div className="empty">{ip || q || level ? '当前筛选条件下没有日志' : '暂无日志'}</div>
          )}
          {rows.map((l, i) => {
            const key = `${l.time}-${i}`
            const isIp = /^\d{1,3}(\.\d{1,3}){3}$/.test(l.object || '')
            return (
              <div className={`log-entry ${open === key ? 'open' : ''}`} key={key}>
                <div className="log-row log-click" onClick={() => setOpen((v) => (v === key ? '' : key))}>
                  <span className="log-time">{fmtTime(l.time)}</span>
                  <span className={`log-level ${LEVELS[l.level]?.cls}`}>{LEVELS[l.level]?.label}</span>
                  <span className="log-module">
                    {l.module}
                    {l.source && <em className="log-source">{l.source}</em>}
                  </span>
                  <span className="log-msg">
                    {l.object && <em className={`log-obj mono ${isIp ? 'tag-ip' : ''}`}>{l.object}</em>}
                    {l.msg}
                  </span>
                </div>
                {open === key && (
                  <div className="log-detail">
                    <div><span>对象</span><b className="mono">{l.object || '—'}</b></div>
                    <div><span>来源</span><b>{l.source || '—'}</b></div>
                    <div><span>操作者</span><b>{l.actor || '—'}</b></div>
                    <div className="wide"><span>详情</span><b>{l.detail || '—'}</b></div>
                    {isIp && (
                      <div className="wide">
                        <button className="btn btn-mini" onClick={() => setIpInput(l.object)}>只看该 IP 的日志</button>
                      </div>
                    )}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </div>
      <div className="pager logs-pager">
        <span className="pager-info">共 {total} 条 · 第 {page + 1} / {pages} 页</span>
        {note && <span className="test-msg">{note}</span>}
        <button className="btn" disabled={page === 0} onClick={() => setPage((p) => Math.max(0, p - 1))}>上一页</button>
        <button className="btn" disabled={page + 1 >= pages} onClick={() => setPage((p) => p + 1)}>下一页</button>
      </div>
    </div>
  )
}

/* ---------- 页面：访问分析 ---------- */
const statusTone = (code) => {
  if (/^5/.test(code)) return 'st-bad'
  if (/^4/.test(code)) return 'st-warn'
  if (/^3/.test(code)) return 'st-info'
  return 'st-ok'
}

function WebAccess({ data, err, range, bannedSet, banStatus, banBtn, scanIps = [], onBanScans, scanActing, scanMsg }) {
  const [open, setOpen] = useState('')
  const [zoom, setZoom] = useState('')
  // 被访问路径：点击某条路径展开其来源 IP（路径-来源关联只保留最近 7 天）
  const [openPath, setOpenPath] = useState('')
  const [pathSrc, setPathSrc] = useState({ path: '', loading: false, data: null, err: '' })
  useEffect(() => { setOpenPath('') }, [range])
  const togglePath = (path) => {
    if (openPath === path) {
      setOpenPath('')
      return
    }
    setOpenPath(path)
    setPathSrc({ path, loading: true, data: null, err: '' })
    api.webPathSources(path, range)
      .then((d) => setPathSrc((s) => (s.path === path ? { path, loading: false, data: d, err: '' } : s)))
      .catch((e) => setPathSrc((s) => (s.path === path ? { path, loading: false, data: null, err: e.message } : s)))
  }
  // 来源地区地图：world 世界视图 / cn 中国省级视图，面板与放大层共用，保证下钻状态同步
  const [mapMode, setMapMode] = useState('world')
  const p2 = (n) => String(n).padStart(2, '0')
  const isDay = range === '7d' || range === '30d'
  const fmtWin = (ts) => {
    if (!ts) return '—'
    const d = new Date(ts * 1000)
    return isDay
      ? `${d.getMonth() + 1}/${d.getDate()} 00:00`
      : `${p2(d.getHours())}:${p2(d.getMinutes())}`
  }

  if (err && !data) return <section className="panel"><div className="empty">访问分析加载失败：{err}</div></section>
  if (!data) return <section className="panel"><div className="empty">正在读取访问日志…</div></section>

  const ips = data.ips || []
  const paths = data.paths || []
  const statuses = data.statuses || []
  const countries = data.countries || []
  // 中国省级分布：复用 Top IP 的归属地（region），内网 / 无法识别 / 仅注册机构级的地址不计入
  const provMap = new Map()
  ips.forEach((it) => {
    if (it.internal || it.approx) return
    const code = (it.code || '').toLowerCase()
    let name = ''
    if (code === 'hk') name = '香港'
    else if (code === 'mo') name = '澳门'
    else if (code === 'tw') name = '台湾'
    else if (code === 'cn' || /中国/.test(it.country || '')) name = provinceName(it.region)
    if (!name) return
    provMap.set(name, (provMap.get(name) || 0) + (it.count || 0))
  })
  const provinces = [...provMap].map(([name, count]) => ({ name, count }))
  // 中国总访问量（含港澳台等归属中国的条目），用于中国视图左下角默认显示
  const cnTotal = countries
    .filter((c) => (c.code || '').toLowerCase() === 'cn' || /中国/.test(c.name || ''))
    .reduce((a, c) => a + (c.count || 0), 0)
  const scans = data.scans || []
  const total = data.total || 0
  const bad = data.bad != null ? data.bad : statuses.filter((s) => /^[45]/.test(s.code)).reduce((a, s) => a + s.count, 0)
  const badPct = total ? Math.round((bad * 100) / total) : 0
  const abroadPct = data.domesticPct == null ? null : 100 - data.domesticPct
  const maxPath = Math.max(1, ...paths.map((p) => p.count))
  const countrySum = countries.reduce((a, c) => a + c.count, 0) || 1
  const maxCountry = Math.max(1, ...countries.map((c) => c.count))
  const granularity = range === '1h' ? '每 10 分钟' : isDay ? '按天' : '按小时'
  // 环比：只有上一个窗口有数据时才显示，避免 0 → 有数据时出现「+∞%」
  const delta = (cur, prev) => {
    if (!prev) return ''
    const d = Math.round(((cur - prev) * 100) / prev)
    if (d === 0) return ' · 较上期持平'
    return ` · 较上期 ${d > 0 ? '+' : ''}${d}%`
  }

  return (
    <>
      {!data.available ? (
        <section className="panel">
          <div className="panel-title"><Icon name="info" size={16} /> 暂无可分析数据</div>
          <p className="about-text">
            未找到可读的 nginx 访问日志。程序会自动探测 /usr/trim/nginx/logs/access.log、
            /var/log/nginx/access.log 等常见路径，请确认 Web 服务已产生访问日志，且运行账号有读取权限。
            {data.error ? ` 最近错误：${data.error}` : ''}
          </p>
        </section>
      ) : (
        <>
          <section className="stat-grid web-stats">
            <StatCard icon={siStatus} title="总请求数" watermark={wmSync} foot={`${fmtWin(data.from)} – ${fmtWin(data.to)}${delta(total, data.prevTotal)}`}>
              <div className="stat-value-lg">{total.toLocaleString()}</div>
            </StatCard>
            <StatCard icon={siTime} title="异常请求" watermark={wmTime} foot={`${bad.toLocaleString()} 次 4xx / 5xx${delta(bad, data.prevBad)}`}>
              <div className="stat-value-lg">{badPct}%</div>
            </StatCard>
            <StatCard icon={siPower} title="境外访问占比" watermark={wmPower} foot={data.domesticPct == null ? '暂无公网访问，无法判定（内网/本地未计入）' : `境内公网 ${data.domesticPct}%（内网/本地未计入）`}>
              <div className="stat-value-lg">{abroadPct == null ? '—' : `${abroadPct}%`}</div>
            </StatCard>
            <StatCard icon={siStatus} title="最近访问" watermark={wmStatus} foot={`聚合数据保留 ${data.retentionDays} 天`}>
              <div className="stat-value-lg">{data.lastEvent ? fmtTime(data.lastEvent) : '—'}</div>
            </StatCard>
          </section>

          <section className="two-col even">
            <div className="panel">
              <div className="panel-head">
                <PanelTitle icon="chart" onZoom={() => setZoom('trend')}>访问量趋势</PanelTitle>
                <div className="panel-sub">{granularity} · {fmtWin(data.from)} – {fmtWin(data.to)}</div>
              </div>
              <div className="chart-legend">
                <span><i style={{ background: '#8b6bff' }} />总请求</span>
                <span><i style={{ background: '#e0566f' }} />异常 4xx / 5xx</span>
              </div>
              <WebChart points={data.points || []} range={range} />
            </div>

            <div className="panel map-panel">
              <div className="panel-head">
                <PanelTitle icon="net" onZoom={() => setZoom('map')}>来源地区地图</PanelTitle>
                <div className="map-legend">
                  <span><i style={{ background: MAP_BASE }} />无来源</span>
                  <span><i style={{ background: `rgba(${MAP_RGB},0.45)` }} />较少</span>
                  <span><i style={{ background: `rgba(${MAP_RGB},0.94)` }} />较多</span>
                </div>
              </div>
              <div className="world-map-wrap">
                <WorldMap items={countries} provinces={provinces} mode={mapMode} onModeChange={setMapMode} cnTotal={cnTotal} />
              </div>
            </div>
          </section>

          <section className="two-col">
            <div className="panel">
              <div className="panel-head">
                <div className="panel-title"><Icon name="list" size={16} /> 来源 IP Top</div>
                <div className="panel-sub">按请求量排序 · 点击查看详情与封禁</div>
              </div>
              {scans.length > 0 && (
                <div className="scan-bar">
                  <span>检测到 {scans.length} 个疑似扫描 IP（异常请求占比较高）</span>
                  {onBanScans && scanIps.length > 0 ? (
                    <button className="btn btn-mini btn-danger" disabled={scanActing} onClick={onBanScans}>
                      {scanActing ? '封禁中…' : `一键封禁疑似扫描（${scanIps.length}）`}
                    </button>
                  ) : (
                    <span className="scan-done">均已在封禁名单中</span>
                  )}
                </div>
              )}
              {scanMsg && <div className="test-msg scan-msg">{scanMsg}</div>}
              {ips.length === 0 ? (
                <div className="empty">该时段暂无访问记录</div>
              ) : (
                <div className="web-table">
                  <div className="web-ip-grid web-ip-head">
                    <span>来源 IP</span><span>请求</span><span>占比</span><span>归属地</span>
                  </div>
                  {ips.map((it) => {
                    const share = total ? Math.round((it.count * 100) / total) : 0
                    const banned = bannedSet.has(it.ip)
                    const place = it.internal ? '内网/保留地址' : it.country || '未知'
                    const full = geoLabelText(it)
                    return (
                      <div className={`ip-entry ${open === it.ip ? 'open' : ''}`} key={it.ip}>
                        <div className="web-ip-grid ip-click" onClick={() => setOpen((v) => (v === it.ip ? '' : it.ip))}>
                          <span className="mono">
                            {it.ip}
                            {banned && <em className="tag tag-banned">已封禁</em>}
                            {!banned && it.scan && <em className="tag tag-scan">疑似扫描</em>}
                            {!banned && !it.scan && share >= 15 && it.count >= 20 && <em className="tag tag-risk">高频</em>}
                            <em className="net-caret">›</em>
                          </span>
                          <span className="ip-count">{it.count}</span>
                          <span className="web-share">{share}%</span>
                          <span className="ellipsis" title={full}>{place}</span>
                        </div>
                        {open === it.ip && (
                          <div className="net-detail web-ip-detail">
                            <div><span>来源 IP</span><b className="mono">{it.ip}</b></div>
                            <div><span>请求次数</span><b>{it.count} 次</b></div>
                            <div><span>占比</span><b>{share}%</b></div>
                            {it.bad > 0 && <div><span>异常请求</span><b>{it.bad} 次 4xx / 5xx{it.scan ? '（疑似扫描）' : ''}</b></div>}
                            <div>
                              <span>归属地</span>
                              <b>
                                {geoLabelText(it, false)}
                                {it.approx && !it.internal && <em className="geo-warn">{GEO_APPROX_NOTE}</em>}
                              </b>
                            </div>
                            <div><span>封禁状态</span><b>{banStatus(it.ip)}</b></div>
                            {banBtn(it.ip)}
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>
              )}
            </div>

            <div className="panel">
              <div className="panel-head">
                <div className="panel-title"><Icon name="net" size={16} /> 被访问路径 Top</div>
                <div className="panel-sub">已去除查询参数 · 点击路径查看来源 IP</div>
              </div>
              {paths.length === 0 ? (
                <div className="empty">该时段暂无访问记录</div>
              ) : (
                <div className="web-path-list">
                  {paths.map((p) => (
                    <div className={`web-path ${openPath === p.path ? 'open' : ''}`} key={p.path}>
                      <div className="web-path-top ip-click" onClick={() => togglePath(p.path)}>
                        <span className="path-name">
                          <span className="mono ellipsis" title={p.path}>{p.path}</span>
                          <em className="net-caret">›</em>
                        </span>
                        <span className="web-path-count">{p.count} 次</span>
                      </div>
                      <div className="web-bar"><i style={{ width: `${Math.max(2, (p.count * 100) / maxPath)}%` }} /></div>
                      {openPath === p.path && (
                        <div className="path-src">
                          {pathSrc.loading && <div className="src-hint">正在统计来源 IP…</div>}
                          {pathSrc.err && <div className="src-hint">来源 IP 加载失败：{pathSrc.err}</div>}
                          {pathSrc.data && (
                            (pathSrc.data.ips || []).length === 0 ? (
                              <div className="src-hint">该时段没有记录到来源 IP（路径-来源关联只保留最近 {pathSrc.data.retentionDays || 7} 天）</div>
                            ) : (
                              <>
                                <div className="src-grid src-head"><span>来源 IP</span><span>请求</span><span>归属地</span></div>
                                {(pathSrc.data.ips || []).map((it) => {
                                  const place = it.internal ? '内网/保留地址' : geoLabelText(it)
                                  return (
                                    <div className="src-grid src-row" key={it.ip}>
                                      <span className="mono ellipsis" title={it.ip}>{it.ip}</span>
                                      <span className="src-num">{it.count} 次</span>
                                      <span className="ellipsis" title={place}>{place}</span>
                                    </div>
                                  )
                                })}
                                <div className="src-foot">
                                  已记录 {pathSrc.data.covered} 次 · 仅列出 Top 5 来源
                                  {pathSrc.data.partial
                                    ? '（窗口超过 7 天，只统计最近 7 天）'
                                    : `（保留最近 ${pathSrc.data.retentionDays || 7} 天）`}
                                </div>
                              </>
                            )
                          )}
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              )}
              {statuses.length > 0 && (
                <>
                  <div className="web-divider"><span>状态码分布</span></div>
                  <div className="status-chips">
                    {statuses.map((s) => (
                      <span className={`status-chip ${statusTone(s.code)}`} key={s.code}>
                        {s.code}<i>{s.count}</i>
                      </span>
                    ))}
                  </div>
                </>
              )}
            </div>
          </section>

          <section className="two-col">
            <div className="panel">
              <div className="panel-head">
                <div className="panel-title"><Icon name="chart" size={16} /> 地区分布</div>
                <div className="panel-sub">按访问量 Top IP 估算</div>
              </div>
              {countries.length === 0 ? (
                <div className="empty">暂无归属地数据</div>
              ) : (
                <div className="web-country-list">
                  {countries.map((c) => (
                    <div className="web-country" key={c.name}>
                      <span className="ellipsis" title={c.name}>{c.name}</span>
                      <div className="web-bar"><i style={{ width: `${Math.max(2, (c.count * 100) / maxCountry)}%` }} /></div>
                      <span className="web-country-val">{Math.round((c.count * 100) / countrySum)}%</span>
                    </div>
                  ))}
                </div>
              )}
            </div>

            <div className="panel">
              <div className="panel-title"><Icon name="info" size={16} /> 采集说明</div>
              <p className="about-text">
                数据来自 nginx 访问日志（{data.logPath || '自动探测'}），按 inode + 偏移量增量读取，日志轮转后自动跟随；
                聚合结果按小时落盘，保留 {data.retentionDays} 天，本次采集开始于 {fmtTime(data.since)}。
                <br />
                统计窗口按整点对齐：1 小时为本小时，1 天为近 24 小时，7 天 / 30 天为近 7 / 30 天（含今天）；
                异常请求指 4xx / 5xx 状态码，疑似扫描指异常次数 ≥ 20 且占比过半的来源 IP。
                地区占比按访问量最高的 10 个 IP 估算，公网地址会并发查询 ip-api、ipwho.is、ip2location、iplocation 多源比对并缓存 24 小时，内网地址仅本地判定。
                只有两个以上数据源的城市坐标一致时才显示省市，否则只显示国家 / 运营商并标注「地址可能不准」，这类地址不计入省份分布。
                {data.loopback > 0 && (
                  <>
                    <br />
                    检测到 {data.loopback.toLocaleString()} 次请求来源为 127.0.0.1：日志可能经过反向代理且未记录
                    X-Forwarded-For，此时来源 IP 会偏少、地区分布仅供参考。
                  </>
                )}
              </p>
            </div>
          </section>
        </>
      )}

      {zoom === 'trend' && (
        <ZoomModal
          title="访问量趋势"
          onClose={() => setZoom('')}
          headRight={<span className="zoom-sub">{granularity} · {fmtWin(data.from)} – {fmtWin(data.to)}</span>}
        >
          <div className="chart-legend">
            <span><i style={{ background: '#8b6bff' }} />总请求</span>
            <span><i style={{ background: '#e0566f' }} />异常 4xx / 5xx</span>
          </div>
          <WebChart points={data.points || []} range={range} height={320} />
        </ZoomModal>
      )}

      {zoom === 'map' && (
        <ZoomModal
          title="来源地区地图"
          icon="net"
          onClose={() => setZoom('')}
          headRight={
            <div className="map-legend">
              <span><i style={{ background: MAP_BASE }} />无来源</span>
              <span><i style={{ background: `rgba(${MAP_RGB},0.45)` }} />较少</span>
              <span><i style={{ background: `rgba(${MAP_RGB},0.94)` }} />较多</span>
            </div>
          }
        >
          <div className="world-map-wrap">
            <WorldMap items={countries} provinces={provinces} mode={mapMode} onModeChange={setMapMode} cnTotal={cnTotal} />
          </div>
        </ZoomModal>
      )}
    </>
  )
}

/* ---------- 页面：网络与服务 ---------- */
// 把 "IP:端口" / "[IPv6]:端口" 还原成纯 IP
const ipOf = (remote = '') => {
  if (remote.startsWith('[')) return remote.slice(1, remote.indexOf(']'))
  // 裸 IPv6（无端口、无方括号）：整串就是地址
  if ((remote.match(/:/g) || []).length > 1) return remote
  return remote.split(':')[0]
}

function Network({ data }) {
  // 时限与封禁时长本地持久化，刷新页面后保持上次选择；
  // 存储键换到 v2，让升级后的默认值（1 天 / 24 小时）对老用户也生效一次
  const [range, setRange] = useState(() => {
    const v = localStorage.getItem('atff.netRange.v2')
    return WEB_RANGES.some(([k]) => k === v) ? v : '1d'
  })
  const [banDur, setBanDur] = useState(() => {
    const v = localStorage.getItem('atff.banDuration.v2')
    return BAN_DURS.some(([k]) => k === v) ? v : '24h'
  })
  useEffect(() => { localStorage.setItem('atff.netRange.v2', range) }, [range])
  const pickBanDur = (v) => {
    setBanDur(v)
    localStorage.setItem('atff.banDuration.v2', v)
  }
  const [net, setNet] = useState(null)
  const [geo, setGeo] = useState({})
  const [open, setOpen] = useState('')
  const [acting, setActing] = useState('')
  const [actMsg, setActMsg] = useState('')
  const [tick, setTick] = useState(0)
  // 活动连接明细（点「活动连接」卡片打开）：逐条连接 + 可单独断开
  const [showConns, setShowConns] = useState(false)
  // 进程级流量明细（点「流量用量」标题旁的「明细」打开）
  const [procOpen, setProcOpen] = useState(false)
  const [procData, setProcData] = useState(null)
  const [procLoading, setProcLoading] = useState(false)
  const [procErr, setProcErr] = useState('')
  const [procTick, setProcTick] = useState(0)
  const [zoomChart, setZoomChart] = useState('')
  const [connList, setConnList] = useState([])
  const [connLoading, setConnLoading] = useState(false)
  const [connMsg, setConnMsg] = useState('')
  const [connFilter, setConnFilter] = useState('')
  const [connKilling, setConnKilling] = useState('')
  const [connTick, setConnTick] = useState(0)
  // 页内分段：「访问分析」为默认，「实时连接」保持原有内容
  const [view, setView] = useState(() => {
    // 0.1.61 起默认分段改为「访问分析」，旧的分段记忆只重置一次
    const saved = localStorage.getItem('atff.netView.v2')
    if (saved === 'live' || saved === 'web') return saved
    localStorage.removeItem('atff.netView')
    return 'web'
  })
  const [webData, setWebData] = useState(null)
  const [webErr, setWebErr] = useState('')
  useEffect(() => { localStorage.setItem('atff.netView.v2', view) }, [view])

  useEffect(() => {
    if (view !== 'web') return undefined
    let alive = true
    const load = () => api.webSummary(range)
      .then((r) => { if (alive) { setWebData(r); setWebErr('') } })
      .catch((e) => { if (alive) setWebErr(e.message) })
    load()
    const t = setInterval(load, 60000)
    return () => { alive = false; clearInterval(t) }
  }, [view, range])

  useEffect(() => {
    if (!showConns) return undefined
    let alive = true
    const load = () => {
      setConnLoading(true)
      api.connections()
        .then((r) => { if (alive) setConnList(r.connections || []) })
        .catch((e) => { if (alive) setConnMsg(e.message) })
        .finally(() => { if (alive) setConnLoading(false) })
    }
    load()
    const t = setInterval(load, 8000)
    return () => { alive = false; clearInterval(t) }
  }, [showConns, connTick])

  useEffect(() => {
    if (!procOpen) return undefined
    let alive = true
    const load = () => {
      setProcLoading(true)
      api.procTraffic(range)
        .then((r) => { if (alive) { setProcData(r); setProcErr('') } })
        .catch((e) => { if (alive) setProcErr(e.message) })
        .finally(() => { if (alive) setProcLoading(false) })
    }
    load()
    const t = setInterval(load, 5000)
    return () => { alive = false; clearInterval(t) }
  }, [procOpen, range, procTick])

  useEffect(() => {
    let alive = true
    const load = () => api.net(range).then((r) => { if (alive) setNet(r) }).catch(() => {})
    load()
    const t = setInterval(load, 30000)
    return () => { alive = false; clearInterval(t) }
  }, [range, tick])

  const points = net?.points || []
  const traffic = net?.traffic || []
  const trafficRx = traffic.reduce((sum, it) => sum + (it.rx || 0), 0)
  const trafficTx = traffic.reduce((sum, it) => sum + (it.tx || 0), 0)
  const procRows = procData?.procs || []
  const procTotal = procRows.reduce((sum, p) => sum + (p.rx || 0) + (p.tx || 0), 0)
  const hostTotal = (procData?.hostRx || 0) + (procData?.hostTx || 0)
  const procCoverage = hostTotal > 0 ? Math.min(100, Math.round((procTotal / hostTotal) * 100)) : 0
  const conns = net?.connections || []
  const details = net?.connDetail || []
  const svcs = net?.services || []
  const ports = net?.listenPorts || []
  const running = net?.runningServices || []
  const failed = net?.failedIPs || []
  const banned = net?.bannedIPs || []
  const bans = net?.bans || []
  const banMap = new Map(bans.map((b) => [b.ip, b]))
  const bannedSet = new Set([...banned, ...bans.map((b) => b.ip)])
  const banBackendName = net?.banBackend === 'fail2ban' ? 'fail2ban' : net?.banBackend === 'nft' ? 'nft 本应用' : '—'
  const publicPorts = ports.filter((p) => p.public).length
  const activeSvcs = svcs.filter((s) => s.active).length
  const rangeText = ({ '1h': '1 小时', '1d': '24 小时', '7d': '7 天', '30d': '30 天' })[range] || '24 小时'
  // 30 天仅用于访问分析；切回实时连接时回到 7 天，避免吞吐趋势图对不上
  useEffect(() => {
    if (view !== 'web' && range === '30d') setRange('7d')
  }, [view, range])

  const fmtRemain = (sec) => {
    if (!sec || sec <= 0) return ''
    if (sec >= 3600) return `${Math.round((sec / 3600) * 10) / 10} 小时`
    return `${Math.ceil(sec / 60)} 分钟`
  }
  // 封禁状态文案：区分 fail2ban 与本应用 nft 兜底封禁，并显示剩余时间
  const banStatus = (ip) => {
    const b = banMap.get(ip)
    if (!b) return bannedSet.has(ip) ? '已封禁（fail2ban）' : '未封禁'
    const name = b.backend === 'nft' ? 'nft 本应用' : 'fail2ban'
    const remain = fmtRemain(b.expires)
    return `已封禁（${name}${remain ? `，剩余 ${remain}` : ''}）`
  }
  // 内网地址封禁会导致局域网设备失联，先二次确认
  const isPrivateIp = (ip) => /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(ip || '')

  // 批量封禁疑似扫描 IP：过滤已封禁与内网 / 回环，逐条提交并汇总结果
  const scanIps = (webData?.scans || [])
    .map((s) => s.ip)
    .filter((ip) => !bannedSet.has(ip) && !isPrivateIp(ip) && !ip.startsWith('127.') && ip !== '::1')
  const banScans = async () => {
    if (acting || scanIps.length === 0) return
    if (!window.confirm(`将按当前封禁时长（${banDurLabel(banDur)}）依次封禁 ${scanIps.length} 个疑似扫描 IP，确定继续吗？`)) return
    setActing('__scan__')
    setActMsg('')
    let ok = 0
    const fails = []
    for (const ip of scanIps) {
      try {
        await api.banIp(ip, banDur)
        ok += 1
      } catch (e) {
        fails.push(`${ip}：${e.message}`)
      }
    }
    setActing('')
    setActMsg(`批量封禁完成：成功 ${ok} 个${fails.length ? `，失败 ${fails.length} 个（${fails[0]}）` : ''}`)
    setTick((x) => x + 1)
  }

  const detailOf = (remote) => details.find((d) => d.remote === remote) || {}
  const toggleRow = (k) => setOpen((v) => (v === k ? '' : k))

  const lookup = (ip) => {
    if (!ip || geo[ip]?.loading) return
    setGeo((g) => ({ ...g, [ip]: { loading: true } }))
    api.ipinfo(ip)
      .then((info) => setGeo((g) => ({ ...g, [ip]: { info } })))
      .catch((e) => setGeo((g) => ({ ...g, [ip]: { info: { error: e.message } } })))
  }

  const geoText = (ip) => {
    const g = geo[ip]
    if (!g) return ''
    if (g.loading) return '查询中…'
    const i = g.info || {}
    if (i.error) return i.error
    return geoLabelText(i) || '未查询到归属地'
  }

  // 归属地默认不查，点「查看归属」才把该 IP 发送至 ip-api.com（内网地址仅本地判定）
  const geoCell = (ip) => {
    const g = geo[ip]
    const text = geoText(ip)
    return (
      <span className="geo-cell" onClick={(e) => e.stopPropagation()}>
        {!g?.loading && (!g?.info || g.info.error) && (
          <button className="btn btn-mini" onClick={() => lookup(ip)}>查看归属</button>
        )}
        {g?.loading ? (
          <span className="geo-text">查询中…</span>
        ) : text ? (
          <span className="geo-text" title={text}>{text}</span>
        ) : null}
      </span>
    )
  }

  // 封禁 / 解封：失败时原样回显后端错误，成功后刷新列表
  const doBan = async (ip, ban) => {
    if (acting) return
    if (ban && isPrivateIp(ip) && !window.confirm(`确定要封禁内网地址 ${ip} 吗？\n若该地址是你当前使用的设备、路由器或网关，封禁后会立刻无法访问 NAS。`)) return
    setActing(ip)
    setActMsg('')
    try {
      const res = await (ban ? api.banIp(ip, banDur) : api.unbanIp(ip))
      const be = res?.backend === 'nft' ? 'nft 本应用' : 'fail2ban'
      const durText = ban ? ` · ${banDurLabel(banDur)}` : ''
      setActMsg(`${ban ? '已封禁' : '已解封'} ${ip}（${be}${durText}）`)
      setTick((x) => x + 1)
    } catch (e) {
      setActMsg(e.message)
    } finally {
      setActing('')
    }
  }

  const banBtn = (ip) => {
    const isBanned = bannedSet.has(ip)
    return (
      <div className="net-actions">
        {!isBanned && (
          <div className="ban-dur" role="group" aria-label="封禁时长">
            {BAN_DURS.map(([v, label]) => (
              <button
                key={v}
                type="button"
                className={`ban-dur-btn ${banDur === v ? 'on' : ''}`}
                onClick={(e) => { e.stopPropagation(); pickBanDur(v) }}
              >
                {label}
              </button>
            ))}
          </div>
        )}
        <button
          className={`btn btn-mini ${isBanned ? '' : 'btn-danger'}`}
          disabled={acting === ip}
          onClick={(e) => { e.stopPropagation(); doBan(ip, !isBanned) }}
        >
          {acting === ip ? '处理中…' : isBanned ? '解封此 IP' : '立即封禁'}
        </button>
        {actMsg && <span className="net-msg">{actMsg}</span>}
      </div>
    )
  }

  // 手动断开一条连接：走内核 SOCK_DESTROY（ss -K），失败原因原样回显。
  // 受保护的连接（本应用自身端口）连确认框都不弹，直接提示原因；
  // 敏感端口（SSH / SMB / NFS）仍可断开，但确认框里会多一段警告，防止手滑断掉自己正在用的会话。
  const killConn = async (c) => {
    const key = `${c.local}|${c.remote}|${c.pid}`
    if (connKilling) return
    if (c.locked) {
      setConnMsg(c.lockReason || '该连接受保护，不能断开')
      return
    }
    const proc = c.process ? `\n进程 ${c.process}${c.pid ? ` (PID ${c.pid})` : ''}` : ''
    const warn = c.sensitive
      ? `\n\n⚠ 本机端口 ${String(c.local).split(':').pop()} 通常用于${c.sensitive}，断开可能中断你正在进行的远程操作（例如当前这条 SSH 会话本身）。`
      : ''
    if (!window.confirm(`确定断开这条连接吗？\n本机 ${c.local}\n远端 ${c.remote}${proc}\n\n只中断这一条 TCP 连接，不封禁 IP、也不结束进程。${warn}`)) return
    setConnKilling(key)
    setConnMsg('')
    try {
      await api.killConnection(c.local, c.remote)
      setConnMsg(`已断开 ${c.remote}`)
      setConnList((list) => list.filter((x) => !(x.local === c.local && x.remote === c.remote)))
    } catch (e) {
      setConnMsg(e.message)
    } finally {
      setConnKilling('')
    }
  }
  const connKw = connFilter.trim().toLowerCase()
  const connRows = connKw
    ? connList.filter((c) => `${c.local} ${c.remote} ${c.process || ''} ${c.pid || ''}`.toLowerCase().includes(connKw))
    : connList
  const fmtBytes = (n) => (n >= 1048576 ? `${(n / 1048576).toFixed(1)} MB` : n >= 1024 ? `${Math.round(n / 1024)} KB` : `${n} B`)

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h2>网络与服务</h2>
          <div className="page-sub">
            {view === 'live'
              ? '实时吞吐每 2 秒刷新；趋势图每 30 秒采样一次，连接来源与 SSH 失败登录为实时统计'
              : '访问日志增量采集、按小时聚合；1 小时 = 本小时，1 天 = 近 24 小时，7 / 30 天 = 近 7 / 30 天'}
          </div>
        </div>
        <div className="net-head-tools">
          <SegTabs value={view} onChange={setView} items={[['web', '访问分析'], ['live', '实时连接']]} />
          <RangePicker value={range} onChange={setRange} items={view === 'web' ? WEB_RANGES : RANGES} />
        </div>
      </div>
      {view === 'live' ? (
        <>
      <section className="stat-grid three">
        <StatCard icon={siStatus} title="实时下行" watermark={wmSync} foot="接收速率">
          <div className="stat-value-lg">{fmtRate(data?.netRxKBps)}</div>
        </StatCard>
        <StatCard icon={siStatus} title="实时上行" watermark={wmPower} foot="发送速率">
          <div className="stat-value-lg">{fmtRate(data?.netTxKBps)}</div>
        </StatCard>
        <StatCard icon={siStatus} title="活动连接" watermark={wmStatus} foot="ESTABLISHED · 点击查看明细" onClick={() => { setConnMsg(''); setShowConns(true) }}>
          <div className="stat-value-lg">{data?.establishedConns ?? 0} 个</div>
        </StatCard>
      </section>
      <section className="two-col even">
        <div className="panel">
          <PanelTitle icon="net" onZoom={() => setZoomChart('net')}>网络吞吐趋势</PanelTitle>
          <div className="legend">
            <span className="legend-item"><i style={{ background: '#43e7ad' }} />下行</span>
            <span className="legend-item"><i style={{ background: '#8b6bff' }} />上行</span>
          </div>
          <NetChart points={points} range={range} />
        </div>
        <div className="panel">
          <div className="panel-head">
            <div className="head-left">
              <PanelTitle icon="chart" onZoom={() => setZoomChart('traffic')}>流量用量</PanelTitle>
              <button type="button" className="btn btn-mini" onClick={() => setProcOpen(true)}>进程明细</button>
            </div>
            <div className="panel-sub">共 {fmtTraffic(trafficRx + trafficTx)}（↓ {fmtTraffic(trafficRx)} / ↑ {fmtTraffic(trafficTx)}）</div>
          </div>
          <div className="legend">
            <span className="legend-item"><i style={{ background: '#43e7ad' }} />下行</span>
            <span className="legend-item"><i style={{ background: '#8b6bff' }} />上行</span>
          </div>
          <TrafficChart points={traffic} range={range} />
        </div>
      </section>
      <section className="panel">
        <div className="panel-head">
          <div className="panel-title"><Icon name="info" size={16} /> 可疑 IP 分析</div>
          <div className="panel-sub">巡检时间：{net?.checkedAt ? fmtTime(net.checkedAt) : '—'}</div>
        </div>
        {failed.length === 0 ? (
          <div className="empty">近 {rangeText}未发现 SSH 失败登录记录</div>
        ) : (
          <div className="ip-table">
            <div className="ip-grid ip-head">
              <span>来源 IP</span><span>失败次数</span><span>最近尝试</span><span>账号</span><span>归属地</span>
            </div>
            {failed.map((f) => {
              const key = `i:${f.ip}`
              return (
                <div className={`ip-entry ${open === key ? 'open' : ''}`} key={f.ip}>
                  <div className="ip-grid ip-click" onClick={() => toggleRow(key)}>
                    <span className="mono">
                      {f.ip}
                      {f.banned && <em className="tag tag-banned">已封禁</em>}
                      {!f.banned && f.count >= 10 && <em className="tag tag-risk">高频</em>}
                      <em className="net-caret">›</em>
                    </span>
                    <span className="ip-count">{f.count} 次</span>
                    <span>{f.lastTs ? fmtTime(f.lastTs) : '—'}</span>
                    <span className="mono">{f.user || '—'}</span>
                    <span>{geoCell(f.ip)}</span>
                  </div>
                  {open === key && (
                    <div className="net-detail">
                      <div><span>来源 IP</span><b className="mono">{f.ip}</b></div>
                      <div><span>失败次数</span><b>{f.count} 次</b></div>
                      <div><span>最近尝试</span><b>{f.lastTs ? fmtTime(f.lastTs) : '—'}</b></div>
                      <div><span>尝试账号</span><b className="mono">{f.user || '—'}</b></div>
                      <div><span>封禁状态</span><b>{banStatus(f.ip)}</b></div>
                      <div><span>归属地</span><b>{geoText(f.ip) || '未查询，点「查看归属」查询'}</b></div>
                      {banBtn(f.ip)}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
        <div className="banned-block">
          <span className="banned-title">封禁名单（{banBackendName}）：</span>
          {banned.length === 0
            ? <span className="banned-empty">当前无封禁 IP</span>
            : banned.map((ip) => (
              <span className="tag tag-banned mono" key={ip} title={banStatus(ip)}>
                {ip}
                {banMap.get(ip)?.backend === 'nft' && <i className="ban-src">nft</i>}
              </span>
            ))}
        </div>
        <p className="about-text" style={{ marginTop: 12, fontSize: 13 }}>
          失败登录明细来自 lastb / journalctl（跟随右上角时限选择：{rangeText}，按次数排序，最多 10 条）；封禁名单优先来自 fail2ban，
          未安装 fail2ban 时由本应用用 nft 独立封禁表拦截（封禁时长可选 1 小时 / 24 小时 / 一周 / 一年，到期由内核自动解封）。
          点击「查看归属」会把该 IP 发送至 ip-api.com 查询并缓存 24 小时，内网地址仅本地判定、不会外发。
        </p>
      </section>
      <section className="two-col">
        <div className="panel">
          <div className="panel-head">
            <div className="panel-title"><Icon name="list" size={16} /> 连接 Top 来源</div>
            <div className="panel-sub">点击任意行查看明细</div>
          </div>
          {conns.length === 0 && <div className="empty">暂无已建立的连接</div>}
          {conns.map((c) => {
            const ip = ipOf(c.remote)
            const d = detailOf(c.remote)
            const key = `c:${c.remote}`
            return (
              <div className={`net-row ${open === key ? 'open' : ''}`} key={c.remote}>
                <div className="net-head" onClick={() => toggleRow(key)}>
                  <span className="kv-key mono">
                    {c.remote}
                    {c.remote.startsWith('[') && <em className="tag tag-v6">IPv6</em>}
                  </span>
                  <span className="kv-val">
                    {c.count} 个连接 {geoCell(ip)} <em className="net-caret">›</em>
                  </span>
                </div>
                {open === key && (
                  <div className="net-detail">
                    <div><span>远端地址</span><b className="mono">{c.remote}</b></div>
                    <div><span>本机地址</span><b className="mono">{d.local || '—'}</b></div>
                    <div><span>协议 / 状态</span><b>{d.protocol || '—'} · {d.state || 'ESTAB'}</b></div>
                    <div><span>进程</span><b className="mono">{d.process ? `${d.process}${d.pid ? ` (PID ${d.pid})` : ''}` : '需 root 权限查看'}</b></div>
                    <div><span>连接数</span><b>{d.count || c.count} 个</b></div>
                    <div><span>归属地</span><b>{geoText(ip) || '未查询，点「查看归属」查询'}</b></div>
                    <div><span>封禁状态</span><b>{banStatus(ip)}</b></div>
                    {banBtn(ip)}
                  </div>
                )}
              </div>
            )
          })}
        </div>
        <div className="panel">
          <div className="panel-head">
            <div className="panel-title"><Icon name="check" size={16} /> 服务状态</div>
            <div className="panel-sub">已检测 {svcs.length} 项 · {activeSvcs} 项运行中</div>
          </div>
          {svcs.length === 0 && <div className="empty">未检测到 systemd 服务、Docker 容器或飞牛应用</div>}
          <div className="svc-list">
            {svcs.map((s) => (
              <div className={`kv-row ${s.active ? '' : 'kv-dim'}`} key={`${s.kind}-${s.name}`}>
                <span className="kv-key">
                  <StatusDot active={s.active} /> {s.name}
                  {s.kind === 'docker' && <em className="tag tag-kind">容器</em>}
                  {s.kind === 'appcenter' && <em className="tag tag-kind">应用</em>}
                </span>
                <span className="kv-val">{s.note}</span>
              </div>
            ))}
          </div>
        </div>
      </section>
      <section className="two-col">
        <div className="panel">
          <div className="panel-head">
            <div className="panel-title"><Icon name="net" size={16} /> 监听端口</div>
            <div className="panel-sub">{ports.length} 个端口 · {publicPorts} 个对外开放</div>
          </div>
          {ports.length === 0 ? (
            <div className="empty">未检测到监听端口（需 root 权限查看进程）</div>
          ) : (
            <div className="port-table">
              <div className="port-grid port-head">
                <span>端口</span><span>进程 / 监听地址</span><span>范围</span>
              </div>
              {ports.map((p, i) => (
                <div className="port-grid" key={`${p.port}-${p.addr}-${i}`}>
                  <span className="mono">
                    {p.port}
                    {p.proto === 'tcp6' && <em className="tag tag-v6">v6</em>}
                  </span>
                  <span className="port-proc">
                    <b className="mono ellipsis" title={p.process ? `${p.process} (PID ${p.pid})` : ''}>
                      {p.process || '—'}
                    </b>
                    <em className="mono ellipsis" title={p.addr}>{p.addr}</em>
                  </span>
                  <span>
                    {p.public
                      ? <em className="tag tag-risk">对外开放</em>
                      : <span className="muted">仅本机</span>}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
        <div className="panel">
          <div className="panel-head">
            <div className="panel-title"><Icon name="gear" size={16} /> 运行中服务</div>
            <div className="panel-sub">{running.length} 个 systemd 单元</div>
          </div>
          {running.length === 0 ? (
            <div className="empty">未检测到运行中的服务</div>
          ) : (
            <div className="svc-chips">
              {running.map((s) => (
                <span className="svc-chip" key={s.unit} title={s.desc || s.unit}>{s.unit}</span>
              ))}
            </div>
          )}
        </div>
      </section>
      <section className="panel">
        <div className="panel-title"><Icon name="info" size={16} /> 说明</div>
        <p className="about-text">
          吞吐数据来自真实网卡计数（每 30 秒采样）；连接明细由 ss -tanp 读取（含本机地址与进程，进程信息需 root 权限）；
          服务状态分三路检测：本机已安装的常见 systemd 服务（未安装的不再占位显示）、Docker 容器（含自部署的 Emby 等）、
          飞牛应用中心应用进程；监听端口由 ss -lntp 采集，「对外开放」表示绑定在所有网卡。
          封禁 / 解封优先通过 fail2ban-client 执行；未安装 fail2ban 时自动改用本应用的 nft 兜底封禁表（inet atff）。
          仅接受合法 IP，回环与本机网卡地址会直接拒绝，内网地址会先二次确认，失败原因原样显示。
        </p>
      </section>
        </>
      ) : (
        <WebAccess
          data={webData}
          err={webErr}
          range={range}
          bannedSet={bannedSet}
          banStatus={banStatus}
          banBtn={banBtn}
          scanIps={scanIps}
          onBanScans={banScans}
          scanActing={acting === '__scan__'}
          scanMsg={actMsg}
        />
      )}
      {showConns && (
        <div className="pay-mask" onClick={() => setShowConns(false)}>
          <div
            className="conn-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="活动连接"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="pay-dialog-head">
              <strong>活动连接 · {connList.length} 条</strong>
              <span className="conn-head-btns">
                <button type="button" className="btn btn-mini" disabled={connLoading} onClick={() => setConnTick((x) => x + 1)}>
                  {connLoading ? '刷新中…' : '刷新'}
                </button>
                <button type="button" className="btn btn-mini" onClick={() => setShowConns(false)}>关闭</button>
              </span>
            </div>
            <div className="conn-tools">
              <input
                className="search-input"
                placeholder="筛选 IP / 端口 / 进程"
                value={connFilter}
                onChange={(e) => setConnFilter(e.target.value)}
              />
              <span className="conn-count">{connLoading ? '读取中…' : `${connRows.length} / ${connList.length} 条`}</span>
            </div>
            {connMsg && <div className="conn-msg">{connMsg}</div>}
            <div className="conn-body">
              <div className="conn-grid conn-head-row">
                <span>本机地址</span><span>远端地址</span><span>进程</span><span className="conn-q">队列</span><span>操作</span>
              </div>
              {connRows.length === 0 ? (
                <div className="empty">{connList.length === 0 ? '没有已建立的连接（进程信息需 root 权限）' : '没有匹配的连接'}</div>
              ) : (
                connRows.map((c) => {
                  const key = `${c.local}|${c.remote}|${c.pid}`
                  return (
                    <div className="conn-grid conn-row" key={key}>
                      <span className="mono ellipsis" title={c.local}>{c.local}</span>
                      <span className="mono ellipsis" title={c.remote}>
                        {c.remote}
                        {c.protocol === 'tcp6' && <em className="tag tag-v6">v6</em>}
                      </span>
                      <span className="ellipsis" title={c.process ? `${c.process} (PID ${c.pid})` : '需 root 权限查看'}>
                        {c.process || '—'}{c.pid ? <em className="conn-pid">#{c.pid}</em> : null}
                      </span>
                      <span className="conn-q conn-qv">
                        {c.sendQ > 0 ? `待发 ${fmtBytes(c.sendQ)}` : c.recvQ > 0 ? `待收 ${fmtBytes(c.recvQ)}` : '—'}
                      </span>
                      <span>
                        {c.locked ? (
                          <span className="conn-lock" title={c.lockReason || '受保护的连接'}>已保护</span>
                        ) : (
                          <button
                            type="button"
                            className={`btn btn-mini btn-danger${c.sensitive ? ' btn-conn-warn' : ''}`}
                            disabled={connKilling === key}
                            title={c.sensitive ? `敏感端口：${c.sensitive}` : '断开这条连接'}
                            onClick={() => killConn(c)}
                          >
                            {connKilling === key ? '断开中…' : '断开'}
                          </button>
                        )}
                      </span>
                    </div>
                  )
                })
              )}
            </div>
            <p className="conn-tip">
              断开使用内核 SOCK_DESTROY（ss -K）直接中断这一条 TCP 连接，对端会立即收到 RST；不封禁 IP、也不结束进程，列表每 8 秒自动刷新。
              本应用自身端口的连接显示为「已保护」、无法断开；SSH / SMB / NFS 等敏感端口的按钮为橙色，确认时会额外提醒。
            </p>
          </div>
        </div>
      )}
      {procOpen && (
        <div className="pay-mask" onClick={() => setProcOpen(false)}>
          <div
            className="conn-dialog proc-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="进程流量明细"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="pay-dialog-head">
              <strong>进程流量明细 · {rangeText}</strong>
              <span className="conn-head-btns">
                <button type="button" className="btn btn-mini" disabled={procLoading} onClick={() => setProcTick((x) => x + 1)}>
                  {procLoading ? '刷新中…' : '刷新'}
                </button>
                <button type="button" className="btn btn-mini" onClick={() => setProcOpen(false)}>关闭</button>
              </span>
            </div>
            <div className="conn-tools">
              <span className="conn-count">
                已归因 {fmtTraffic(procTotal)} / 全机 {fmtTraffic(hostTotal)}（{procCoverage}%）
              </span>
              <span className="conn-count">
                {procData?.updatedAt ? `更新于 ${fmtTime(procData.updatedAt)}` : '等待首次采样'}
              </span>
            </div>
            {procErr && <div className="conn-msg">{procErr}</div>}
            <div className="conn-body">
              <div className="proc-grid proc-head-row">
                <span>进程</span><span className="proc-num">下行</span><span className="proc-num">上行</span><span>占比</span>
              </div>
              {procRows.length === 0 ? (
                <div className="empty">{procLoading ? '读取中…' : '暂无进程流量数据（仅统计 TCP，服务刚启动时需累计几分钟）'}</div>
              ) : (
                procRows.map((p) => {
                  const share = procTotal > 0 ? Math.round(((p.rx + p.tx) / procTotal) * 100) : 0
                  return (
                    <div className="proc-grid proc-row" key={p.name}>
                      <span className="ellipsis" title={p.pid ? `${p.name} (PID ${p.pid})` : p.name}>
                        {p.name}{p.pid ? <em className="conn-pid">#{p.pid}</em> : null}
                      </span>
                      <span className="proc-num">{fmtTraffic(p.rx)}</span>
                      <span className="proc-num">{fmtTraffic(p.tx)}</span>
                      <span className="proc-share">
                        <i style={{ width: `${Math.max(2, share)}%` }} />
                        <em>{share}%</em>
                      </span>
                    </div>
                  )
                })
              )}
            </div>
            <p className="conn-tip">
              数据来自内核 TCP socket 的累计字节（ss），每 3 秒差分一次，因此只覆盖 TCP：UDP 流量（如 DNS、QUIC）与存活短于 3 秒的连接不计入，
              同名进程会合并为一行。已归因合计通常小于上面的全机总量，两者不可直接相加或抵扣。
            </p>
          </div>
        </div>
      )}
      {zoomChart === 'net' && (
        <ZoomModal title="网络吞吐趋势" icon="net" onClose={() => setZoomChart('')}>
          <div className="legend">
            <span className="legend-item"><i style={{ background: '#43e7ad' }} />下行</span>
            <span className="legend-item"><i style={{ background: '#8b6bff' }} />上行</span>
          </div>
          <NetChart points={points} range={range} height={320} />
        </ZoomModal>
      )}
      {zoomChart === 'traffic' && (
        <ZoomModal
          title="流量用量"
          onClose={() => setZoomChart('')}
          headRight={<span className="zoom-sub">共 {fmtTraffic(trafficRx + trafficTx)}（↓ {fmtTraffic(trafficRx)} / ↑ {fmtTraffic(trafficTx)}）</span>}
        >
          <div className="legend">
            <span className="legend-item"><i style={{ background: '#43e7ad' }} />下行</span>
            <span className="legend-item"><i style={{ background: '#8b6bff' }} />上行</span>
          </div>
          <TrafficChart points={traffic} range={range} height={320} />
        </ZoomModal>
      )}
    </div>
  )
}

/* ---------- 页面：健康度 ---------- */
function Health({ data, history, range, onRange }) {
  const [zoom, setZoom] = useState('')
  if (!data) return null
  const bItems = data.baselineItems || []
  const bTotal = bItems.reduce((s, it) => s + (Number(it.max) || 0), 0)
  const bEarned = bItems.reduce((s, it) => s + Math.min(Number(it.score) || 0, Number(it.max) || 0), 0)
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h2>健康度</h2>
          <div className="page-sub">健康指数由系统真实资源加权得出，附逐项构成与安全基线检查</div>
        </div>
      </div>
      <section className="stat-grid three">
        <div className="stat-card big">
          <div className="stat-title"><img className="stat-icon" src={siSync} alt="" /> NAS 健康指数</div>
          <div className="stat-huge">{data.healthIndex}<span className="stat-unit">%</span></div>
          <div className="stat-foot">↑ {data.healthText}</div>
        </div>
        <StatCard icon={siStatus} title="防护状态" watermark={wmStatus} foot={data.statusText}>
          <div className="stat-value-lg"><StatusDot active={data.active} /> {data.active ? '正常运行' : '未运行'}</div>
        </StatCard>
        <StatCard icon={siTime} title="运行时长" watermark={wmTime} foot="持续守护中">
          <div className="stat-value-lg">{data.uptimeText}</div>
        </StatCard>
      </section>
      <section className="two-col even">
        <div className="panel">
          <div className="panel-head">
            <PanelTitle icon="chart" onZoom={() => setZoom('trend')}>健康指数趋势</PanelTitle>
            <RangePicker value={range} onChange={onRange} />
          </div>
          <LineChart points={history} target={data.healthIndex} height={260} range={range} refs={[85, 70, 50]} />
        </div>
        <div className="panel baseline-panel">
          <div className="panel-head">
            <PanelTitle icon="check" onZoom={() => setZoom('baseline')}>安全基线构成</PanelTitle>
            {bTotal > 0 && (
              <div className="panel-sub">{bEarned} / {bTotal} 分 · 扣分 {bTotal - bEarned}</div>
            )}
          </div>
          <BaselineDonut items={bItems} />
        </div>
      </section>
      <section className="two-col">
      <div className="panel">
        <div className="panel-head">
          <div className="panel-title"><Icon name="chart" size={16} /> 健康指数构成</div>
          <div className="panel-sub">各项得分按权重折算，合计即当前 {data.healthIndex}%</div>
        </div>
        <div className="bp-list">
          {(data.healthParts || []).map((p) => (
            <div className="bp-row" key={p.label}>
              <div className="bp-name">{p.label}</div>
              <div className="bp-bar">
                <div className={`bp-fill ${p.score < 70 ? 'bp-low' : p.score < 90 ? 'bp-mid' : ''}`} style={{ width: `${Math.max(0, Math.min(100, p.score))}%` }} />
              </div>
              <div className="bp-score">{p.score}</div>
              <div className="bp-weight">权重 {p.weight}%</div>
              <div className={`bp-loss ${p.loss > 0 ? 'bp-loss-on' : ''}`}>{p.loss > 0 ? `-${p.loss}` : '满分'}</div>
            </div>
          ))}
          {(!data.healthParts || data.healthParts.length === 0) && <div className="empty">等待采样数据</div>}
        </div>
      </div>
      <div className="panel">
        <div className="panel-head">
          <div className="panel-title"><Icon name="check" size={16} /> 安全基线检查</div>
          <div className="baseline-sum"><b>{data.defensePower}</b> / 100 分</div>
        </div>
        <div className="baseline-list">
          {(data.baselineItems || []).map((it) => (
            <div className="baseline-row" key={it.name}>
              <div>
                <div className="baseline-name"><StatusDot active={it.ok} /> {it.name}</div>
                <div className="baseline-note">{it.note}</div>
              </div>
              <ProgressBar
                value={it.score}
                max={it.max}
                tone={it.ok ? 'green' : 'amber'}
              />
              <div className="baseline-score">{it.score} / {it.max}</div>
            </div>
          ))}
          {(!data.baselineItems || data.baselineItems.length === 0) && <div className="empty">暂无检查结果</div>}
        </div>
        <p className="about-text" style={{ marginTop: 14, fontSize: 13 }}>
          检查项来自真实系统巡检：飞牛防火墙开关（读 /usr/trim/etc/fw.conf，规则数不含 Docker）、fail2ban 或本应用 nft 兜底封禁、
          近 24 小时 SSH 失败登录、SMART 硬盘健康与 sshd 密码登录配置，均每 5 分钟刷新一次。
        </p>
      </div>
      </section>
      <section className="panel">
        <div className="panel-title"><Icon name="chart" size={16} /> 实时资源构成</div>
        <div className="info-grid">
          <div><span>CPU 使用率</span><b>{fmtPct(data.cpuUsagePct)}</b></div>
          <div><span>内存占用</span><b>{fmtPct(data.memUsedPct)}</b></div>
          <div><span>磁盘占用</span><b>{fmtPct(data.diskUsedPct)}</b></div>
          <div><span>系统负载（1 分钟）</span><b>{data.loadAvg1 >= 0 ? data.loadAvg1.toFixed(2) : '—'}</b></div>
          <div><span>网络下行 / 上行</span><b>{fmtRate(data.netRxKBps)} / {fmtRate(data.netTxKBps)}</b></div>
          <div><span>磁盘读 / 写</span><b>{fmtRate(data.diskReadKBps)} / {fmtRate(data.diskWriteKBps)}</b></div>
          <div><span>活动连接数</span><b>{data.establishedConns} 个</b></div>
          <div><span>CPU 温度</span><b>{data.tempC > 0 ? `${data.tempC.toFixed(1)} ℃` : '—'}</b></div>
          <div><span>硬盘温度</span><b>{data.diskTempC > 0 ? `${data.diskTempC.toFixed(1)} ℃` : '—'}</b></div>
          <div><span>在线用户</span><b>{data.loggedInUsers} 人</b></div>
          <div><span>UPS 电量</span><b>{data.upsBattery >= 0 ? `${data.upsBattery}%` : '未接 UPS'}</b></div>
        </div>
      </section>
      <section className="panel">
        <div className="panel-title"><Icon name="chart" size={16} /> 存储分区</div>
        <div className="info-grid">
          {(data.diskVolumes || []).map((v, i) => (
            <div key={i}>
              <span>{v.mount}（{v.device}）</span>
              <b>{v.usedGB} / {v.totalGB} GB</b>
            </div>
          ))}
          {(!data.diskVolumes || data.diskVolumes.length === 0) && <div><span>存储分区</span><b>—</b></div>}
        </div>
      </section>
      <section className="panel">
        <div className="panel-title"><Icon name="info" size={16} /> 关于健康指数</div>
        <p className="about-text">
          健康指数取自 NAS 的真实资源状态：内存余量与磁盘余量各占 30%，CPU 空闲占 20%，系统负载与温度各占 10%。
          服务每 30 秒采样一次并保留 7 天历史，指数越高说明系统余量越充足、运行越稳定。
          当同步率与飞牛共鸣至 200%，开心之壁的绝对领域将为你的 NAS 展开。
        </p>
      </section>
      {zoom === 'trend' && (
        <ZoomModal title="健康指数趋势" onClose={() => setZoom('')}>
          <LineChart points={history} target={data.healthIndex} height={320} range={range} refs={[85, 70, 50]} />
        </ZoomModal>
      )}
      {zoom === 'baseline' && (
        <ZoomModal
          title="安全基线构成"
          icon="check"
          onClose={() => setZoom('')}
          headRight={bTotal > 0 ? <span className="zoom-sub">{bEarned} / {bTotal} 分 · 扣分 {bTotal - bEarned}</span> : null}
        >
          <BaselineDonut items={bItems} />
        </ZoomModal>
      )}
    </div>
  )
}

/* ---------- 页面：设置 ---------- */
function Settings({ data, onSave, onEnable, onDisable, onDirtyChange }) {
  const [cfg, setCfg] = useState(null)
  const [saved, setSaved] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [testMsg, setTestMsg] = useState('')
  // 导出 / 导入配置的结果提示，就地显示在「一键操作」按钮下方
  const [ioMsg, setIoMsg] = useState('')
  // 「更多」菜单（导出诊断包 / 导出配置 / 导入配置 / 恢复默认）是否展开
  const [moreOpen, setMoreOpen] = useState(false)
  const [alerts, setAlerts] = useState([])
  // 隐藏的文件选择框，供「导入配置」触发
  const fileRef = useRef(null)
  // 只在首次拿到数据时填充表单：实时推送（SSE）每 5 秒会刷新 data，
  // 若每次都覆盖 cfg，用户改到一半未保存的内容就会被冲掉。
  useEffect(() => { if (data) setCfg((prev) => prev || data.config) }, [data])
  // 把未保存状态上抛给 App，用于离开设置页/关闭窗口前提醒
  useEffect(() => { if (onDirtyChange) onDirtyChange(dirty) }, [dirty, onDirtyChange])
  useEffect(() => () => { if (onDirtyChange) onDirtyChange(false) }, [onDirtyChange])
  useEffect(() => {
    api.alerts(10).then((r) => setAlerts(r.logs || [])).catch(() => {})
  }, [])
  if (!cfg || !data) return null
  const alert = cfg.alert || {}
  const setAlert = (patch) => {
    setCfg({ ...cfg, alert: { ...alert, ...patch } })
    setSaved(false)
    setDirty(true)
  }
  const testAlert = async () => {
    setTestMsg('发送中…')
    try {
      await api.alertTest()
      setTestMsg('测试消息已发送，请检查飞书 / 企业微信 / 钉钉机器人')
    } catch (e) {
      setTestMsg(e.message)
    }
    setTimeout(() => setTestMsg(''), 6000)
  }
  const toggle = (key) => {
    const next = { ...cfg, [key]: !cfg[key] }
    setCfg(next)
    setSaved(false)
    setDirty(true)
  }
  // 扫描防护：默认关闭，服务端会校验阈值（10~10000）与封禁时长白名单
  const scanBan = cfg.scanBan || { enabled: false, threshold: 10, duration: '24h' }
  const setScanBan = (patch) => {
    setCfg({ ...cfg, scanBan: { ...scanBan, ...patch } })
    setSaved(false)
    setDirty(true)
  }
  const save = async () => {
    try {
      const res = await onSave(cfg)
      if (res && res.alert) {
        const next = { ...cfg, ...res }
        setCfg(next)
        // 服务端会做范围校验并回落默认值，若与输入不同则明确提示，避免「以为存了」
        const keys = ['healthMin', 'diskPct', 'memPct', 'volumePct', 'tempC', 'diskTempC', 'loginFailCount', 'badAccessCount']
        const adjusted =
          keys.some((k) => Number(cfg.alert[k]) !== Number(res.alert[k])) ||
          (res.scanBan && Number(scanBan.threshold) !== Number(res.scanBan.threshold))
        setTestMsg(adjusted ? '部分数值超出有效范围，已按服务端校验结果调整并保存' : '')
      }
      setDirty(false)
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } catch (err) {
      setTestMsg('保存失败：' + err.message)
    }
  }
  const resetDefaults = async () => {
    if (!window.confirm('确定恢复默认配置吗？将清空已填写的 Webhook 与签名密钥，并重置全部阈值。')) return
    try {
      const res = await api.resetConfig()
      setCfg(res)
      setDirty(false)
      setSaved(false)
      setTestMsg('已恢复默认配置')
      setTimeout(() => setTestMsg(''), 4000)
    } catch (e) {
      setTestMsg('恢复默认失败：' + e.message)
    }
  }
  // 导出当前界面上的设置（含未保存改动）为 JSON。
  // 外面套一层 meta，导入时据此认出「这确实是本应用的配置文件」；config 里仍是可直接 PUT 的配置本体。
  const exportConfig = () => {
    try {
      const d = new Date()
      const p = (n) => String(n).padStart(2, '0')
      const payload = {
        app: 'AT-ForceField',
        kind: 'config',
        version: data.version || 'dev',
        exportedAt: d.toISOString(),
        config: cfg,
      }
      const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json;charset=utf-8' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `飞牛AT力场_配置_${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}.json`
      a.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      setIoMsg(dirty ? '配置已导出（含界面上未保存的改动）' : '配置已导出为 JSON（包含 Webhook 明文，请妥善保管）')
      setTimeout(() => setIoMsg(''), 6000)
    } catch (e) {
      setIoMsg('导出失败：' + e.message)
    }
  }
  // 导入配置：先在本地把文件挡一遍（格式 / 归属 / 字段类型），
  // 通过后再整份交给服务端 PUT /api/config；服务端还会再校验 Webhook 并补齐缺省值，
  // 所以文件有问题时现有配置不会被改动。
  const importConfig = async (file) => {
    const flash = (msg) => {
      setIoMsg(msg)
      setTimeout(() => setIoMsg(''), 9000)
    }
    const fail = (msg) => flash('导入失败：' + msg)
    if (!file) return
    if (file.size > 256 * 1024) return fail('文件过大（配置 JSON 一般只有几 KB）')
    let raw
    try {
      raw = JSON.parse(await file.text())
    } catch (_) {
      return fail('不是有效的 JSON 文件')
    }
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return fail('JSON 顶层不是对象')
    if (raw.app && raw.app !== 'AT-ForceField') return fail(`不是飞牛AT力场的配置文件（app = ${String(raw.app).slice(0, 40)}）`)
    // 兼容 0.1.52 及更早导出的裸配置（当时没有 meta 包装）
    const next = raw.config && typeof raw.config === 'object' ? raw.config : raw
    const flagKeys = ['atDefense', 'invadeScan', 'dataSeal']
    if (!flagKeys.some((k) => k in next) && !('alert' in next)) return fail('文件里找不到可识别的配置项')
    for (const k of flagKeys) {
      if (k in next && typeof next[k] !== 'boolean') return fail(`${k} 应为 true / false`)
    }
    const al = next.alert
    if (al !== undefined && al !== null) {
      if (typeof al !== 'object' || Array.isArray(al)) return fail('alert 字段格式不对')
      const alBool = ['monitor', 'push', 'watchHealth', 'watchDisk', 'watchMem', 'watchVolume', 'watchTemp', 'watchDiskTemp', 'watchAutoBan', 'watchLoginFail', 'watchBadAccess']
      const alNum = ['healthMin', 'diskPct', 'memPct', 'volumePct', 'tempC', 'diskTempC', 'loginFailCount', 'badAccessCount']
      const alStr = ['webhook', 'wecomWebhook', 'feishuSecret', 'dingWebhook', 'dingSecret']
      for (const k of alBool) if (k in al && typeof al[k] !== 'boolean') return fail(`alert.${k} 应为 true / false`)
      for (const k of alNum) if (k in al && (typeof al[k] !== 'number' || !isFinite(al[k]))) return fail(`alert.${k} 应为数字`)
      for (const k of alStr) if (k in al && typeof al[k] !== 'string') return fail(`alert.${k} 应为字符串`)
    }
    const countHooks = (c) => (c && c.alert ? ['webhook', 'wecomWebhook', 'dingWebhook'].filter((k) => c.alert[k]).length : 0)
    const sb = next.scanBan
    if (sb !== undefined && sb !== null) {
      if (typeof sb !== 'object' || Array.isArray(sb)) return fail('scanBan 字段格式不对')
      if ('enabled' in sb && typeof sb.enabled !== 'boolean') return fail('scanBan.enabled 应为 true / false')
      if ('threshold' in sb && (typeof sb.threshold !== 'number' || !isFinite(sb.threshold))) return fail('scanBan.threshold 应为数字')
      if ('duration' in sb && typeof sb.duration !== 'string') return fail('scanBan.duration 应为字符串')
    }
    if (!window.confirm(`导入会覆盖当前全部设置（防护开关 ${flagKeys.filter((k) => next[k]).length}/3 项开启，扫描防护${next.scanBan && next.scanBan.enabled ? '开启' : '关闭'}，推送渠道 ${countHooks(next)} 个）。确定继续吗？`)) return
    try {
      const res = await onSave(next)
      const saved = res && res.alert ? res : next
      setCfg(saved)
      setDirty(false)
      setSaved(false)
      flash(`已导入并保存：防护 ${flagKeys.filter((k) => saved[k]).length}/3 项开启，推送渠道 ${countHooks(saved)} 个`)
    } catch (e) {
      fail(e.message)
    }
  }
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h2>设置</h2>
          <div className="page-sub">防护开关、告警阈值与推送渠道，修改后需点击「保存设置」生效</div>
        </div>
        {dirty && <span className="dirty-badge">有未保存的修改</span>}
      </div>
      <section className="panel">
        <div className="panel-title"><Icon name="check" size={16} /> 防护功能</div>
        {[
          ['atDefense', 'AT 力场防护', '构建安全屏障，抵御各类威胁'],
          ['invadeScan', '实时入侵检测', '智能监控异常行为，及时阻止潜在风险'],
          ['dataSeal', '数据安全加固', '多重防护机制，保障数据安全'],
        ].map(([key, title, desc]) => (
          <div className="setting-row" key={key}>
            <div>
              <div className="setting-title">{title}</div>
              <div className="setting-desc">{desc}</div>
            </div>
            <button className={`switch ${cfg[key] ? 'switch-on' : ''}`} onClick={() => toggle(key)}>
              <span className="switch-knob" />
            </button>
          </div>
        ))}
      </section>
      <section className="panel">
        <div className="panel-head">
          <div className="panel-title"><Icon name="net" size={16} /> 扫描防护</div>
          <div className="panel-sub">异常请求过多的来源 IP 自动封禁</div>
        </div>
        <div className="setting-row">
          <div>
            <div className="setting-title">自动封禁疑似扫描 IP</div>
            <div className="setting-desc">
              {scanBan.enabled
                ? `已开启：每 10 秒检查一次访问日志，1 分钟内异常请求达到 ${scanBan.threshold} 次的公网 IP 自动封禁 ${banDurLabel(scanBan.duration)}`
                : '已关闭：仅在「访问分析」里标记疑似扫描 IP，需手动封禁'}
            </div>
          </div>
          <button className={`switch ${scanBan.enabled ? 'switch-on' : ''}`} onClick={() => setScanBan({ enabled: !scanBan.enabled })}>
            <span className="switch-knob" />
          </button>
        </div>
        <div className="form-row">
          <label className="form-label">异常请求阈值（次 / 1 分钟）</label>
          <input
            className="text-input"
            type="number"
            min={10}
            max={10000}
            step={10}
            disabled={!scanBan.enabled}
            value={scanBan.threshold}
            onChange={(e) => setScanBan({ threshold: e.target.value === '' ? '' : Number(e.target.value) })}
            onBlur={(e) => {
              let v = Number(e.target.value)
              if (e.target.value === '' || !isFinite(v)) v = 10
              v = Math.min(10000, Math.max(10, Math.round(v)))
              if (v !== scanBan.threshold) setScanBan({ threshold: v })
            }}
          />
        </div>
        <div className="form-row">
          <label className="form-label">封禁时长</label>
          <div className="ban-dur" role="group" aria-label="扫描防护封禁时长">
            {BAN_DURS.map(([v, label]) => (
              <button
                key={v}
                type="button"
                className={`ban-dur-btn ${scanBan.duration === v ? 'on' : ''}`}
                disabled={!scanBan.enabled}
                onClick={() => setScanBan({ duration: v })}
              >
                {label}
              </button>
            ))}
          </div>
        </div>
        <div className="setting-desc" style={{ marginTop: 4 }}>
          只对公网 IP 生效：回环、内网、链路本地地址一律跳过，避免把 NAS 自身或局域网设备挡在门外；
          触发封禁会写入防护日志（模块「扫描防护」），同一 IP 在封禁期内不会重复提交。
        </div>
      </section>
      <section className="panel">
        <div className="panel-head">
          <div className="panel-title"><Icon name="info" size={16} /> 告警设置</div>
        </div>
        <div className="setting-row">
          <div>
            <div className="setting-title">监控与告警</div>
            <div className="setting-desc">
              {alert.monitor
                ? '已开启：每 30 秒检查一次阈值，触发时写入防护日志，并按右侧推送设置发送消息'
                : '已关闭：不检查阈值，也不写入防护日志、不发送任何消息'}
            </div>
          </div>
          <button className={`switch ${alert.monitor ? 'switch-on' : ''}`} onClick={() => setAlert({ monitor: !alert.monitor })}>
            <span className="switch-knob" />
          </button>
        </div>
        <div className="setting-row">
          <div>
            <div className="setting-title">消息推送（飞书 / 企业微信 / 钉钉）</div>
            <div className="setting-desc">
              {!alert.monitor
                ? '「监控与告警」关闭时此项不生效'
                : alert.push
                ? '已开启：触发告警时推送到下方 Webhook 地址'
                : '已关闭：照常写入防护日志，只是不发飞书 / 企业微信 / 钉钉消息'}
            </div>
          </div>
          <button
            className={`switch ${alert.push && alert.monitor ? 'switch-on' : ''}`}
            title={alert.monitor ? '' : '请先开启「监控与告警」'}
            onClick={() => setAlert({ push: !alert.push })}
          >
            <span className="switch-knob" />
          </button>
        </div>
        <div className="form-row">
          <label className="form-label">飞书机器人 Webhook</label>
          <input
            className="text-input"
            placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/…"
            value={alert.webhook || ''}
            onChange={(e) => setAlert({ webhook: e.target.value })}
          />
        </div>
        <div className="form-row">
          <label className="form-label">企业微信机器人 Webhook</label>
          <input
            className="text-input"
            placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…"
            value={alert.wecomWebhook || ''}
            onChange={(e) => setAlert({ wecomWebhook: e.target.value })}
          />
        </div>
        <div className="form-row">
          <label className="form-label">钉钉机器人 Webhook</label>
          <input
            className="text-input"
            placeholder="https://oapi.dingtalk.com/robot/send?access_token=…"
            value={alert.dingWebhook || ''}
            onChange={(e) => setAlert({ dingWebhook: e.target.value })}
          />
        </div>
        <div className="form-row">
          <label className="form-label">飞书签名校验密钥</label>
          <input
            className="text-input"
            type="password"
            autoComplete="new-password"
            placeholder="可选：飞书机器人「签名校验」密钥，填写后推送自动加签"
            value={alert.feishuSecret || ''}
            onChange={(e) => setAlert({ feishuSecret: e.target.value })}
          />
        </div>
        <div className="form-row">
          <label className="form-label">钉钉加签密钥</label>
          <input
            className="text-input"
            type="password"
            autoComplete="new-password"
            placeholder="可选：钉钉机器人「加签」密钥（SEC 开头），填写后推送自动加签"
            value={alert.dingSecret || ''}
            onChange={(e) => setAlert({ dingSecret: e.target.value })}
          />
        </div>
        <div className="threshold-grid">
          {[
            ['healthMin', '健康指数低于（%）', 1, 100, 'watchHealth'],
            ['diskPct', '磁盘使用率（%）', 1, 100, 'watchDisk'],
            ['memPct', '内存使用率（%）', 1, 100, 'watchMem'],
            ['volumePct', '单个分区（%）', 1, 100, 'watchVolume'],
            ['tempC', 'CPU 温度（℃）', 30, 120, 'watchTemp'],
            ['diskTempC', '硬盘温度（℃）', 30, 100, 'watchDiskTemp'],
          ].map(([key, label, lo, hi, watch]) => {
            // 单项监控开关：默认开启，取消勾选后该项不判定、不写日志
            const on = alert[watch] !== false
            return (
              <div className={`form-row compact thr-row${on ? '' : ' thr-off'}`} key={key}>
                <label className="form-label thr-label" title={on ? '取消勾选可关闭此项监控' : '已关闭此项监控'}>
                  <input
                    type="checkbox"
                    className="thr-check"
                    checked={on}
                    onChange={() => setAlert({ [watch]: !on })}
                  />
                  <span>{label}</span>
                </label>
                <input
                  className="text-input"
                  type="number"
                  min={lo}
                  max={hi}
                  step="1"
                  disabled={!on}
                  title={`有效范围 ${lo} - ${hi}`}
                  value={alert[key] ?? ''}
                  onChange={(e) => setAlert({ [key]: e.target.value === '' ? '' : Number(e.target.value) })}
                  onBlur={(e) => {
                    // 失焦时就地夹到有效区间，避免提交后被服务端静默改回默认值
                    let v = Number(e.target.value)
                    if (e.target.value === '' || Number.isNaN(v)) v = lo
                    v = Math.min(hi, Math.max(lo, Math.round(v)))
                    if (v !== alert[key]) setAlert({ [key]: v })
                  }}
                />
              </div>
            )
          })}
        </div>
        <div className="panel-sub" style={{ marginTop: 12 }}>
          安全事件推送（需同时开启上方「监控与告警」与「消息推送」，同类事件 30 分钟内只推一次）
        </div>
        <div className="setting-row">
          <div>
            <div className="setting-title">自动封禁 IP 通知</div>
            <div className="setting-desc">扫描防护自动封禁疑似扫描 IP 时推送汇总，含 IP、触发阈值与封禁时长</div>
          </div>
          <button
            className={`switch ${alert.watchAutoBan && alert.monitor ? 'switch-on' : ''}`}
            title={alert.monitor ? '' : '请先开启「监控与告警」'}
            onClick={() => setAlert({ watchAutoBan: !alert.watchAutoBan })}
          >
            <span className="switch-knob" />
          </button>
        </div>
        <div className="setting-row">
          <div>
            <div className="setting-title">SSH 异常登录提醒</div>
            <div className="setting-desc">最近 1 分钟失败登录达到设定次数时推送，附尝试最多的来源 IP</div>
          </div>
          <div className="alert-inline">
            <input
              className="text-input alert-num"
              type="number"
              min={3}
              max={1000}
              step="1"
              disabled={!alert.monitor || !alert.watchLoginFail}
              title="有效范围 3 - 1000 次"
              value={alert.loginFailCount ?? ''}
              onChange={(e) => setAlert({ loginFailCount: e.target.value === '' ? '' : Number(e.target.value) })}
              onBlur={(e) => {
                let v = Number(e.target.value)
                if (e.target.value === '' || Number.isNaN(v)) v = 20
                v = Math.min(1000, Math.max(3, Math.round(v)))
                if (v !== alert.loginFailCount) setAlert({ loginFailCount: v })
              }}
            />
            <button
              className={`switch ${alert.watchLoginFail && alert.monitor ? 'switch-on' : ''}`}
              title={alert.monitor ? '' : '请先开启「监控与告警」'}
              onClick={() => setAlert({ watchLoginFail: !alert.watchLoginFail })}
            >
              <span className="switch-knob" />
            </button>
          </div>
        </div>
        <div className="setting-row">
          <div>
            <div className="setting-title">异常访问突增提醒</div>
            <div className="setting-desc">单个 IP 在 1 分钟内 4xx / 5xx 达到设定次数时推送，不依赖是否开启自动封禁</div>
          </div>
          <div className="alert-inline">
            <input
              className="text-input alert-num"
              type="number"
              min={10}
              max={10000}
              step="1"
              disabled={!alert.monitor || !alert.watchBadAccess}
              title="有效范围 10 - 10000 次"
              value={alert.badAccessCount ?? ''}
              onChange={(e) => setAlert({ badAccessCount: e.target.value === '' ? '' : Number(e.target.value) })}
              onBlur={(e) => {
                let v = Number(e.target.value)
                if (e.target.value === '' || Number.isNaN(v)) v = 60
                v = Math.min(10000, Math.max(10, Math.round(v)))
                if (v !== alert.badAccessCount) setAlert({ badAccessCount: v })
              }}
            />
            <button
              className={`switch ${alert.watchBadAccess && alert.monitor ? 'switch-on' : ''}`}
              title={alert.monitor ? '' : '请先开启「监控与告警」'}
              onClick={() => setAlert({ watchBadAccess: !alert.watchBadAccess })}
            >
              <span className="switch-knob" />
            </button>
          </div>
        </div>
        <div className="btn-row">
          <button className="btn" onClick={testAlert}>发送测试消息</button>
          <button className={`btn btn-save ${dirty ? 'btn-dirty' : ''}`} onClick={save}>
            {saved ? '✓ 已保存' : dirty ? '保存设置（有未保存修改）' : '保存设置'}
          </button>
          {testMsg && <span className="test-msg">{testMsg}</span>}
        </div>
        <p className="setting-desc" style={{ marginTop: 10 }}>
          每项前面的勾选框是该指标的独立监控开关，默认全部勾选；取消勾选后该项不判定阈值、不写日志、不推送（其余项不受影响）。
          阈值触发后写入防护日志；填写 Webhook 后同时推送到飞书 / 企业微信 / 钉钉（填几个就各推一份），同一告警 30 分钟内只推送一次，应用重启后冷却时间仍然有效。
          Webhook 仅接受 https 的飞书 / 企业微信 / 钉钉官方机器人地址；机器人开启签名（飞书）或加签（钉钉）时，需同时填写密钥；推送失败（含重试 2 次）会记入防护日志。
          指标进入告警时推送一条通知，回落到阈值以下会再推一条「已恢复」，其中磁盘/内存/温度严重超限会标记为严重级别。
          安全事件部分默认关闭：开启「自动封禁 IP 通知」后，扫描防护每轮封禁会汇总推送一条；开启「SSH 异常登录提醒」或「异常访问突增提醒」后，后台每 30 秒按最近 1 分钟窗口检查一次，达到设定次数即推送，同类事件 30 分钟内只推一次。
          <b> 以上阈值与 Webhook 都要点「保存设置」才会写入配置并生效。</b>
        </p>
      </section>
      <section className="panel">
        <div className="panel-title"><Icon name="list" size={16} /> 最近告警</div>
        {alerts.length === 0 && <div className="empty">暂无告警记录</div>}
        {alerts.map((l, i) => (
          <div className="log-row" key={`${l.time}-${i}`}>
            <span className="log-time">{fmtTime(l.time)}</span>
            <span className="log-level lv-warn">警告</span>
            <span className="log-module">{l.module}</span>
            <span className="log-msg">{l.msg}</span>
          </div>
        ))}
      </section>
      <section className="panel">
        <div className="panel-title">一键操作</div>
        <div className="btn-row">
          <button className="btn btn-primary" onClick={onEnable}>一键启用防护</button>
          <button className="btn" onClick={onDisable}>关闭全部防护</button>
          <button className={`btn btn-save ${dirty ? 'btn-dirty' : ''}`} onClick={save}>{saved ? '✓ 已保存' : dirty ? '保存设置（有未保存修改）' : '保存设置'}</button>
          {/* 低频且不可逆的操作收进「更多」，主行只留三个高频主操作，手机端不再挤成两行 */}
          <div className="more-wrap">
            <button
              type="button"
              className={`btn more-btn${moreOpen ? ' more-on' : ''}`}
              aria-expanded={moreOpen}
              onClick={() => setMoreOpen((v) => !v)}
            >
              更多 <span className="more-caret">▾</span>
            </button>
            {moreOpen && (
              <>
                <button type="button" className="more-mask" aria-label="关闭菜单" onClick={() => setMoreOpen(false)} />
                <div className="more-menu" role="menu">
                  <button type="button" className="btn" role="menuitem" onClick={() => { setMoreOpen(false); window.location.href = api.diagnosticsUrl() }}>导出诊断包</button>
                  <button type="button" className="btn" role="menuitem" onClick={() => { setMoreOpen(false); exportConfig() }}>导出配置</button>
                  <button type="button" className="btn" role="menuitem" onClick={() => { setMoreOpen(false); fileRef.current && fileRef.current.click() }}>导入配置</button>
                  <button type="button" className="btn" role="menuitem" onClick={() => { setMoreOpen(false); resetDefaults() }}>恢复默认</button>
                </div>
              </>
            )}
          </div>
          <input
            ref={fileRef}
            type="file"
            accept=".json,application/json"
            style={{ display: 'none' }}
            onChange={(e) => {
              const f = e.target.files && e.target.files[0]
              e.target.value = '' // 清空，选同一个文件也能再次触发
              importConfig(f)
            }}
          />
        </div>
        {ioMsg && <p className="io-msg">{ioMsg}</p>}
        <p className="setting-desc" style={{ marginTop: 10 }}>
          「更多」菜单里是导出诊断包 / 导出配置 / 导入配置 / 恢复默认。导出配置会把当前界面上的设置（含未保存的改动）下载成 JSON，内含 Webhook 明文，请妥善保管；导入配置选择这份 JSON 即可整份还原，会覆盖现有设置，Webhook 不合法时直接拒绝、不会改动现有配置；恢复默认会清空 Webhook 与签名密钥并重置阈值，操作前会二次确认。
        </p>
      </section>
      <section className="panel">
        <div className="panel-title"><Icon name="info" size={16} /> 系统信息</div>
        <div className="info-grid">
          <div><span>CPU 使用率</span><b>{fmtPct(data.cpuUsagePct)}（{cpuLabel(data.cpuCores, data.cpuPhysical)}）</b></div>
          <div><span>物理内存</span><b>{data.totalMemMB ? `${data.totalMemMB} MB` : '—'}</b></div>
          <div><span>可用内存</span><b>{data.availableMB ? `${data.availableMB} MB` : '—'}</b></div>
          <div><span>磁盘容量</span><b>{data.diskTotalGB ? `${data.diskUsedGB} / ${data.diskTotalGB} GB` : '—'}</b></div>
          <div><span>系统负载</span><b>{data.loadAvg1 >= 0 ? data.loadAvg1.toFixed(2) : '—'}</b></div>
          <div><span>CPU 温度</span><b>{data.tempC > 0 ? `${data.tempC.toFixed(1)} ℃` : '—'}</b></div>
          <div><span>硬盘温度</span><b>{data.diskTempC > 0 ? `${data.diskTempC.toFixed(1)} ℃` : '—'}</b></div>
          <div><span>在线用户</span><b>{data.loggedInUsers} 人</b></div>
          <div><span>UPS 电量</span><b>{data.upsBattery >= 0 ? `${data.upsBattery}%` : '未接 UPS'}</b></div>
          <div><span>活动连接</span><b>{data.establishedConns} 个</b></div>
        </div>
      </section>
      <section className="panel">
        <div className="panel-title"><Icon name="chart" size={16} /> 数据采集</div>
        <div className="setting-row">
          <div>
            <div className="setting-title">采样间隔：30 秒</div>
            <div className="setting-desc">健康指数、CPU、内存、磁盘、网络、连接数与温度均由 NAS 系统实时采集</div>
          </div>
        </div>
        <div className="setting-row">
          <div>
            <div className="setting-title">已采集 {data.sampleCount} 个采样点</div>
            <div className="setting-desc">
              {data.source === 'real'
                ? '当前为真实系统指标，历史数据保留 7 天'
                : '当前环境无法读取系统指标，界面展示为模拟值'}
            </div>
          </div>
        </div>
      </section>
    </div>
  )
}

/* ---------- 页面：关于 ---------- */
function About({ data }) {
  const version = data?.version || 'dev'
  const [showPay, setShowPay] = useState(false)
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h2>关于</h2>
          <div className="page-sub">版本信息、功能说明与联系方式</div>
        </div>
      </div>
      <section className="panel about-panel">
        <div className="about-head">
          <img className="app-badge lg" src={appIconHd} alt="飞牛AT力场" />
          <div>
            <h3>飞牛AT力场</h3>
            <div className="hero-sub">实用效率 · 版本 {version}</div>
          </div>
        </div>
        <p className="about-text">
          飞牛AT力场，为飞牛 NAS 提供的硬件防护解决方案。<br />
          通过模拟 AT 力场的防护机制，在飞牛系统底层为您的 NAS 提供全方位的安全防护。
          包括系统入侵防护、数据保护、异常行为检测等功能，守护您的数字资产安全。
        </p>
        <p className="about-text highlight">
          当使用者同步率与飞牛达到 200% 时，将为飞牛展开开心之壁的绝对领域。
        </p>
        <ul className="about-list">
          <li><img className="li-icon" src={fci1} alt="" /> AT 力场防护：构建安全屏障，抵御各类威胁</li>
          <li><img className="li-icon" src={fci2} alt="" /> 实时入侵检测：智能监控异常行为，及时阻止潜在风险</li>
          <li><img className="li-icon" src={fci3} alt="" /> 数据安全加固：多重防护机制，保障您的数据安全</li>
          <li><img className="li-icon" src={fci4} alt="" /> 一键启用：简单配置，轻松开启全方位防护</li>
        </ul>
        <div className="about-meta">
          <div className="about-meta-row"><span>开发者</span><b>米恋泥</b></div>
          <div className="about-meta-row"><span>企鹅群</span><b>1039270739</b></div>
          <div className="about-meta-row">
            <span>感谢支持</span>
            <button type="button" className="pay-link" onClick={() => setShowPay(true)}>投喂入口</button>
          </div>
        </div>
      </section>
      {showPay && (
        <div className="pay-mask" onClick={() => setShowPay(false)}>
          <div
            className="pay-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="投喂入口"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="pay-dialog-head">
              <strong>投喂入口</strong>
              <button type="button" className="btn btn-mini" onClick={() => setShowPay(false)}>关闭</button>
            </div>
            <div className="pay-body">
              <p className="pay-tip">
                如果您觉得这个工具对您有帮助，欢迎通过以下方式赞赏支持开发者。<br />
                您的支持是我持续开发和维护的动力！感谢每一位用户的认可与鼓励。
              </p>
              <img src="https://fndesk.imcq.top/?url=pay" alt="投喂二维码" />
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

/* ---------- 根组件 ---------- */
const NAV = [
  ['overview', '总览', 'chart'],
  ['network', '网络', 'net'],
  ['health', '健康度', 'chart'],
  ['logs', '防护日志', 'list'],
  ['settings', '设置', 'gear'],
]

const PAGES = [...NAV.map((n) => n[0]), 'about']

export default function App() {
  const [page, setPage] = useState(() => {
    const h = location.hash.replace('#', '')
    return PAGES.includes(h) ? h : 'overview'
  })
  const [data, setData] = useState(null)
  const [history, setHistory] = useState([])
  const [range, setRange] = useState('7d')
  const [error, setError] = useState('')
  const [live, setLive] = useState(() => {
    if (location.search.includes('nosse=1')) return 'off'
    return window.EventSource ? 'connecting' : 'off'
  })
  const [settingsDirty, setSettingsDirty] = useState(false)
  // 轮询里要判断 SSE 是否在线，用 ref 避免把它写进 effect 依赖导致反复重连
  const liveRef = useRef(live)
  liveRef.current = live
  // 后台标签页收到的最后一份推送，回到前台再一次性渲染
  const pendingRef = useRef(null)

  // 只刷新趋势历史（图表数据），不重复拉总览
  const loadHistory = useCallback(async (rng) => {
    try {
      const hist = await api.history(rng || '7d')
      setHistory(hist.points)
    } catch (_) {}
  }, [])

  const refresh = useCallback(async (rng) => {
    try {
      const [ov, hist] = await Promise.all([api.overview(), api.history(rng || '7d')])
      setData(ov)
      setHistory(hist.points)
      setError('')
    } catch (e) {
      setError(e.message)
    }
  }, [])

  useEffect(() => {
    refresh(range)
    // 30 秒轮询：SSE 在线时只补趋势历史，没连上时才连总览一起拉，
    // 免得同一份数据被两路请求重复取回、互相触发渲染
    const timer = setInterval(() => {
      if (liveRef.current === 'live') loadHistory(range)
      else refresh(range)
    }, 30000)
    // SSE 实时推送（5 秒一次）：只负责总览数据，趋势与日志仍由轮询兜底。
    // 长连接会阻塞无头浏览器截图，可用 ?nosse=1 临时关闭。
    let es = null
    if (window.EventSource && !location.search.includes('nosse=1')) {
      try {
        es = new EventSource(api.streamUrl())
        es.onopen = () => setLive('live')
        es.onmessage = (ev) => {
          try {
            const next = JSON.parse(ev.data)
            setLive('live')
            // 后台标签页不做 React 渲染，只留存最后一份，回到前台再补
            if (document.hidden) {
              pendingRef.current = next
              return
            }
            setData(next)
            setError('')
          } catch (_) {}
        }
        // EventSource 自带重连，期间标记为「轮询兜底」，避免界面看起来是实时其实已断
        es.onerror = () => setLive('poll')
      } catch (_) {
        setLive('poll')
      }
    }
    return () => {
      clearInterval(timer)
      if (es) es.close()
    }
  }, [refresh, loadHistory, range])

  // 标签页切到后台时暂停持续动画（同步率浮标、未保存徽标、连接指示灯），
  // 回到前台立刻补一次最新数据，避免「看不见的时候还在烧 CPU」
  useEffect(() => {
    const onVis = () => {
      document.documentElement.classList.toggle('is-hidden', document.hidden)
      if (document.hidden) return
      if (pendingRef.current) {
        setData(pendingRef.current)
        pendingRef.current = null
        setError('')
      }
      loadHistory(range)
    }
    document.addEventListener('visibilitychange', onVis)
    document.documentElement.classList.toggle('is-hidden', document.hidden)
    return () => document.removeEventListener('visibilitychange', onVis)
  }, [loadHistory, range])

  const onRange = (rng) => setRange(rng)

  // 切换页面时，如果设置页还有未保存的修改，先确认再离开
  const goPage = (key) => {
    if (key === page) return
    if (page === 'settings' && settingsDirty) {
      if (!window.confirm('设置页还有未保存的修改，确定离开吗？')) return
      setSettingsDirty(false)
    }
    setPage(key)
  }
  // 关闭/刷新窗口前提醒（仅浏览器层面，部署为 PWA 内嵌时可能不触发）
  useEffect(() => {
    const onBeforeUnload = (e) => {
      if (!settingsDirty) return
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [settingsDirty])

  const onFeature = async (key, val) => {
    const next = { ...data.config, [key]: val }
    await api.saveConfig(next)
    refresh(range)
  }
  const onEnableAll = async () => {
    if (data.active) await api.disable()
    else await api.enable()
    refresh(range)
  }
  const onSave = async (cfg) => {
    const res = await api.saveConfig(cfg)
    refresh(range)
    return res
  }
  const onEnable = async () => { await api.enable(); refresh(range) }
  const onDisable = async () => { await api.disable(); refresh(range) }

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand"><img className="brand-icon" src={appIconHd} alt="" /> 飞牛AT力场</div>
        <div className={`live-chip live-${live}`}>
          <span className="live-dot" />
          {live === 'live'
            ? '实时推送已连接'
            : live === 'connecting'
              ? '正在连接实时推送…'
              : live === 'poll'
                ? '推送中断 · 轮询兜底中'
                : '轮询刷新模式'}
        </div>
        <nav>
          {NAV.map(([key, label, icon]) => (
            <button key={key} className={`nav-item ${page === key ? 'nav-on' : ''}`} onClick={() => goPage(key)}>
              <Icon name={icon} size={22} /> {label}
            </button>
          ))}
        </nav>
        <button className={`nav-item nav-bottom ${page === 'about' ? 'nav-on' : ''}`} onClick={() => goPage('about')}>
          <Icon name="info" size={22} /> 关于
          <span className="nav-ver">v{data?.version || 'dev'}</span>
        </button>
      </aside>
      <main className="main">
        {error && <div className="error-bar">{error}</div>}
        {page === 'overview' && <Overview data={data} history={history} range={range} onRange={onRange} onFeature={onFeature} onEnableAll={onEnableAll} />}
        {page === 'network' && <Network data={data} />}
        {page === 'logs' && <Logs />}
        {page === 'health' && <Health data={data} history={history} range={range} onRange={onRange} />}
        {page === 'settings' && <Settings data={data} onSave={onSave} onEnable={onEnable} onDisable={onDisable} onDirtyChange={setSettingsDirty} />}
        {page === 'about' && <About data={data} />}
      </main>
    </div>
  )
}
