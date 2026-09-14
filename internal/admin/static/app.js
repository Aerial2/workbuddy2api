/* WorkBuddy2API 管理台前端：无框架、无外部依赖，全部数据经网关 API 获取。 */
'use strict';

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

const store = {
  key: localStorage.getItem('wb2api_key') || '',
  models: [],
  status: null,
  cfg: null,        // /admin/api/config 的响应
  cfgObj: null,     // 正在编辑的配置对象
  cfgSub: 'form',
  chatCtrl: null,
  autoTimer: null,
  loginTimer: null,
  credits: new Map(), // 按账号缓存一分钟，避免随 5s 状态刷新重复查询
  creditsLoading: false,
  requestPage: 1,
  requestTotal: 0,
  requestLoading: false,
  requestSeq: 0,
};

/* ── 基础工具 ────────────────────────────────────────── */

function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

function toast(msg, kind) {
  const box = document.createElement('div');
  box.className = 'toast ' + (kind || '');
  box.textContent = msg;
  $('#toasts').appendChild(box);
  setTimeout(() => box.remove(), kind === 'err' ? 8000 : 4200);
}

function fmtDur(sec) {
  sec = Number(sec) || 0;
  if (sec <= 0) return '-';
  if (sec < 60) return sec + 's';
  if (sec < 3600) return Math.floor(sec / 60) + 'm' + (sec % 60 ? (sec % 60) + 's' : '');
  return Math.floor(sec / 3600) + 'h' + Math.floor((sec % 3600) / 60) + 'm';
}

function fmtTime(t) {
  if (!t) return '-';
  const d = new Date(t);
  if (isNaN(d.getTime()) || d.getFullYear() < 2000) return '-';
  const p = (n) => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function agoTime(t) {
  if (!t) return '-';
  const d = new Date(t);
  if (isNaN(d.getTime()) || d.getFullYear() < 2000) return '-';
  const s = Math.max(0, Math.floor((Date.now() - d.getTime()) / 1000));
  if (s < 60) return s + 's 前';
  if (s < 3600) return Math.floor(s / 60) + 'm 前';
  if (s < 86400) return Math.floor(s / 3600) + 'h 前';
  return Math.floor(s / 86400) + 'd 前';
}

function getPath(obj, path) {
  return path.split('.').reduce((o, k) => (o == null ? undefined : o[k]), obj);
}

function setPath(obj, path, val) {
  const parts = path.split('.');
  let cur = obj;
  for (let i = 0; i < parts.length - 1; i++) {
    if (typeof cur[parts[i]] !== 'object' || cur[parts[i]] === null) cur[parts[i]] = {};
    cur = cur[parts[i]];
  }
  cur[parts[parts.length - 1]] = val;
}

/* ── API ─────────────────────────────────────────────── */

async function api(path, opts = {}) {
  const headers = Object.assign({}, opts.headers || {});
  if (store.key) headers['Authorization'] = 'Bearer ' + store.key;
  if (opts.body !== undefined && !headers['Content-Type']) headers['Content-Type'] = 'application/json';
  const res = await fetch(path, Object.assign({}, opts, { headers }));
  const text = await res.text();
  let data = text;
  try { data = text ? JSON.parse(text) : null; } catch (e) { /* 原样保留 */ }
  if (!res.ok) {
    const msg = (data && data.error && data.error.message) || (typeof data === 'string' ? data : '') || ('HTTP ' + res.status);
    const err = new Error(msg);
    err.status = res.status;
    if (res.status === 401) markUnauthorized();
    throw err;
  }
  return data;
}

function setConn(ok, title) {
  const el = $('#conn');
  el.className = 'conn ' + (ok ? 'ok' : 'bad');
  el.textContent = ok ? '已连接' : '连接失败';
  el.title = title || (ok ? '已连接' : '连接失败');
}

/* 鉴权失败（401）：把「去哪里填 API Key」讲清楚，而不是只在控制台刷 401。 */
function markUnauthorized() {
  const bar = $('#authBar');
  if (bar) bar.hidden = false;
  const input = $('#apiKey');
  if (input) input.classList.add('invalid');
  setConn(false, '未通过鉴权（401）：请填写 config.json 的 api_key');
}

function clearUnauthorized() {
  const bar = $('#authBar');
  if (bar) bar.hidden = true;
  const input = $('#apiKey');
  if (input) input.classList.remove('invalid');
}

async function connect(silent) {
  try {
    store.status = await api('/status');
    setConn(true);
    clearUnauthorized();
    if (!silent) toast('连接成功', 'ok');
    return true;
  } catch (e) {
    setConn(false, e.message);
    if (e.status === 401) {
      markUnauthorized();
      if (!silent) toast('鉴权失败：请在右上角填写 config.json 的 api_key', 'err');
    } else if (!silent) {
      toast('连接失败：' + e.message, 'err');
    }
    return false;
  }
}

/* ── 左侧导航 ────────────────────────────────────────── */

const mobileMenu = window.matchMedia('(max-width: 900px)');

function setMenuOpen(open, restoreFocus = true) {
  open = open && mobileMenu.matches;
  document.body.classList.toggle('menu-open', open);
  $('#toggleMenu').setAttribute('aria-expanded', String(open));
  $('#menuBackdrop').hidden = !open;
  $('#workspace').inert = open;
  if (open) {
    $('#closeMenu').focus();
  } else if (restoreFocus && mobileMenu.matches) {
    $('#toggleMenu').focus();
  }
}

$('#toggleMenu').addEventListener('click', () => setMenuOpen(!document.body.classList.contains('menu-open')));
$('#closeMenu').addEventListener('click', () => setMenuOpen(false));
$('#menuBackdrop').addEventListener('click', () => setMenuOpen(false));
mobileMenu.addEventListener('change', () => {
  const sidebarHadFocus = $('#sidebar').contains(document.activeElement);
  setMenuOpen(false, false);
  if (sidebarHadFocus) {
    $(mobileMenu.matches ? '#toggleMenu' : '#tabs .tab.active').focus();
  }
});
document.addEventListener('keydown', (e) => {
  if (!document.body.classList.contains('menu-open')) return;
  if (e.key === 'Escape') {
    e.preventDefault();
    setMenuOpen(false);
  }
  if (e.key === 'Tab') {
    const controls = $$('#sidebar button');
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }
});

$$('#tabs .tab').forEach((btn) => {
  btn.setAttribute('aria-controls', 'panel-' + btn.dataset.tab);
  btn.addEventListener('click', () => {
    $$('#tabs .tab').forEach((b) => {
      b.classList.toggle('active', b === btn);
      if (b === btn) b.setAttribute('aria-current', 'page');
      else b.removeAttribute('aria-current');
    });
    $$('.panel').forEach((p) => p.classList.toggle('active', p.id === 'panel-' + btn.dataset.tab));
    $('#currentPage').textContent = btn.textContent.trim();
    const wasMenuOpen = document.body.classList.contains('menu-open');
    setMenuOpen(false, false);
    if (wasMenuOpen) $('#mainContent').focus({ preventScroll: true });
    window.scrollTo({ top: 0, behavior: 'instant' });
    const t = btn.dataset.tab;
    if (t === 'overview') loadOverview();
    if (t === 'accounts') loadAccounts();
    if (t === 'requests') loadRequestHistory(1, true);
    if (t === 'logs') loadRealtimeLogs('latest', true);
    if (t === 'usage') loadUsageStats();
    if (t === 'health') loadDiagnostics();
    if (t === 'performance') { loadPerformance(); void loadPricingIntoForm(); }
    if (t === 'models') loadModels();
    if (t === 'chat') loadModels(true);
    if (t === 'config') loadConfig();
  });
});

/* ── 网关实时日志 ─────────────────────────────────────── */

const liveLogs = { busy: false, seq: 0, cursors: [0], next: 0, controller: null };

function updateLogControls() {
  $$('#panel-logs select, #logModel, #refreshLogs').forEach((el) => { el.disabled = liveLogs.busy; });
  $('#logsPrev').disabled = liveLogs.busy || liveLogs.cursors.length <= 1;
  $('#logsNext').disabled = liveLogs.busy || !liveLogs.next;
  $('#logsPage').textContent = `第 ${liveLogs.cursors.length} 页${liveLogs.cursors.length > 1 ? ' · 自动刷新暂停' : ''}`;
}

function resetRealtimeLogs() {
  liveLogs.seq++;
  if (liveLogs.controller) liveLogs.controller.abort();
  liveLogs.busy = false;
  liveLogs.cursors = [0];
  liveLogs.next = 0;
  $('#logAccount').innerHTML = '<option value="">全部账号</option>';
  $('#logsTable tbody').innerHTML = '';
  $('#logsHint').textContent = '';
  updateLogControls();
}

function renderRealtimeLogs(rows) {
  const accounts = (store.status && store.status.accounts) || [];
  const accountLabel = (uid) => {
    const a = accounts.find((item) => item.uid === uid);
    return a && a.nickname ? `${a.nickname} · ${uid.slice(0, 8)}` : uid || '-';
  };
  $('#logsTable tbody').innerHTML = rows.map((row) => {
    const ok = row.result === 'success';
    const details = [
      `开始：${new Date(row.started_at).toLocaleString('zh-CN')}`,
      `结束：${new Date(row.finished_at).toLocaleString('zh-CN')}`,
      `最后账号 UID：${row.uid || '-'}`,
      `账号尝试：${row.attempts} 次 · ${(row.accounts || []).join(' → ') || '-'}`,
      `首个数据帧：${row.first_token_ms > 0 ? row.first_token_ms + ' ms' : '-'}`,
      `输出 tokens（上游报告）：${row.completion_tokens >= 0 ? row.completion_tokens : '-'}`,
      `最终错误：${row.error_message || '-'}${row.error_code ? ' (' + row.error_code + ')' : ''}`,
      `最近一次尝试失败：${row.last_failure || '-'}`,
    ].join('\n');
    return `<tr>
      <td class="mono" title="${esc(row.started_at)}">${esc(fmtTime(row.started_at))}<div class="hint">#${esc(row.id)}</div></td>
      <td class="log-text mono">${esc(row.model || '-')}</td>
      <td class="log-text" title="${esc(row.uid)}">${esc(accountLabel(row.uid))}</td>
      <td>${row.mode === 'stream' ? '流式' : row.mode === 'sync' ? '非流式' : '-'}</td>
      <td class="mono">${esc((Number(row.duration_ms) / 1000).toFixed(2))}s</td>
      <td><span class="badge ${ok ? 'ok' : 'err'}">${ok ? '成功' : '失败'}</span> <span class="mono">${esc(row.status || '-')}</span></td>
      <td class="mono">${esc(row.retries)}</td>
      <td class="log-detail"><details><summary>${esc(row.error_message || (row.retries ? '重试后成功' : '查看详情'))}</summary><pre>${esc(details)}</pre></details></td>
    </tr>`;
  }).join('') || '<tr><td colspan="8" class="hint">暂无符合条件的已结束请求。重启前的日志和其他客户端直连官网的请求不会显示。</td></tr>';
}

async function loadRealtimeLogs(action = 'latest', reloadAccounts = false) {
  if (liveLogs.busy) return;
  const seq = ++liveLogs.seq;
  const key = store.key;
  const controller = new AbortController();
  liveLogs.controller = controller;
  const timeout = setTimeout(() => controller.abort(), 15000);
  let cursors = liveLogs.cursors.slice();
  if (action === 'latest') cursors = [0];
  if (action === 'next' && liveLogs.next) cursors.push(liveLogs.next);
  if (action === 'prev' && cursors.length > 1) cursors.pop();
  liveLogs.busy = true;
  updateLogControls();
  $('#logsHint').textContent = '正在读取网关日志…';
  try {
    if (reloadAccounts) {
      const st = await api('/status', { signal: controller.signal });
      if (seq !== liveLogs.seq || key !== store.key) return;
      store.status = st;
      const select = $('#logAccount');
      const current = select.value;
      const accounts = st.accounts || [];
      select.innerHTML = '<option value="">全部账号</option>' + accounts.map((a) => `<option value="${esc(a.uid)}">${esc(a.nickname || a.uid)} · ${esc(a.uid.slice(0, 8))}</option>`).join('');
      if (current && !accounts.some((a) => a.uid === current)) {
        const option = document.createElement('option');
        option.value = current;
        option.textContent = current + '（已移除账号）';
        select.appendChild(option);
      }
      select.value = current;
    }
    const params = new URLSearchParams({
      model: $('#logModel').value.trim(), uid: $('#logAccount').value,
      result: $('#logResult').value, limit: $('#logLimit').value,
      before: String(cursors[cursors.length - 1]),
    });
    const data = await api('/admin/api/request-logs?' + params, { signal: controller.signal });
    if (seq !== liveLogs.seq || key !== store.key) return;
    if (!data || !Array.isArray(data.data) || !Number.isInteger(data.retained)) throw new Error('实时日志响应格式无效');
    liveLogs.cursors = cursors;
    liveLogs.next = data.has_more ? data.next_before : 0;
    renderRealtimeLogs(data.data);
    $('#logsHint').textContent = `内存保留 ${data.retained} / ${data.capacity} 条 · 当前筛选匹配 ${data.total} 条 · 累计淘汰 ${data.dropped} 条 · 更新于 ${fmtTime(new Date())}。历史记录可能随新请求到来被淘汰。`;
  } catch (e) {
    if (seq !== liveLogs.seq || key !== store.key) return;
    liveLogs.next = 0;
    $('#logsHint').textContent = '日志查询失败：' + (e.name === 'AbortError' ? '请求超时，请刷新重试' : e.message);
    $('#logsTable tbody').innerHTML = '<tr><td colspan="8" class="hint">查询未完成，请检查上方提示并刷新重试。</td></tr>';
  } finally {
    clearTimeout(timeout);
    if (seq === liveLogs.seq) {
      liveLogs.busy = false;
      liveLogs.controller = null;
      updateLogControls();
    }
  }
}

$('#refreshLogs').addEventListener('click', () => loadRealtimeLogs('latest', true));
$('#logsPrev').addEventListener('click', () => loadRealtimeLogs('prev'));
$('#logsNext').addEventListener('click', () => loadRealtimeLogs('next'));
$$('#logAccount, #logModel, #logResult, #logLimit').forEach((el) => {
  el.addEventListener('change', () => loadRealtimeLogs('latest'));
});
setInterval(() => {
  if (!document.hidden && $('#panel-logs').classList.contains('active') && $('#logsAuto').checked && liveLogs.cursors.length === 1) {
    // Preserve expanded details while the user is inspecting an individual request.
    if (!$('#logsTable details[open]')) loadRealtimeLogs('latest');
  }
}, 5000);

/* ── 统计、余额提醒与诊断 ─────────────────────────────── */

const dashboards = {
  usage: { seq: 0, busy: false, hint: '#usageStatsHint', clear: '#usageStatsCards, #usageDaily, #usageRanking, #usageAccountsTable tbody, #usageModelsTable tbody' },
  health: { seq: 0, busy: false, hint: '#diagnosticsHint', clear: '#diagnosticsTable tbody' },
  performance: { seq: 0, busy: false, hint: '#performanceHint', clear: '#performanceCards, #performanceModels tbody, #slowRequests tbody' },
};
let diagnosticData = null;
const savedThreshold = localStorage.getItem('wb2api_low_balance');
const balanceSettings = {
  enabled: localStorage.getItem('wb2api_balance_enabled') !== 'false',
  threshold: savedThreshold !== null && Number.isFinite(Number(savedThreshold)) && Number(savedThreshold) >= 0 && Number(savedThreshold) <= 1e9 ? Number(savedThreshold) : 50,
};
$('#balanceEnabled').checked = balanceSettings.enabled;
$('#balanceThreshold').value = balanceSettings.threshold;
const fmtNumber = (n) => n == null ? '—' : Number(n).toLocaleString('zh-CN', { maximumFractionDigits: 2 });
const fmtPercent = (n) => n == null ? '—' : fmtNumber(n) + '%';
const fmtMS = (n) => n == null ? '—' : (Number(n) / 1000).toFixed(2) + 's';

function balanceState(uid) {
  const entry = store.credits.get(uid);
  if (!entry || entry.key !== store.key || Date.now() - entry.at >= 65000) return { known: false, text: '余额未知（未查询或已过期）' };
  if (entry.error) return { known: false, text: '余额查询失败：' + entry.error };
  return { known: true, remain: entry.data.remain, low: entry.data.remain < balanceSettings.threshold, text: '剩余 ' + fmtNumber(entry.data.remain) + ' 积分' };
}
function renderBalanceAlerts() {
  const accounts = (store.status && store.status.accounts) || [];
  let low = 0, unknown = 0, available = 0, availableLow = 0, availableUnknown = 0, availableCredits = 0;
  for (const a of accounts) {
    const b = balanceState(a.uid);
    if (!b.known) unknown++; else if (b.low) low++;
    if (!a.disabled && !a.cooling) {
      available++;
      if (!b.known) availableUnknown++; else { availableCredits += b.remain; if (b.low) availableLow++; }
    }
  }
  $('#balanceSummary').innerHTML = [
    card('低余额账号', low, '剩余积分低于 ' + balanceSettings.threshold, low && balanceSettings.enabled ? 'warn' : ''),
    card('余额未知', unknown, '查询失败 / 过期 / 尚未查询', unknown ? 'warn' : ''),
    card('非冷却、非禁用账号积分合计', availableUnknown ? '不完整' : fmtNumber(availableCredits), `共 ${available} 个账号 · 未知 ${availableUnknown} 个`),
  ].join('');
  const notice = $('#balanceNotice');
  notice.className = 'dashboard-notice';
  if (!balanceSettings.enabled) notice.textContent = '站内低余额提醒已关闭。';
  else if (!accounts.length) notice.textContent = '尚无账号数据，请连接服务后刷新。';
  else if (!available) { notice.classList.add('warn'); notice.textContent = '当前没有非冷却、非禁用账号，请查看健康诊断。'; }
  else if (!availableUnknown && availableLow === available) { notice.classList.add('danger'); notice.textContent = '可用账号余额全部偏低，请及时补充额度！阈值：' + balanceSettings.threshold + ' 积分。'; }
  else if (unknown) { notice.classList.add('warn'); notice.textContent = `已确认 ${low} 个低余额账号，另有 ${unknown} 个账号余额未知，不能据此判断全部余额正常。`; }
  else if (low) { notice.classList.add('warn'); notice.textContent = `${low} 个账号余额偏低，已在账号页标色。`; }
  else notice.textContent = '已查询的账号余额均未低于提醒阈值。';
  $$('#accountsTable tr[data-credit-uid]').forEach((row) => {
    const b = balanceState(row.dataset.creditUid);
    row.classList.toggle('low-balance', balanceSettings.enabled && b.known && b.low);
  });
  $$('[data-diagnostic-balance]').forEach((el) => {
    const b = balanceState(el.dataset.diagnosticBalance);
    el.textContent = b.known && b.remain <= 0 ? '官网积分已耗尽，请补充额度或等待签到恢复；查询余额不会自动解除账号处罚。' : b.text;
    el.className = 'hint' + (balanceSettings.enabled && b.known && b.low ? ' balance-low' : '');
  });
}
$('#saveBalanceSettings').addEventListener('click', () => {
  const input = $('#balanceThreshold');
  if (!input.value.trim() || !input.checkValidity()) { toast('阈值必须是 0 至 1000000000 之间的数字，最多两位小数', 'err'); return; }
  balanceSettings.threshold = Number(input.value);
  balanceSettings.enabled = $('#balanceEnabled').checked;
  localStorage.setItem('wb2api_low_balance', String(balanceSettings.threshold));
  localStorage.setItem('wb2api_balance_enabled', String(balanceSettings.enabled));
  renderBalanceAlerts(); renderCreditCells(); toast('提醒设置已保存到当前浏览器', 'ok');
});
$('#refreshBalances').addEventListener('click', async () => {
  const button = $('#refreshBalances'); button.disabled = true;
  try { const key = store.key; const st = await api('/status'); if (key !== store.key) return; store.status = st; await loadAccountCredits(st.accounts || [], true); renderBalanceAlerts(); }
  catch (e) { toast('刷新余额失败：' + e.message, 'err'); }
  finally { button.disabled = false; }
});

function dashboardControls(name) {
  $$(`#panel-${name} button, #panel-${name} select`).forEach((el) => { el.disabled = dashboards[name].busy; });
}
function resetDashboards() {
  diagnosticData = null;
  for (const [name, s] of Object.entries(dashboards)) {
    s.seq++; if (s.controller) s.controller.abort(); s.busy = false;
    $$(s.clear).forEach((el) => { el.innerHTML = ''; });
    $(s.hint).textContent = ''; dashboardControls(name);
  }
  $('#usageAccount').innerHTML = '<option value="">全部账号</option>';
  store.status = null;
  store.credits.clear(); renderBalanceAlerts();
}
async function dashboardLoad(name, run, render, timeout = 20000) {
  const s = dashboards[name]; if (s.busy) return;
  const seq = ++s.seq; const key = store.key;
  const current = () => seq === s.seq && key === store.key;
  const controller = new AbortController(); s.controller = controller;
  const timer = setTimeout(() => controller.abort(), timeout);
  s.busy = true; dashboardControls(name);
  $$(s.clear).forEach((el) => { el.innerHTML = ''; });
  $(s.hint).className = 'dashboard-notice';
  $(s.hint).textContent = name === 'usage' ? '正在逐页读取官网账单，最多等待 90 秒…' : '正在读取统计数据…';
  try { const data = await run(controller.signal, current); if (current()) render(data); }
  catch (e) { if (current()) { $(s.hint).className = 'dashboard-notice danger'; $(s.hint).textContent = '查询失败：' + (e.name === 'AbortError' ? '请求超时，请刷新重试' : e.message); } }
  finally { clearTimeout(timer); if (current()) { s.busy = false; s.controller = null; dashboardControls(name); } }
}
function usageCredit(g, partial = false) {
  return (!g.known && (g.requests > 0 || partial)) ? '—' : fmtNumber(g.credits);
}
function usageBars(rows) {
  if (!rows.length) return '<p class="hint">暂无记录。</p>';
  const max = Math.max(1, ...rows.map((r) => Number(r.credits)));
  return rows.map((r) => `<div class="usage-bar"><span class="mono">${esc(r.name)}</span><meter min="0" max="${max}" value="${Number(r.credits)}" aria-label="${esc(r.name)} 积分消耗"></meter><span>${esc(usageCredit(r))} · ${esc(r.requests)} 次</span></div>`).join('');
}
function loadUsageStats(force = false) {
  return dashboardLoad('usage', async (signal, current) => {
    const st = await api('/status', { signal }); if (!current()) return;
    const select = $('#usageAccount'); const selected = select.value;
    select.innerHTML = '<option value="">全部账号</option>' + (st.accounts || []).map((a) => `<option value="${esc(a.uid)}">${esc(a.nickname || a.uid)}</option>`).join('');
    if ((st.accounts || []).some((a) => a.uid === selected)) select.value = selected;
    const params = new URLSearchParams({ period: $('#usagePeriod').value, uid: select.value, refresh: force ? '1' : '0' });
    return api('/admin/api/usage-stats?' + params, { signal });
  }, (data) => {
    if (!data || !Array.isArray(data.accounts) || !Array.isArray(data.models) || !Array.isArray(data.daily)) throw new Error('用量统计响应格式无效');
    $('#usageStatsHint').className = 'dashboard-notice ' + (data.complete ? '' : 'danger');
    $('#usageStatsHint').textContent = `${data.complete ? '分页读取完成' : '统计不完整：以下仅为已读取部分，不能当作全部用量'} · ${data.start} 至 ${data.end}（北京时间） · 查询于 ${fmtTime(data.fetched_at)}`;
    $('#usageStatsCards').innerHTML = [
      card('积分消耗' + (data.complete ? '' : '（部分）'), usageCredit(data, !data.complete), '官网账单实际消耗'),
      card('已读取请求次数', data.requests, `有效积分 ${data.known} 条 · 缺失 ${data.requests - data.known} 条`),
      card('单次平均积分', fmtNumber(data.average), '仅以有效积分记录为分母'),
      card('完整账号', data.accounts.filter((a) => a.complete).length + ' / ' + data.accounts.length, '展开下方完整性说明', data.complete ? 'ok' : 'warn'),
    ].join('');
    $('#usageDaily').innerHTML = usageBars(data.daily);
    $('#usageRanking').innerHTML = usageBars(data.models.slice(0, 10));
    $('#usageAccountsTable tbody').innerHTML = data.accounts.map((a) => `<tr><td class="wrap-text" title="${esc(a.key)}">${esc(a.name || a.key)}</td><td>${esc(a.requests)} / ${a.expected >= 0 ? esc(a.expected) : '未知'}</td><td>${esc(usageCredit(a, !a.complete))}</td><td>${esc(fmtNumber(a.average))}</td><td class="wrap-text"><span class="badge ${a.complete ? 'ok' : 'err'}">${a.complete ? '完整' : '不完整'}</span> · ${esc(a.pages)} 页${(a.issues || []).map((v) => `<div class="hint">${esc(v)}</div>`).join('')}</td></tr>`).join('') || '<tr><td colspan="5">暂无账号。</td></tr>';
    $('#usageModelsTable tbody').innerHTML = data.models.map((m) => `<tr><td class="wrap-text mono">${esc(m.name)}</td><td>${esc(m.requests)}</td><td>${esc(m.known)}</td><td>${esc(usageCredit(m))}</td><td>${esc(fmtNumber(m.average))}</td></tr>`).join('') || '<tr><td colspan="5">暂无已读取记录。</td></tr>';
  }, 100000);
}
function loadPerformance() {
  return dashboardLoad('performance', (signal) => api('/admin/api/performance', { signal }), (data) => {
    const p = data.stats; if (!p || !Array.isArray(p.models) || !Array.isArray(p.slow)) throw new Error('性能统计响应格式无效');
    $('#performanceHint').textContent = `仅覆盖本次启动后保留的日志：${p.retained} / ${p.capacity} 条，已淘汰 ${p.dropped} 条。启动：${fmtTime(data.started_at)}；样本：${fmtTime(p.oldest)} 至 ${fmtTime(p.latest)}。${p.attempts_complete ? '' : '部分日志缺少账号尝试明细，超时、限流计数不完整。'}`;
    $('#performanceCards').innerHTML = [card('请求成功率', fmtPercent(p.success_rate), `${p.success} / ${p.requests} 次`, 'ok'), card('平均耗时', fmtMS(p.average_ms)), card('P95 耗时', fmtMS(p.p95_ms)), card('首帧平均耗时', fmtMS(p.first_frame_ms), `${p.first_frame_samples} 个流式样本`), card('请求重试比例', fmtPercent(p.retry_rate), `${p.retried} 个请求 · 重试 ${p.retries} 次`), card('超时 / 限流', `${p.timeouts} / ${p.rate_limits}`, '按账号尝试计数')].join('');
    const tk = p.tokens || {};
    renderTokenCards(tk);
    $('#performanceModels tbody').innerHTML = p.models.map((m) => `<tr><td class="wrap-text mono">${esc(m.model)}</td><td>${esc(m.requests)}</td><td>${esc(fmtPercent(m.success_rate))}</td><td>${esc(fmtMS(m.average_ms))}</td><td>${esc(fmtMS(m.p95_ms))}</td><td>${esc(fmtMS(m.first_frame_ms))} / ${esc(m.first_frame_samples)}</td><td>${esc(fmtPercent(m.retry_rate))}</td><td>${esc(m.timeouts)} / ${esc(m.rate_limits)}</td></tr>`).join('') || '<tr><td colspan="8">暂无请求样本。</td></tr>';
    $('#slowRequests tbody').innerHTML = p.slow.map((r) => `<tr><td>#${esc(r.id)} · ${esc(fmtTime(r.started_at))}</td><td class="wrap-text mono">${esc(r.model || '-')}</td><td class="wrap-text mono">${esc(r.uid || '-')}</td><td>${esc(fmtMS(r.duration_ms))}</td><td>${r.result === 'success' ? '成功' : '失败'}</td><td>${esc(r.retries)}</td></tr>`).join('') || '<tr><td colspan="6">暂无请求样本。</td></tr>';
  });
}

function fmtCompact(n) {
  if (!Number.isFinite(n)) return '-';
  if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(Math.round(n));
}
function renderTokenCards(tk) {
  const hasSamples = (tk.with_usage || 0) > 0;
  const hitRate = tk.cache_hit_rate == null ? '—' : Number(tk.cache_hit_rate).toFixed(2) + '%';
  const cacheSub = hasSamples
    ? `缓存读 ${fmtCompact(tk.cached_prompt || 0)} / 计入 ${fmtCompact(tk.cacheable_prompt || 0)}`
    : '暂无已上报 usage 的请求';
  const rounds = hasSamples ? fmtCompact(tk.with_usage) : '—';
  const roundsSub = `${tk.with_usage || 0} 次成功请求 · 未知 Token ${tk.abnormal || 0} 次`;
  const promptMain = hasSamples ? fmtCompact(tk.prompt || 0) : '—';
  const promptSub = `补全 ${fmtCompact((tk.prompt || 0) - (tk.cached_prompt || 0))} · 思考 ${fmtCompact(tk.reasoning || 0)} · 合计 ${fmtCompact(tk.total_tokens || 0)}`;
  let valueMain = '—', valueSub = '尚未设置单价';
  const est = tk.estimate;
  if (est && est.amount != null) {
    valueMain = '$' + Number(est.amount).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    const pr = est.pricing || {};
    valueSub = `输入 ${pr.input_per_m ?? '-'} · 缓存 ${pr.cached_input_per_m ?? '-'} · 输出 ${pr.output_per_m ?? '-'} $/M`;
  } else if (est && est.unknown_turns > 0) {
    valueSub = `${est.unknown_turns} 次请求无 usage，未计入`;
  }
  $('#tokenCards').innerHTML = [
    card('缓存命中率', hitRate, cacheSub, hasSamples && (tk.cache_hit_rate || 0) >= 50 ? 'ok' : ''),
    card('对话轮次', rounds, roundsSub, ''),
    card('Token 消耗', promptMain, promptSub, hasSamples ? 'info' : ''),
    card('价值估算（估）', valueMain, valueSub, ''),
  ].join('');
  if (est && est.amount != null) {
    $('#pricingHint').className = 'dashboard-notice';
    $('#pricingHint').textContent = `按当前单价估算为 $${Number(est.amount).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}，仅覆盖本次启动保留的日志，不代表官网积分账单（真实消耗见「请求记录」）。`;
  } else {
    $('#pricingHint').className = 'dashboard-notice warn';
    $('#pricingHint').textContent = '尚未设置任何单价，价值估算暂不可用；填写并保存后立即生效。';
  }
}
function loadDiagnostics(force = false) {
  return dashboardLoad('health', (signal) => api('/admin/api/diagnostics', { signal }), (data) => {
    if (!data || !Array.isArray(data.accounts)) throw new Error('健康诊断响应格式无效');
    diagnosticData = data;
    store.status = Object.assign({}, store.status || {}, { accounts: data.accounts.map((a) => a.status) });
    $('#diagnosticsHint').textContent = `状态查询于 ${fmtTime(data.generated_at)} · 近期指标来自保留的 ${data.retained} 条请求，已淘汰 ${data.dropped} 条。${data.attempts_complete ? '' : '部分账号尝试明细缺失，近期指标不完整。'}`;
    $('#diagnosticsTable tbody').innerHTML = data.accounts.map((a) => {
      const st = a.status; const recent = a.recent;
      return `<tr><td class="wrap-text" title="${esc(st.uid)}">${esc(st.nickname || st.uid)}<div class="hint" data-diagnostic-balance="${esc(st.uid)}"></div></td><td class="wrap-text">${a.issues.map((i) => `<div><span class="badge ${i.code === 'ready' ? 'ok' : 'warn'}">${esc(i.message)}</span><p class="hint">${esc(i.advice)}</p></div>`).join('')}</td><td>${a.recovery_at ? esc(fmtTime(a.recovery_at)) : st.disabled ? '需人工处理' : '—'}</td><td title="账号池最近成功时间（上游响应头成功口径）">${esc(fmtTime(st.last_success))}<div class="hint">完整请求：${esc(fmtTime(recent.last_success))}</div></td><td>${esc(fmtPercent(recent.success_rate))}<div class="hint">${esc(recent.success)} / ${esc(recent.attempts)} 次</div></td><td>${esc(recent.retried_requests)}</td><td class="wrap-text">${esc(fmtTime(recent.last_failure))}<div class="hint">${esc(recent.last_error || '-')}</div></td></tr>`;
    }).join('') || '<tr><td colspan="7">暂无账号。</td></tr>';
    renderBalanceAlerts(); void loadAccountCredits(store.status.accounts, force);
  });
}
$('#refreshUsageStats').addEventListener('click', () => loadUsageStats(true));
$$('#usagePeriod, #usageAccount').forEach((el) => el.addEventListener('change', () => loadUsageStats()));
$('#refreshPerformance').addEventListener('click', loadPerformance);
async function loadPricingIntoForm() {
  try {
    const p = await api('/admin/api/pricing');
    $('#priceInput').value = p.input_per_m ?? '';
    $('#priceCached').value = p.cached_input_per_m ?? '';
    $('#priceOutput').value = p.output_per_m ?? '';
  } catch (e) { /* 未连接时留空，保存时会提示 */ }
}
$('#savePricing').addEventListener('click', async () => {
  const parse = (id) => {
    const raw = $(id).value.trim();
    if (raw === '') return null;
    const v = Number(raw);
    if (!Number.isFinite(v) || v < 0 || v > 100000) throw new Error('单价必须是 0 至 100000 的数字，或留空');
    return v;
  };
  let body;
  try {
    body = { input_per_m: parse('#priceInput'), cached_input_per_m: parse('#priceCached'), output_per_m: parse('#priceOutput') };
  } catch (e) { toast(e.message, 'err'); return; }
  try {
    await api('/admin/api/pricing', { method: 'PUT', body: JSON.stringify(body) });
    toast('单价已保存', 'ok');
    loadPerformance();
  } catch (e) { toast('保存失败：' + e.message, 'err'); }
});
$$('#priceInput, #priceCached, #priceOutput').forEach((el) => el.addEventListener('keydown', (e) => { if (e.key === 'Enter') $('#savePricing').click(); }));
$('#refreshDiagnostics').addEventListener('click', () => loadDiagnostics(true));
$('#healthChatTest').addEventListener('click', () => $('#tabs [data-tab="chat"]').click());
setInterval(() => {
  if (!document.hidden && $('#panel-performance').classList.contains('active') && $('#performanceAuto').checked) loadPerformance();
}, 5000);
setInterval(() => {
  if (document.hidden) return;
  const active = $('.panel.active')?.id;
  if (active === 'panel-overview') loadOverview();
  else if (active === 'panel-accounts') loadAccounts(true);
  else if (active === 'panel-health') loadDiagnostics();
}, 60000);

/* ── 概览 ────────────────────────────────────────────── */

function card(label, value, sub, cls) {
  return `<div class="card ${cls || ''}">
      <div class="label">${esc(label)}</div>
      <div class="value">${esc(value)}</div>
      ${sub ? `<div class="sub">${esc(sub)}</div>` : ''}
    </div>`;
}

async function loadOverview() {
  const keyAtStart = store.key;
  const cards = $('#overviewCards');
  try {
    const [hz, st, cfg] = await Promise.all([
      fetch('/healthz').then((r) => r.json()).catch(() => null),
      api('/status'),
      api('/admin/api/config').catch(() => null),
    ]);
    if (keyAtStart !== store.key) return;
    store.status = st;
    renderBalanceAlerts();
    void loadAccountCredits(st.accounts || [], false);
    setConn(true);
    const healthy = hz ? hz.healthy : 0;
    const total = hz ? hz.total : 0;
    cards.innerHTML = [
      card('服务状态', healthy > 0 ? '可服务' : '不可用', hz ? `${hz.service} · healthy ${hz.healthy}/${hz.total}` : '探活失败', healthy > 0 ? 'ok' : 'bad'),
      card('账号总数', st.total, 'auths/ 目录', 'info'),
      card('健康账号', st.healthy, '未冷却未禁用', st.healthy > 0 ? 'ok' : 'bad'),
      card('冷却中', st.cooling, '限流 / 熔断', st.cooling > 0 ? 'warn' : ''),
      card('已禁用', st.disabled, 'session 失效等', st.disabled > 0 ? 'warn' : ''),
      card('满载账号', st.in_flight_full, '在途已占满', ''),
      card('粘性会话', st.sticky_sessions, '会话绑定数', ''),
      card('Redis', st.redis_mode === 'upstash' ? 'Upstash' : '内存', st.redis_mode === 'noop' ? 'Noop 降级' : '状态镜像开启', ''),
      card('运行时长', cfg ? fmtDur(cfg.uptime_sec) : '-', cfg ? '启动于 ' + fmtTime(cfg.started_at) : '', ''),
    ].join('');

    const eff = (cfg && cfg.effective) || {};
    const rows = [
      ['配置文件', eff.config_path],
      ['监听地址', eff.listen],
      ['鉴权', eff.api_key_set ? '已启用 api_key' : '未启用（不鉴权）'],
      ['提示词模式', eff.prompt_mode + (eff.prompt_file ? ' · ' + eff.prompt_file : '')],
      ['上游超时', `短 RPC ${eff.timeout_seconds}s · 首字节 ${eff.header_timeout_seconds}s · 空闲 ${eff.idle_timeout_seconds}s`],
      ['软冷却', `${eff.soft_rate} → 封顶 ${eff.soft_rate_max}`],
      ['账号池', `在途上限 ${eff.max_in_flight} · 熔断阈值 ${eff.breaker_threshold}`],
      ['会话粘性', eff.session_sticky ? '开启' : '关闭'],
      ['定时任务', (eff.schedule ? ['签到', '猫猫旅行', '活跃上报', 'token 保活'].filter((_, i) => [
        eff.schedule.checkin_enabled, eff.schedule.travel_enabled,
        eff.schedule.activity_enabled, eff.schedule.keepalive_enabled][i]).join(' / ') || '全部关闭' : '-')],
    ];
    $('#effectiveTable tbody').innerHTML = rows
      .map(([k, v]) => `<tr><td>${esc(k)}</td><td>${esc(v == null ? '-' : v)}</td></tr>`).join('');

    const key = store.key || 'your-api-key';
    $('#snippets').innerHTML = [
      `curl -s http://localhost:7863/status -H "Authorization: Bearer ${key}"`,
      `curl -s http://localhost:7863/v1/models -H "Authorization: Bearer ${key}"`,
      `curl -sN http://localhost:7863/v1/chat/completions -H "Authorization: Bearer ${key}" -H "Content-Type: application/json" -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true}'`,
    ].map((s) => `<div>${esc(s)}</div>`).join('');
  } catch (e) {
    setConn(false, e.message);
    cards.innerHTML = card('连接失败', '—', e.message, 'bad');
  }
}

function startAuto() {
  stopAuto();
  store.autoTimer = setInterval(() => {
    const active = $('.panel.active');
    if (!active) return;
    if (active.id === 'panel-overview') loadOverview();
    else if (active.id === 'panel-accounts') loadAccounts(true);
  }, 5000);
}
function stopAuto() {
  if (store.autoTimer) clearInterval(store.autoTimer);
  store.autoTimer = null;
}

/* ── 账号 ────────────────────────────────────────────── */

function creditCells(uid) {
  const entry = store.credits.get(uid);
  if (!entry || entry.key !== store.key) {
    return '<td colspan="3" class="hint">积分加载中…</td>';
  }
  if (entry.error) {
    return `<td colspan="3" class="hint" title="${esc(entry.error)}">积分查询失败（悬停查看原因）</td>`;
  }
  return ['total', 'used', 'remain'].map((field) => {
    const value = entry.data[field];
    return `<td class="mono" title="${esc(value)} credits · ${esc(fmtTime(entry.at))}">${esc(value.toLocaleString('zh-CN', { maximumFractionDigits: 2 }))}</td>`;
  }).join('');
}

function renderCreditCells() {
  $$('#accountsTable tr[data-credit-uid]').forEach((row) => {
    const start = row.querySelector('[data-credit-start]');
    const end = row.querySelector('[data-credit-end]');
    while (start.nextElementSibling && start.nextElementSibling !== end) {
      start.nextElementSibling.remove();
    }
    start.insertAdjacentHTML('afterend', creditCells(row.dataset.creditUid));
  });
  renderBalanceAlerts();
}

async function loadAccountCredits(accounts, force) {
  if (store.creditsLoading) return;
  const key = store.key;
  const queue = accounts.filter((a) => {
    const entry = store.credits.get(a.uid);
    return force || !entry || entry.key !== key || Date.now() - entry.at >= 60000;
  });
  store.creditsLoading = true;
  try {
    // 限制并发数，积分查询不阻塞账号状态展示。
    await Promise.all(Array.from({ length: Math.min(4, queue.length) }, async () => {
      while (queue.length && store.key === key) {
        const a = queue.shift();
        let entry;
        try {
          const data = await api(`/admin/api/accounts/${encodeURIComponent(a.uid)}/credits`);
          if (!data || !['total', 'used', 'remain'].every((f) => typeof data[f] === 'number' && Number.isFinite(data[f]))) {
            throw new Error('积分响应格式无效');
          }
          entry = { data, key, at: Date.now() };
        } catch (e) {
          entry = { error: e.message, key, at: Date.now() };
        }
        if (store.key !== key) return;
        store.credits.set(a.uid, entry);
        renderCreditCells();
      }
    }));
  } finally {
    store.creditsLoading = false;
  }
}

async function loadAccounts(silent) {
  const keyAtStart = store.key;
  try {
    const st = await api('/status');
    if (keyAtStart !== store.key) return;
    store.status = st;
    setConn(true);
    $('#accountsHint').textContent = `共 ${st.total} 个 · 健康 ${st.healthy} · 冷却 ${st.cooling} · 禁用 ${st.disabled}`;
    const tbody = $('#accountsTable tbody');
    if (!st.accounts || !st.accounts.length) {
      tbody.innerHTML = `<tr><td colspan="13" style="text-align:center;color:#8b98a9;padding:26px">
        暂无账号。点击右上角「+ 添加账号」完成 OAuth 登录，或执行 ./login.sh 后重启服务。</td></tr>`;
      return;
    }
    tbody.innerHTML = st.accounts.map((a) => {
      let badge = '<span class="badge ok">健康</span>';
      let detail = '';
      if (a.disabled) {
        badge = '<span class="badge err">已禁用</span>';
        detail = esc(a.disabled_reason || '');
      } else if (a.cooling) {
        badge = '<span class="badge warn">冷却</span>';
        detail = esc(a.cool_kind ? a.cool_kind + ' · ' + (a.reason || '') : (a.reason || ''));
      }
      const breaker = a.breaker_until && new Date(a.breaker_until) > new Date()
        ? `<span class="badge warn">${esc(fmtDur(Math.floor((new Date(a.breaker_until) - Date.now()) / 1000)))}</span>`
        : '<span class="badge mute">—</span>';
      return `<tr data-credit-uid="${esc(a.uid)}">
        <td class="mono" title="${esc(a.uid)}">${esc(a.uid.length > 12 ? a.uid.slice(0, 12) + '…' : a.uid)}</td>
        <td data-credit-start>${esc(a.nickname || '-')}</td>
        ${creditCells(a.uid)}
        <td data-credit-end>${badge}${detail ? `<div class="hint" style="margin:2px 0 0">${detail}</div>` : ''}</td>
        <td class="mono">${a.cooling ? fmtDur(a.cool_remaining_sec) : '-'}</td>
        <td class="mono">${a.soft_streak || 0}</td>
        <td>${breaker}</td>
        <td class="mono">${a.in_flight || 0}</td>
        <td class="mono">${a.success_count || 0} / ${a.err_total || 0}</td>
        <td class="mono" title="${esc(fmtTime(a.last_success))}">${esc(agoTime(a.last_success))}</td>
        <td>
          ${a.disabled ? `<button class="btn mini" data-act="revive" data-uid="${esc(a.uid)}">复活</button>` : ''}
          ${(a.cooling || a.breaker_fails) ? `<button class="btn mini" data-act="reset" data-uid="${esc(a.uid)}">清处罚</button>` : ''}
        </td>
      </tr>`;
    }).join('');
    renderBalanceAlerts();
    void loadAccountCredits(st.accounts, !silent);
    if (!silent) toast('账号状态已刷新，积分正在更新', 'ok');
  } catch (e) {
    setConn(false, e.message);
    if (!silent) toast('加载账号失败：' + e.message, 'err');
  }
}

$('#accountsTable').addEventListener('click', async (ev) => {
  const btn = ev.target.closest('button[data-act]');
  if (!btn) return;
  const uid = btn.dataset.uid;
  const act = btn.dataset.act;
  btn.disabled = true;
  try {
    await api(`/admin/api/accounts/${encodeURIComponent(uid)}/${act}`, { method: 'POST' });
    toast(act === 'revive' ? '已复活并放回账号池' : '已清除冷却与熔断', 'ok');
    await loadAccounts(true);
  } catch (e) {
    toast('操作失败：' + e.message, 'err');
    btn.disabled = false;
  }
});

/* ── 请求记录 ────────────────────────────────────────── */

function requestDate(date) {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
  }).formatToParts(date);
  const value = (type) => parts.find((p) => p.type === type).value;
  return `${value('year')}-${value('month')}-${value('day')}`;
}

function updateRequestControls() {
  $$('#panel-requests select, #panel-requests input, #refreshRequests').forEach((el) => {
    el.disabled = store.requestLoading;
  });
  const size = Number($('#requestPageSize').value);
  $('#requestsPrev').disabled = store.requestLoading || store.requestPage <= 1;
  $('#requestsNext').disabled = store.requestLoading || store.requestPage * size >= store.requestTotal;
}

function renderRequestRows(rows) {
  $('#requestsTable tbody').innerHTML = rows.map((row) => {
    const text = row.input || row.inputTrunc || '';
    const preview = (row.inputTrunc || text).replace(/\s+/g, ' ').slice(0, 160);
    const credit = row.credit == null ? '—' : Number(row.credit).toLocaleString('zh-CN', { maximumFractionDigits: 8 });
    return `<tr>
      <td class="mono">${esc(row.requestTime || '-')}</td>
      <td class="request-content">
        ${text ? `<details><summary>${esc(preview || '查看请求内容')}</summary><pre>${esc(text)}</pre></details>` : '<span class="hint">无请求内容</span>'}
        <div class="mono request-id">${esc(row.requestId || '-')}</div>
      </td>
      <td class="mono" title="${esc(row.credit == null ? '官网未返回消耗值' : row.credit + ' credits')}">${esc(credit)}</td>
      <td class="mono">${esc(row.model || '-')}</td>
      <td>${esc(row.client || '-')}</td>
    </tr>`;
  }).join('') || '<tr><td colspan="5" class="hint">该账号在所选日期范围内暂无请求记录。</td></tr>';
}

async function loadRequestHistory(page = 1, reloadAccounts = false) {
  const seq = ++store.requestSeq;
  const key = store.key;
  store.requestLoading = true;
  updateRequestControls();
  $('#requestsHint').textContent = '正在查询官网请求记录…';
  $('#requestsTable tbody').innerHTML = '<tr><td colspan="5" class="hint">加载中…</td></tr>';
  try {
    if (!$('#requestStart').value || !$('#requestEnd').value) {
      $('#requestStart').value = requestDate(new Date(Date.now() - 7 * 86400000));
      $('#requestEnd').value = requestDate(new Date());
    }
    const select = $('#requestAccount');
    if (reloadAccounts || !select.value) {
      const st = await api('/status');
      if (seq !== store.requestSeq || key !== store.key) return;
      const current = select.value;
      select.innerHTML = (st.accounts || []).map((a) => `<option value="${esc(a.uid)}">${esc(a.nickname || a.uid)} · ${esc(a.uid.slice(0, 8))}</option>`).join('');
      if ((st.accounts || []).some((a) => a.uid === current)) select.value = current;
    }
    if (!select.value) {
      store.requestPage = 1;
      store.requestTotal = 0;
      $('#requestsHint').textContent = '暂无账号，请先在「账号」页添加账号。';
      $('#requestsPage').textContent = '第 1 页';
      $('#requestsTable tbody').innerHTML = '<tr><td colspan="5" class="hint">暂无账号。</td></tr>';
      return;
    }
    const start = $('#requestStart').value;
    const end = $('#requestEnd').value;
    if (start > end) throw new Error('结束日期不能早于开始日期');
    if ((Date.parse(end) - Date.parse(start)) / 86400000 > 31) throw new Error('日期间隔不能超过 31 天');
    const params = new URLSearchParams({ start, end, page: String(page), page_size: $('#requestPageSize').value });
    const data = await api(`/admin/api/accounts/${encodeURIComponent(select.value)}/requests?${params}`);
    if (seq !== store.requestSeq || key !== store.key) return;
    if (!data || !Array.isArray(data.data) || !Number.isInteger(data.total) || data.total < 0) throw new Error('请求记录响应格式无效');
    store.requestPage = page;
    store.requestTotal = data.total;
    renderRequestRows(data.data);
    const pages = Math.max(1, Math.ceil(data.total / Number($('#requestPageSize').value)));
    $('#requestsPage').textContent = `第 ${page} / ${pages} 页 · 共 ${data.total} 条`;
    $('#requestsHint').textContent = `已加载 ${data.data.length} 条 · 日期 ${start} 至 ${end} · 更新时间 ${fmtTime(new Date())}`;
  } catch (e) {
    if (seq !== store.requestSeq || key !== store.key) return;
    store.requestTotal = 0;
    store.requestPage = 1;
    $('#requestsPage').textContent = '查询未完成';
    $('#requestsHint').textContent = e.message;
    $('#requestsTable tbody').innerHTML = '<tr><td colspan="5" class="hint">查询失败，请检查上方提示并点击「刷新记录」重试。</td></tr>';
  } finally {
    if (seq === store.requestSeq) {
      store.requestLoading = false;
      updateRequestControls();
    }
  }
}

$('#refreshRequests').addEventListener('click', () => loadRequestHistory(1, true));
$$('#requestAccount, #requestStart, #requestEnd, #requestPageSize').forEach((el) => {
  el.addEventListener('change', () => loadRequestHistory(1));
});
$('#requestsPrev').addEventListener('click', () => loadRequestHistory(store.requestPage - 1));
$('#requestsNext').addEventListener('click', () => loadRequestHistory(store.requestPage + 1));

/* ── 模型 ────────────────────────────────────────────── */

async function loadModels(silent) {
  try {
    const data = await api('/v1/models');
    store.models = (data && data.data) || [];
    $('#modelsTable tbody').innerHTML = store.models.map((m) => `<tr>
      <td class="mono">${esc(m.id)}</td>
      <td>${esc(m.owned_by || '-')}</td>
      <td class="mono">${m.context_length || '-'}</td>
      <td class="mono">${m.max_output_tokens || '-'}</td>
      <td><button class="btn mini" data-copy="${esc(m.id)}">复制 ID</button></td>
    </tr>`).join('') || '<tr><td colspan="5" style="text-align:center;color:#8b98a9;padding:24px">无模型数据</td></tr>';
    // 同步到聊天页下拉
    const sel = $('#chatModel');
    const cur = sel.value;
    sel.innerHTML = store.models.map((m) => `<option value="${esc(m.id)}">${esc(m.id)}</option>`).join('');
    if (cur && store.models.some((m) => m.id === cur)) sel.value = cur;
    else if (store.models.some((m) => m.id === 'deepseek-v4-flash')) sel.value = 'deepseek-v4-flash';
    if (!silent) toast('模型列表已刷新（' + store.models.length + ' 个）', 'ok');
  } catch (e) {
    if (!silent) toast('加载模型失败：' + e.message, 'err');
  }
}

$('#modelsTable').addEventListener('click', async (ev) => {
  const btn = ev.target.closest('button[data-copy]');
  if (!btn) return;
  try {
    await navigator.clipboard.writeText(btn.dataset.copy);
    toast('已复制：' + btn.dataset.copy, 'ok');
  } catch (e) { toast('复制失败，请手动选择', 'warn'); }
});

/* ── 聊天测试 ────────────────────────────────────────── */

function chatRender(content, reasoning) {
  $('#chatOutput').textContent = content;
  $('#chatOutput').scrollTop = $('#chatOutput').scrollHeight;
  if (reasoning) {
    $('#chatReasoningWrap').hidden = false;
    $('#chatReasoning').textContent = reasoning;
  } else {
    $('#chatReasoningWrap').hidden = true;
  }
}

async function chatSend() {
  const input = $('#chatInput').value.trim();
  if (!input) { toast('请输入内容', 'warn'); return; }
  const model = $('#chatModel').value;
  if (!model) { toast('请先选择模型（可先刷新模型列表）', 'warn'); return; }
  const stream = $('#chatStream').checked;
  const sys = $('#chatSystem').value.trim();

  const messages = [];
  if (sys) messages.push({ role: 'system', content: sys });
  messages.push({ role: 'user', content: input });

  const btn = $('#chatSend');
  const stop = $('#chatStop');
  btn.disabled = true;
  stop.disabled = false;
  $('#chatStat').textContent = '请求中…';
  chatRender('', '');
  const t0 = performance.now();
  let ttfb = 0;

  store.chatCtrl = new AbortController();
  try {
    const res = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: Object.assign({ 'Content-Type': 'application/json' }, store.key ? { Authorization: 'Bearer ' + store.key } : {}),
      body: JSON.stringify({ model, messages, stream }),
      signal: store.chatCtrl.signal,
    });
    if (!res.ok) {
      const txt = await res.text();
      let msg = txt;
      try { msg = JSON.parse(txt).error.message; } catch (e) { /* 原样 */ }
      throw new Error('HTTP ' + res.status + '：' + msg);
    }

    if (!stream) {
      const j = await res.json();
      const msg = (j.choices && j.choices[0] && j.choices[0].message) || {};
      chatRender(msg.content || JSON.stringify(j, null, 2), msg.reasoning_content || '');
      const usage = j.usage || {};
      $('#chatStat').textContent = `非流式 · ${((performance.now() - t0) / 1000).toFixed(2)}s · tokens=${usage.completion_tokens || '?'}`;
    } else {
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = '', content = '', reasoning = '', usage = null, done = false;
      while (!done) {
        const { done: eof, value } = await reader.read();
        if (eof) break;
        buf += dec.decode(value, { stream: true });
        const lines = buf.split('\n');
        buf = lines.pop();
        for (const raw of lines) {
          const line = raw.trim();
          if (!line.startsWith('data:')) continue;
          const payload = line.slice(5).trim();
          if (payload === '[DONE]') { done = true; break; }
          let j;
          try { j = JSON.parse(payload); } catch (e) { continue; }
          if (j.error) throw new Error(j.error.message || 'upstream error');
          const d = j.choices && j.choices[0] && (j.choices[0].delta || {});
          if (d) {
            if (d.reasoning_content) reasoning += d.reasoning_content;
            if (d.content) content += d.content;
          }
          if (j.usage) usage = j.usage;
          if (!ttfb && (content || reasoning)) ttfb = performance.now() - t0;
          chatRender(content, reasoning);
        }
      }
      chatRender(content, reasoning);
      const total = (performance.now() - t0) / 1000;
      const toks = usage && usage.completion_tokens ? usage.completion_tokens : null;
      $('#chatStat').textContent =
        `流式 · TTFB ${ttfb ? Math.round(ttfb) + 'ms' : '-'} · 总 ${total.toFixed(2)}s` +
        (toks ? ` · ${toks} tok · ${(toks / total).toFixed(1)} tok/s` : '');
    }
  } catch (e) {
    if (e.name === 'AbortError') {
      $('#chatStat').textContent = '已停止';
      toast('已停止生成', 'warn');
    } else {
      toast('请求失败：' + e.message, 'err');
      $('#chatStat').textContent = '失败';
      chatRender($('#chatOutput').textContent + '\n[错误] ' + e.message, $('#chatReasoning').textContent);
    }
  } finally {
    btn.disabled = false;
    stop.disabled = true;
    store.chatCtrl = null;
  }
}

/* ── 配置 ────────────────────────────────────────────── */

const CONFIG_SCHEMA = [
  {
    group: '基础',
    fields: [
      { path: 'listen', label: 'listen', hint: 'HTTP 监听地址，如 :7863' },
      { path: 'api_key', label: 'api_key', type: 'password', hint: '空 = 不鉴权（公网必须设置）；显示 *** 表示沿用原值' },
      { path: 'auth_dir', label: 'auth_dir', hint: '账号凭证目录' },
      { path: 'state_file', label: 'state_file', hint: '账号池状态持久化文件' },
    ],
  },
  {
    group: '服务',
    fields: [
      { path: 'server.max_body_mb', label: 'server.max_body_mb', type: 'number', hint: '聊天请求体上限（MB），超限返回 413' },
    ],
  },
  {
    group: '冷却',
    fields: [
      { path: 'cooldown.soft_rate', label: 'cooldown.soft_rate', hint: '软限流冷却基数，如 600s' },
      { path: 'cooldown.soft_rate_max', label: 'cooldown.soft_rate_max', hint: '软冷却指数退避封顶，如 2h' },
    ],
  },
  {
    group: '定时任务',
    fields: [
      { path: 'schedule.checkin_enabled', label: 'checkin_enabled', type: 'bool', hint: '签到总开关' },
      { path: 'schedule.checkin_hours', label: 'checkin_hours', type: 'hours', hint: '签到整点，逗号分隔，如 9,21' },
      { path: 'schedule.travel_enabled', label: 'travel_enabled', type: 'bool', hint: '猫猫旅行总开关' },
      { path: 'schedule.travel_hours', label: 'travel_hours', type: 'hours', hint: '旅行整点' },
      { path: 'schedule.activity_enabled', label: 'activity_enabled', type: 'bool', hint: '活跃上报总开关' },
      { path: 'schedule.activity_hours', label: 'activity_hours', type: 'hours', hint: '活跃上报整点' },
      { path: 'schedule.keepalive_enabled', label: 'keepalive_enabled', type: 'bool', hint: 'token 保活总开关' },
      { path: 'schedule.keepalive_hours', label: 'keepalive_hours', type: 'hours', hint: '保活整点' },
      { path: 'schedule.activity_report_count', label: 'activity_report_count', type: 'number', hint: '每号每次上报条数（默认 5）' },
    ],
  },
  {
    group: '上游',
    fields: [
      { path: 'upstream.timeout_seconds', label: 'timeout_seconds', type: 'number', hint: '短 RPC 总时长上限（秒）' },
      { path: 'upstream.header_timeout_seconds', label: 'header_timeout_seconds', type: 'number', hint: '聊天首字节前上限（秒）' },
      { path: 'upstream.idle_timeout_seconds', label: 'idle_timeout_seconds', type: 'number', hint: '聊天流中空闲上限（秒）' },
      { path: 'upstream.user_agent', label: 'user_agent', hint: '出站 UA 覆盖，空 = CLI/2.63.2 CodeBuddy/2.63.2' },
    ],
  },
  {
    group: '提示词与脱敏',
    fields: [
      { path: 'prompt.mode', label: 'prompt.mode', type: 'select', options: ['custom', 'passthrough'], hint: 'custom = 网关自有提示词替换客户端 system' },
      { path: 'prompt.file', label: 'prompt.file', hint: '自定义提示词文件，空 = 内置默认；路径不可读会报错' },
      { path: 'features.sanitize_blacklist_fingerprints', label: 'sanitize_blacklist_fingerprints', type: 'bool', hint: '出站请求体指纹脱敏' },
    ],
  },
  {
    group: '账号池',
    fields: [
      { path: 'pool.max_in_flight', label: 'max_in_flight', type: 'number', hint: '单账号最大在途请求数，0 = 不限' },
      { path: 'pool.breaker_threshold', label: 'breaker_threshold', type: 'number', hint: '连续失败触发熔断阈值' },
      { path: 'pool.breaker_cooldown', label: 'breaker_cooldown', hint: '熔断基础退避时长，如 30m' },
      { path: 'pool.breaker_cooldown_max', label: 'breaker_cooldown_max', hint: '熔断退避封顶，如 6h' },
      { path: 'pool.idle_weight_per_hour', label: 'idle_weight_per_hour', type: 'number', step: '0.1', hint: '闲置补偿权重/小时' },
      { path: 'pool.idle_weight_max', label: 'idle_weight_max', type: 'number', step: '0.1', hint: '闲置补偿封顶' },
    ],
  },
  {
    group: '会话粘性',
    fields: [
      { path: 'session_sticky.enabled', label: 'enabled', type: 'bool', hint: '会话粘性路由开关' },
      { path: 'session_sticky.ttl', label: 'ttl', hint: '绑定 TTL，如 30m' },
      { path: 'session_sticky.gc_interval', label: 'gc_interval', hint: 'GC 周期，如 5m' },
    ],
  },
  {
    group: 'Redis（可选状态镜像）',
    fields: [
      { path: 'upstash.url', label: 'upstash.url', hint: '空 = 纯内存模式' },
      { path: 'upstash.token', label: 'upstash.token', type: 'password', hint: '显示 *** 表示沿用原值' },
    ],
  },
];

async function loadConfig() {
  try {
    store.cfg = await api('/admin/api/config');
  } catch (e) {
    $('#configForm').innerHTML = `<p class="hint">加载配置失败：${esc(e.message)}</p>`;
    return;
  }
  $('#configPath').textContent = store.cfg.path || '';
  store.cfgObj = store.cfg.file || {};
  renderConfigForm();
  $('#configJson').value = JSON.stringify(store.cfgObj, null, 2);
}

function renderConfigForm() {
  const obj = store.cfgObj || {};
  $('#configForm').innerHTML = CONFIG_SCHEMA.map((g) => `
    <div class="cfg-group">
      <h3>${esc(g.group)}</h3>
      <div class="cfg-fields">
        ${g.fields.map((f) => {
          const v = getPath(obj, f.path);
          const type = f.type || 'text';
          if (type === 'bool') {
            return `<div class="cfg-field cfg-check">
              <input type="checkbox" data-path="${esc(f.path)}" data-type="bool" ${v ? 'checked' : ''}>
              <span class="label">${esc(f.label)}</span>
              <div class="hint">${esc(f.hint || '')}</div>
            </div>`;
          }
          if (type === 'select') {
            return `<div class="cfg-field">
              <span class="label">${esc(f.label)}</span>
              <select data-path="${esc(f.path)}" data-type="select">
                ${(f.options || []).map((o) => `<option value="${esc(o)}" ${o === v ? 'selected' : ''}>${esc(o)}</option>`).join('')}
              </select>
              <div class="hint">${esc(f.hint || '')}</div>
            </div>`;
          }
          const inputType = type === 'number' ? 'number' : (type === 'password' ? 'password' : 'text');
          const step = f.step ? ` step="${f.step}"` : '';
          const val = v === undefined || v === null ? '' : (type === 'hours' ? (Array.isArray(v) ? v.join(',') : '') : v);
          return `<div class="cfg-field">
            <span class="label">${esc(f.label)}</span>
            <input type="${inputType}"${step} data-path="${esc(f.path)}" data-type="${type}" value="${esc(val)}" autocomplete="off">
            <div class="hint">${esc(f.hint || '')}</div>
          </div>`;
        }).join('')}
      </div>
    </div>`).join('');

  $('#configForm').querySelectorAll('[data-path]').forEach((el) => {
    el.addEventListener('change', () => applyField(el));
  });
}

function applyField(el) {
  const path = el.dataset.path;
  const type = el.dataset.type;
  let val;
  if (type === 'bool') val = el.checked;
  else if (type === 'number') val = el.value === '' ? '' : Number(el.value);
  else if (type === 'hours') {
    val = el.value.split(/[,，\s]+/).filter((x) => x !== '').map((x) => Number(x)).filter((n) => !isNaN(n));
  } else val = el.value;
  setPath(store.cfgObj, path, val);
  $('#configJson').value = JSON.stringify(store.cfgObj, null, 2);
}

function syncFromJson() {
  const raw = $('#configJson').value.trim();
  if (!raw) throw new Error('JSON 为空');
  const obj = JSON.parse(raw);
  if (typeof obj !== 'object' || obj === null || Array.isArray(obj)) throw new Error('配置必须是 JSON 对象');
  store.cfgObj = obj;
  renderConfigForm();
}

async function saveConfig(restart) {
  try {
    if (store.cfgSub === 'json') syncFromJson();
    const body = JSON.stringify(store.cfgObj);
    const res = await api('/admin/api/config', { method: 'PUT', body });
    toast(res.message || '配置已保存', 'ok');
    if (!restart) return;
    await api('/admin/api/restart', { method: 'POST' });
    toast('正在重启服务，5 秒后自动重连…', 'warn');
    setTimeout(() => { connect(true).then((ok) => { if (ok) toast('服务已重启完成', 'ok'); }); }, 6000);
    setTimeout(() => loadConfig().catch(() => {}), 7000);
  } catch (e) {
    toast('保存失败：' + e.message, 'err');
  }
}

/* ── 添加账号 ────────────────────────────────────────── */

function openLoginModal() {
  $('#loginModal').hidden = false;
  $('#loginStat').textContent = '正在获取授权链接…';
  $('#loginUrl').textContent = '';
  api('/admin/api/login/start', { method: 'POST' })
    .then((res) => {
      $('#loginUrl').href = res.auth_url;
      $('#loginUrl').textContent = res.auth_url;
      $('#loginStat').textContent = '等待登录…';
      try { window.open(res.auth_url, '_blank', 'noopener'); } catch (e) { /* 弹窗被拦则手动点 */ }
      pollLogin(res.state, 0);
    })
    .catch((e) => {
      $('#loginStat').textContent = '获取失败：' + e.message;
      toast('获取授权链接失败：' + e.message, 'err');
    });
}

function pollLogin(state, ticks) {
  if (store.loginTimer) clearInterval(store.loginTimer);
  const started = Date.now();
  store.loginTimer = setInterval(async () => {
    if (Date.now() - started > 10 * 60 * 1000) {
      clearInterval(store.loginTimer);
      $('#loginStat').textContent = '超时（10 分钟），请重新点击「添加账号」';
      return;
    }
    try {
      const res = await api('/admin/api/login/poll?state=' + encodeURIComponent(state));
      if (res.status === 'ok') {
        clearInterval(store.loginTimer);
        $('#loginStat').textContent = '登录成功';
        toast(`已添加账号 ${res.nickname || res.uid}（配置文件 ${res.file}），当前共 ${res.accounts} 个账号`, 'ok');
        closeLoginModal();
        loadAccounts(true);
        loadOverview();
      } else {
        $('#loginStat').textContent = '等待登录…（' + Math.floor((Date.now() - started) / 1000) + 's）';
      }
    } catch (e) {
      // 轮询失败不关闭流程：多为暂态网络问题，下一轮继续
      $('#loginStat').textContent = '轮询异常：' + e.message;
    }
  }, 3000);
}

function closeLoginModal() {
  $('#loginModal').hidden = true;
  if (store.loginTimer) clearInterval(store.loginTimer);
  store.loginTimer = null;
}

/* ── 事件绑定 ────────────────────────────────────────── */

$('#saveKey').addEventListener('click', async () => {
  store.key = $('#apiKey').value.trim();
  resetRealtimeLogs();
  resetDashboards();
  store.requestSeq++;
  store.requestLoading = false;
  store.requestPage = 1;
  store.requestTotal = 0;
  $('#requestAccount').innerHTML = '';
  $('#requestsTable tbody').innerHTML = '';
  $('#requestsHint').textContent = '';
  $('#requestsPage').textContent = '第 1 页';
  updateRequestControls();
  localStorage.setItem('wb2api_key', store.key);
  const ok = await connect(false);
  if (ok) {
    loadOverview();
    if ($('#panel-requests').classList.contains('active')) loadRequestHistory(1, true);
    if ($('#panel-logs').classList.contains('active')) loadRealtimeLogs('latest', true);
    if ($('#panel-usage').classList.contains('active')) loadUsageStats();
    if ($('#panel-health').classList.contains('active')) loadDiagnostics();
    if ($('#panel-performance').classList.contains('active')) { loadPerformance(); void loadPricingIntoForm(); }
  }
});

$('#apiKey').addEventListener('keydown', (e) => { if (e.key === 'Enter') $('#saveKey').click(); });

$('#toggleKey').addEventListener('click', () => {
  const el = $('#apiKey');
  el.type = el.type === 'password' ? 'text' : 'password';
  $('#toggleKey').setAttribute('aria-pressed', String(el.type === 'text'));
  $('#toggleKey').setAttribute('aria-label', el.type === 'text' ? '隐藏 API Key' : '显示 API Key');
});

$('#authBarFocus').addEventListener('click', () => {
  const el = $('#apiKey');
  el.classList.remove('invalid');
  el.focus();
  el.select();
});

$('#refreshOverview').addEventListener('click', loadOverview);
$('#refreshAccounts').addEventListener('click', () => loadAccounts(false));
$('#refreshModels').addEventListener('click', () => loadModels(false));
$('#autoRefresh').addEventListener('change', (e) => { e.target.checked ? startAuto() : stopAuto(); });

$('#chatSend').addEventListener('click', chatSend);
$('#chatStop').addEventListener('click', () => { if (store.chatCtrl) store.chatCtrl.abort(); });
$('#chatClear').addEventListener('click', () => { chatRender('', ''); $('#chatStat').textContent = ''; });
$('#chatInput').addEventListener('keydown', (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') chatSend();
});

$$('.sub-tab').forEach((btn) => {
  btn.addEventListener('click', () => {
    const sub = btn.dataset.sub;
    if (store.cfgSub === 'json' && sub === 'form') {
      try { syncFromJson(); } catch (e) { toast('JSON 解析失败：' + e.message, 'err'); return; }
    }
    store.cfgSub = sub;
    $$('.sub-tab').forEach((b) => b.classList.toggle('active', b === btn));
    $('#sub-form').classList.toggle('active', sub === 'form');
    $('#sub-json').classList.toggle('active', sub === 'json');
  });
});

$('#configReload').addEventListener('click', () => { loadConfig().then(() => toast('配置已重新加载', 'ok')); });
$('#configSave').addEventListener('click', () => saveConfig(false));
$('#configSaveRestart').addEventListener('click', () => saveConfig(true));

$('#addAccount').addEventListener('click', openLoginModal);
$('#loginClose').addEventListener('click', closeLoginModal);
$('#loginOpen').addEventListener('click', () => {
  const href = $('#loginUrl').href;
  if (href && href !== '#') window.open(href, '_blank', 'noopener');
});
$('#loginCopy').addEventListener('click', async () => {
  const href = $('#loginUrl').href;
  if (!href || href === '#') return;
  try { await navigator.clipboard.writeText(href); toast('授权链接已复制', 'ok'); }
  catch (e) { toast('复制失败，请手动选择链接', 'warn'); }
});

/* ── 启动 ────────────────────────────────────────────── */

(async function init() {
  const input = $('#apiKey');
  input.value = store.key;
  const ok = await connect(true);
  if (!ok) {
    // 没连上就把「缺 key / key 不对」直接摆在页面上（loadOverview 也会触发 markUnauthorized）
    markUnauthorized();
    if (!store.key) {
      toast('请先填写 API Key：见 config.json 的 api_key 字段', 'warn');
      input.focus();
    }
  }
  loadOverview();
})();
