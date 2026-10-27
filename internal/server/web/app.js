"use strict";

// ---------- helpers ----------
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = (v) => String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const inrFmt = new Intl.NumberFormat("en-IN", { maximumFractionDigits: 2 });
const inr = (n) => "₹" + inrFmt.format(n || 0);
const inrShort = (n) => {
  n = n || 0;
  if (n >= 1e7) return "₹" + (n / 1e7).toFixed(2) + " Cr";
  if (n >= 1e5) return "₹" + (n / 1e5).toFixed(2) + " L";
  return inr(Math.round(n));
};
const num = (n) => inrFmt.format(n || 0);
const pctTxt = (p) => (p == null ? "—" : (Math.round(p * 10) / 10) + "%");
const when = (t) => new Date(t).toLocaleString("en-IN", { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit", hour12: false });
const badge = (level, text) => `<span class="badge ${esc(level)}">${esc(text ?? level)}</span>`;
const ruleNames = { money_cycle: "Money-cycle legs" };
const ruleName = (id) => ruleNames[id] || id;
const debounce = (fn, ms) => { let t; return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); }; };

let apiKey = (() => { try { return localStorage.getItem("fraudlens.key") || ""; } catch { return ""; } })();

async function api(path, opts = {}) {
  const headers = Object.assign({}, opts.headers || {});
  if (apiKey) headers["X-API-Key"] = apiKey;
  const res = await fetch(path, Object.assign({}, opts, { headers }));
  if (res.status === 401) {
    const k = prompt("This server requires an API key:");
    if (k) {
      apiKey = k;
      try { localStorage.setItem("fraudlens.key", k); } catch {}
      return api(path, opts);
    }
  }
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || res.statusText);
  return body;
}
const withKey = (url) => (apiKey ? url + (url.includes("?") ? "&" : "?") + "key=" + encodeURIComponent(apiKey) : url);

// ---------- theme ----------
(() => {
  const root = document.documentElement;
  try { const t = localStorage.getItem("fraudlens.theme"); if (t) root.dataset.theme = t; } catch {}
  $("#theme").onclick = () => {
    const dark = root.dataset.theme ? root.dataset.theme === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
    root.dataset.theme = dark ? "light" : "dark";
    try { localStorage.setItem("fraudlens.theme", root.dataset.theme); } catch {}
  };
})();

// ---------- tooltip ----------
const tip = $("#tooltip");
function showTip(html, e) {
  tip.innerHTML = html;
  tip.style.display = "block";
  const r = tip.getBoundingClientRect();
  let x = e.clientX + 14, y = e.clientY + 14;
  if (x + r.width > innerWidth - 8) x = e.clientX - r.width - 14;
  if (y + r.height > innerHeight - 8) y = e.clientY - r.height - 14;
  tip.style.left = x + "px";
  tip.style.top = y + "px";
}
const hideTip = () => (tip.style.display = "none");

// ---------- tabs ----------
const loaders = {};
let current = "overview";
function show(view) {
  if (!document.getElementById("view-" + view)) view = "overview";
  current = view;
  closeDrawer();
  $$("#tabs button").forEach((b) => b.classList.toggle("active", b.dataset.view === view));
  $$("section.view").forEach((s) => s.classList.toggle("active", s.id === "view-" + view));
  if (location.hash !== "#" + view) history.replaceState(null, "", "#" + view);
  loaders[view] && loaders[view]();
}
$$("#tabs button").forEach((b) => (b.onclick = () => show(b.dataset.view)));

// ---------- bar chart (single series, vertical) ----------
function barChart(el, data, { cls = "", fmt = num, label = "" } = {}) {
  if (!data.length) { el.innerHTML = `<div class="empty">No data yet</div>`; return; }
  const W = 640, H = 150, L = 44, B = 20, T = 8;
  const max = Math.max(1, ...data.map((d) => d.value));
  const step = niceStep(max / 3);
  const top = Math.ceil(max / step) * step;
  const y = (v) => T + (H - T - B) * (1 - v / top);
  const bw = (W - L) / data.length;
  const gap = Math.min(2, bw * 0.25);
  let s = `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="${esc(label)}">`;
  for (let v = 0; v <= top + 1e-9; v += step) {
    s += `<line class="${v === 0 ? "baseline" : "grid-line"}" x1="${L}" x2="${W}" y1="${y(v)}" y2="${y(v)}"/>`;
    s += `<text x="${L - 6}" y="${y(v) + 4}" text-anchor="end">${esc(compact(v))}</text>`;
  }
  const every = Math.ceil(data.length / 8);
  data.forEach((d, i) => {
    const x = L + i * bw + gap / 2, w = Math.max(1, bw - gap), h = y(0) - y(d.value);
    const r = Math.min(4, w / 2, h);
    if (d.value > 0) s += `<path class="bar ${cls}" data-i="${i}" d="M${x},${y(0)} V${y(d.value) + r} Q${x},${y(d.value)} ${x + r},${y(d.value)} H${x + w - r} Q${x + w},${y(d.value)} ${x + w},${y(d.value) + r} V${y(0)} Z"/>`;
    if (i % every === 0) s += `<text x="${x + w / 2}" y="${H - 5}" text-anchor="middle">${esc(d.short || d.label)}</text>`;
    s += `<rect class="hit" data-i="${i}" x="${L + i * bw}" y="${T}" width="${bw}" height="${H - T - B}"/>`;
  });
  el.innerHTML = s + "</svg>";
  $$(".hit", el).forEach((h) => {
    const d = data[+h.dataset.i];
    const bar = $(`.bar[data-i="${h.dataset.i}"]`, el);
    h.onmousemove = (e) => { bar && bar.classList.add("hover"); showTip(`<b>${esc(d.label)}</b><br>${esc(d.tip || fmt(d.value))}`, e); };
    h.onmouseleave = () => { bar && bar.classList.remove("hover"); hideTip(); };
  });
}
function niceStep(x) {
  const p = Math.pow(10, Math.floor(Math.log10(Math.max(x, 1))));
  const m = x / p;
  return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 5 ? 5 : 10) * p;
}
function compact(v) {
  if (v >= 1e6) return (v / 1e6).toFixed(v % 1e6 ? 1 : 0) + "M";
  if (v >= 1e3) return (v / 1e3).toFixed(v % 1e3 ? 1 : 0) + "k";
  return String(v);
}

function hbars(el, rows, onClick) {
  if (!rows.length) { el.innerHTML = `<div class="empty">Nothing flagged yet</div>`; return; }
  const max = Math.max(...rows.map((r) => r.value), 1);
  el.innerHTML = rows.map((r, i) => `<div class="hbar ${onClick ? "click" : ""}" data-i="${i}">
      <span class="name" title="${esc(r.label)}">${esc(r.label)}</span>
      <span class="track"><span class="fill" style="display:block;width:${(r.value / max) * 100}%"></span></span>
      <span class="n">${esc(r.text ?? num(r.value))}</span></div>`).join("");
  if (onClick) $$(".hbar", el).forEach((d) => (d.onclick = () => onClick(rows[+d.dataset.i])));
}

// ---------- overview ----------
async function loadOverview() {
  const [s, c] = await Promise.all([api("/api/v1/stats"), api("/api/v1/csps?limit=8")]);
  const sev = s.alerts_by_severity || {};
  const kpis = [
    ["Transactions", num(s.transactions), s.failed ? `${num(s.failed)} failed` : (s.first_txn ? `${new Date(s.first_txn).toLocaleDateString("en-IN")} – ${new Date(s.last_txn).toLocaleDateString("en-IN")}` : "Upload a sheet to start")],
    ["Volume", inrShort(s.amount), `${num(s.csps)} CSPs · ${num(s.accounts)} accounts`],
    ["Alerts", num(s.alerts), `${num(sev.high || 0)} high · ${num(sev.medium || 0)} medium`],
    ["To block", num(s.high_risk_txns), `${num(s.review_txns)} to review`],
    ["Flagged transactions", num(s.flagged_txns), s.transactions ? pctTxt((s.flagged_txns / s.transactions) * 100) + " of all" : ""],
    ["Decision latency", s.latency_p50_us ? s.latency_p50_us.toFixed(1) + " µs" : "—", s.latency_p99_us ? `p99 ${s.latency_p99_us.toFixed(1)} µs per transaction` : "per transaction"],
  ];
  $("#kpis").innerHTML = kpis.map(([l, v, n]) => `<div class="card kpi"><div class="label">${esc(l)}</div><div class="value">${esc(v)}</div><div class="note">${esc(n)}</div></div>`).join("");

  const days = (s.daily || []).slice(-60);
  const short = (d) => d.slice(8) + "/" + d.slice(5, 7);
  barChart($("#chart-daily"), days.map((d) => ({ label: d.day, short: short(d.day), value: d.txns, tip: `${num(d.txns)} transactions · ${inrShort(d.amount)}` })), { label: "Transactions per day" });
  barChart($("#chart-alerts"), days.map((d) => ({ label: d.day, short: short(d.day), value: d.alerts, tip: `${num(d.alerts)} alerts · ${num(d.flagged)} flagged transactions` })), { cls: "s2", label: "Alerts per day" });

  const rules = Object.entries(s.alerts_by_rule || {}).sort((a, b) => b[1] - a[1]).map(([id, n]) => ({ id, label: ruleName(id), value: n }));
  hbars($("#by-rule"), rules, (r) => { $("#al-rule").value = r.id; show("alerts"); });

  const items = c.items || [];
  $("#top-csps").innerHTML = items.length ? `<div class="table-wrap"><table><thead><tr><th>CSP</th><th>Risk</th><th>Why</th></tr></thead><tbody>${items.map((r) => `
    <tr class="click" data-csp="${esc(r.csp_id)}"><td class="mono">${esc(r.csp_id)}</td><td>${badge(r.risk_level, r.risk_score)}</td>
    <td>${r.flags.slice(0, 3).map((f) => `<span class="chip">${esc(f)}</span>`).join("")}${r.flags.length > 3 ? `<span class="muted">+${r.flags.length - 3}</span>` : ""}</td></tr>`).join("")}</tbody></table></div>` : `<div class="empty">No CSPs yet</div>`;
  $$("#top-csps tr[data-csp]").forEach((tr) => (tr.onclick = () => openCSP(tr.dataset.csp)));
}
loaders.overview = loadOverview;

function feedItem(a, fresh) {
  return `<div class="item ${fresh ? "fresh" : ""}" data-txn="${esc(a.txn_id)}">${badge(a.severity, a.severity)}
    <div><b>${esc(ruleName(a.rule))}</b> · <span class="mono">${esc(a.csp_id)}</span> · ${inr(a.amount)}<br><span class="msg">${esc(a.message)}</span></div>
    <div class="meta">${esc(when(a.timestamp))} · txn <span class="mono">${esc(a.txn_id)}</span></div></div>`;
}
function bindFeed() { $$("#feed .item").forEach((d) => (d.onclick = () => openTxn(d.dataset.txn))); }
async function loadFeed() {
  const r = await api("/api/v1/alerts?limit=40&severity=medium");
  $("#feed").innerHTML = r.items.length ? r.items.map((a) => feedItem(a)).join("") : `<div class="empty">Waiting for alerts…</div>`;
  bindFeed();
}

// ---------- live stream ----------
const refreshVisible = debounce(() => { loaders[current] && loaders[current](); }, 1500);
function connect() {
  const es = new EventSource(withKey("/api/v1/stream"));
  const live = $("#live");
  es.addEventListener("hello", () => { live.classList.add("on"); $("span", live).textContent = "live"; });
  es.addEventListener("alerts", (e) => {
    const d = JSON.parse(e.data);
    const items = d.items.filter((a) => a.severity !== "low").reverse();
    if (items.length) {
      const feed = $("#feed");
      if ($(".empty", feed)) feed.innerHTML = "";
      feed.insertAdjacentHTML("afterbegin", items.slice(0, 15).map((a) => feedItem(a, true)).join(""));
      $$(".item", feed).slice(60).forEach((n) => n.remove());
      bindFeed();
    }
  });
  es.addEventListener("ingest", refreshVisible);
  es.addEventListener("reset", () => { loadFeed(); refreshVisible(); });
  es.onerror = () => { live.classList.remove("on"); $("span", live).textContent = "reconnecting"; };
}

// ---------- alerts ----------
let alPage = 0;
const AL_LIMIT = 100;
function alQuery() {
  const p = new URLSearchParams();
  const v = (id) => $(id).value.trim();
  if (v("#al-rule")) p.set("rule", v("#al-rule"));
  if (v("#al-sev")) p.set("severity", v("#al-sev"));
  if (v("#al-csp")) p.set("csp", v("#al-csp"));
  if (v("#al-acct")) p.set("account", v("#al-acct"));
  return p;
}
async function loadAlerts() {
  const p = alQuery();
  $("#al-export").href = withKey("/api/v1/export/alerts.csv?" + p);
  p.set("limit", AL_LIMIT); p.set("offset", alPage * AL_LIMIT);
  const r = await api("/api/v1/alerts?" + p);
  $("#al-table").innerHTML = `<thead><tr><th>Time</th><th>Severity</th><th>Rule</th><th>CSP</th><th>Account</th><th class="num">Amount</th><th>Details</th><th>Txn</th></tr></thead><tbody>${
    r.items.map((a) => `<tr class="click" data-txn="${esc(a.txn_id)}"><td>${esc(when(a.timestamp))}</td><td>${badge(a.severity)}</td><td>${esc(ruleName(a.rule))}</td>
      <td class="mono"><a href="#" data-csp="${esc(a.csp_id)}">${esc(a.csp_id)}</a></td><td class="mono">${esc(a.account || "")}</td><td class="num">${inr(a.amount)}</td>
      <td class="msg">${esc(a.message)}</td><td class="mono">${esc(a.txn_id)}</td></tr>`).join("") || `<tr><td colspan="8" class="empty">No alerts match</td></tr>`}</tbody>`;
  bindRows("#al-table");
  pager("#al-pager", r.total, alPage, AL_LIMIT, (p) => { alPage = p; loadAlerts(); });
}
loaders.alerts = loadAlerts;
["#al-rule", "#al-sev"].forEach((id) => ($(id).onchange = () => { alPage = 0; loadAlerts(); }));
["#al-csp", "#al-acct"].forEach((id) => ($(id).oninput = debounce(() => { alPage = 0; loadAlerts(); }, 300)));

function bindRows(sel) {
  $$(`${sel} a[data-csp]`).forEach((a) => (a.onclick = (e) => { e.preventDefault(); e.stopPropagation(); openCSP(a.dataset.csp); }));
  $$(`${sel} tr[data-txn]`).forEach((tr) => (tr.onclick = () => openTxn(tr.dataset.txn)));
}
function pager(sel, total, page, limit, go) {
  const el = $(sel);
  const pages = Math.max(1, Math.ceil(total / limit));
  el.innerHTML = `<span>${num(total)} total</span><button class="btn" ${page <= 0 ? "disabled" : ""} data-p="${page - 1}">Previous</button><span>Page ${page + 1} of ${pages}</span><button class="btn" ${page + 1 >= pages ? "disabled" : ""} data-p="${page + 1}">Next</button>`;
  $$("button", el).forEach((b) => (b.onclick = () => go(+b.dataset.p)));
}

// ---------- CSP table ----------
let cspRows = [], cspSort = { key: "risk_score", dir: -1 };
const cspCols = [
  ["csp_id", "CSP", (r) => `<span class="mono">${esc(r.csp_id)}</span><div class="muted">${esc(r.peer_group)}</div>`],
  ["risk_score", "Risk", (r) => badge(r.risk_level, r.risk_score), "num"],
  ["flags", "Why", (r) => r.flags.slice(0, 4).map((f) => `<span class="chip">${esc(f)}</span>`).join("") + (r.flags.length > 4 ? `<span class="muted">+${r.flags.length - 4}</span>` : ""), "why"],
  ["count", "Txns", (r) => num(r.count), "num"],
  ["amount", "Amount", (r) => inrShort(r.amount), "num"],
  ["split_count_pct", "Split % (cnt / amt)", (r) => `${pctTxt(r.split_count_pct)} / ${pctTxt(r.split_amount_pct)}`, "num"],
  ["round_trip_count_pct", "Round trip % · group", (r) => `${pctTxt(r.round_trip_count_pct)} · G${r.round_trip_group}${r.loops ? ` (${r.loops} loops)` : ""}`, "num"],
  ["extra_count", "Extra 1% (cnt · earned)", (r) => r.extra_count ? `${num(r.extra_count)} · ${inr(r.extra_earned)}` : "—", "num"],
  ["test_deposit_accounts", "₹1 test", (r) => r.test_deposit_accounts?.length || "—", "num"],
  ["relative_accounts", "Relatives", (r) => r.relative_accounts?.length || "—", "num"],
  ["pmjjby_count", "₹436", (r) => r.pmjjby_count || "—", "num"],
  ["sub_ko_amount_share", "Sub-KO amt", (r) => pctTxt(r.sub_ko_amount_share * 100), "num"],
  ["small_amount_count", "₹50", (r) => r.small_amount_count || "—", "num"],
  ["night_pct", "Night %", (r) => pctTxt(r.night_pct), "num"],
  ["attendance", "Attendance", (r) => `${r.active_days}d · ${pctTxt(r.attendance * 100)}`, "num"],
  ["count_change_pct", "Spike / drop", (r) => r.count_change_pct == null ? "—" : `${r.spike_level ? badge(r.spike_level === "red" ? "high" : r.spike_level === "amber" ? "medium" : "low", r.spike_level) + " " : ""}${r.count_change_pct > 0 ? "+" : ""}${pctTxt(r.count_change_pct)}`, "num"],
  ["anomaly_score", "Peer anomaly", (r) => r.anomaly_level ? badge(r.anomaly_level === "high" ? "high" : "medium", r.anomaly_score) : (r.anomaly_score || "—"), "num"],
  ["alerts", "Alerts", (r) => num(r.alerts), "num"],
];
async function loadCSPs() {
  const p = new URLSearchParams();
  if ($("#csp-q").value.trim()) p.set("q", $("#csp-q").value.trim());
  if ($("#csp-level").value) p.set("level", $("#csp-level").value);
  $("#csp-export").href = withKey("/api/v1/export/csps.csv");
  cspRows = (await api("/api/v1/csps?" + p)).items;
  renderCSPs();
}
function renderCSPs() {
  const k = cspSort.key;
  const val = (r) => (Array.isArray(r[k]) ? r[k].length : r[k] ?? -Infinity);
  const rows = [...cspRows].sort((a, b) => (val(a) > val(b) ? 1 : val(a) < val(b) ? -1 : 0) * cspSort.dir);
  $("#csp-table").innerHTML = `<thead><tr>${cspCols.map(([key, label, , c]) => `<th data-sort="${key}" class="${c || ""}">${esc(label)}${key === k ? (cspSort.dir > 0 ? " ▲" : " ▼") : ""}</th>`).join("")}</tr></thead>
    <tbody>${rows.map((r) => `<tr class="click" data-csp="${esc(r.csp_id)}">${cspCols.map(([, , f, c]) => `<td class="${c || ""}">${f(r)}</td>`).join("")}</tr>`).join("") || `<tr><td colspan="${cspCols.length}" class="empty">No CSPs</td></tr>`}</tbody>`;
  $$("#csp-table th[data-sort]").forEach((th) => (th.onclick = () => {
    cspSort = { key: th.dataset.sort, dir: cspSort.key === th.dataset.sort ? -cspSort.dir : -1 };
    renderCSPs();
  }));
  $$("#csp-table tr[data-csp]").forEach((tr) => (tr.onclick = () => openCSP(tr.dataset.csp)));
}
loaders.csps = loadCSPs;
$("#csp-q").oninput = debounce(loadCSPs, 300);
$("#csp-level").onchange = loadCSPs;

// ---------- drawer ----------
function openDrawer(html) {
  $("#drawer-body").innerHTML = html;
  $("#drawer").classList.add("open");
  $("#drawer-bg").classList.add("open");
  $("#drawer").scrollTop = 0;
}
function closeDrawer() { $("#drawer").classList.remove("open"); $("#drawer-bg").classList.remove("open"); }
$("#drawer-close").onclick = closeDrawer;
$("#drawer-bg").onclick = closeDrawer;
addEventListener("keydown", (e) => e.key === "Escape" && closeDrawer());

const kv = (pairs) => `<div class="kv">${pairs.map(([k, v]) => `<div><span>${esc(k)}</span><b>${v}</b></div>`).join("")}</div>`;

async function openCSP(id) {
  openDrawer(`<div class="empty">Loading ${esc(id)}…</div>`);
  const d = await api("/api/v1/csps/" + encodeURIComponent(id));
  const r = d.report;
  const months = r.months || [];
  let html = `<h2 class="mono">${esc(r.csp_id)}</h2><p class="sub">${esc(r.peer_group)} · ${esc(r.first_day)} to ${esc(r.last_day)} · ${badge(r.risk_level, "risk " + r.risk_score)}</p>
    <div>${r.flags.map((f) => `<span class="chip">${esc(f)}</span>`).join("") || `<span class="muted">No CSP-level flags</span>`}</div>
    <div class="section-gap">${kv([
      ["Transactions", num(r.count) + (r.failed_count ? ` <span class="muted">+${num(r.failed_count)} failed</span>` : "")],
      ["Amount", inr(r.amount)],
      ["Split (count / amount)", `${num(r.split_count)} · ${pctTxt(r.split_count_pct)} / ${pctTxt(r.split_amount_pct)}`],
      ["Round trip", `${num(r.round_trip_count)} · ${pctTxt(r.round_trip_count_pct)} · G${r.round_trip_group}`],
      ["Loops", num(r.loops)],
      ["Extra 1% charges", `${num(r.extra_count)} · ${inr(r.extra_amount)}`],
      ["Extra earned", inr(r.extra_earned)],
      ["₹436 transactions", `${num(r.pmjjby_count)} · ${inr(r.pmjjby_amount)}`],
      ["Sub-KO share (count / amount)", `${pctTxt(r.sub_ko_count_share * 100)} / ${pctTxt(r.sub_ko_amount_share * 100)}`],
      ["₹50 transactions", num(r.small_amount_count)],
      ["Night transactions", `${num(r.night_count)} · ${pctTxt(r.night_pct)}`],
      ["Attendance (latest month)", `${r.active_days} days · ${pctTxt(r.attendance * 100)}`],
      ["Change vs previous", r.count_change_pct == null ? "—" : `count ${pctTxt(r.count_change_pct)} · amount ${pctTxt(r.amount_change_pct)}`],
      ["Latest day vs 7-day avg", r.day_change_pct == null ? "—" : pctTxt(r.day_change_pct)],
      ["Peer anomaly", `${r.anomaly_score}${r.anomaly_level ? " · " + esc(r.anomaly_level) : ""}`],
      ["Rings", r.ring_ids?.length ? r.ring_ids.map((x) => "#" + x).join(", ") : "—"],
    ])}</div>`;
  if (r.anomaly_drivers) html += `<p class="sub section-gap">Anomaly drivers (robust z-score vs peers): ${Object.entries(r.anomaly_drivers).map(([k, v]) => `<span class="chip">${esc(k)} ${v > 0 ? "+" : ""}${v}</span>`).join("")}</p>`;
  html += `<h3 class="section-gap">Monthly activity</h3><div class="table-wrap"><table><thead><tr><th>Month</th><th class="num">Active days</th><th class="num">Attendance</th><th class="num">Txns</th><th class="num">Amount</th><th class="num">Count vs peers (z)</th><th class="num">Count Δ</th><th class="num">Amount Δ</th><th>Spike/drop</th></tr></thead><tbody>${
    months.map((m) => `<tr><td>${esc(m.month)}</td><td class="num">${m.active_days} / ${m.days_in_data}</td><td class="num">${pctTxt(m.attendance * 100)}</td><td class="num">${num(m.count)}</td><td class="num">${inrShort(m.amount)}</td>
      <td class="num">${m.count_z}${m.count_flag ? " " + badge("medium", m.count_flag) : ""}</td><td class="num">${pctTxt(m.count_change_pct)}</td><td class="num">${pctTxt(m.amount_change_pct)}</td>
      <td>${m.spike_level ? badge(m.spike_level === "red" ? "high" : m.spike_level === "amber" ? "medium" : "low", m.spike_level) : ""}</td></tr>`).join("")}</tbody></table></div>`;
  if (r.relative_accounts?.length) html += `<h3 class="section-gap">Transfers to relative / own accounts</h3><div class="table-wrap"><table><thead><tr><th>Destination</th><th class="num">Txns</th><th class="num">Amount</th><th class="num">Days used</th><th class="num">Source accounts</th><th class="num">CSPs using it</th></tr></thead><tbody>${
    r.relative_accounts.map((x) => `<tr><td class="mono">${esc(x.account)}</td><td class="num">${x.txn_count}</td><td class="num">${inr(x.amount)}</td><td class="num">${x.days_used}</td><td class="num">${x.source_accounts}</td><td class="num">${x.csps_using_destination}</td></tr>`).join("")}</tbody></table></div>`;
  if (r.test_deposit_accounts?.length) html += `<h3 class="section-gap">₹1 test-deposit accounts</h3><div>${r.test_deposit_accounts.map((a) => `<span class="chip mono">${esc(a)}</span>`).join("")}</div>`;
  const sub = (r.sub_ko_breakdown || []).filter((b) => b.sub_ko_count > 0).slice(0, 30);
  if (sub.length) html += `<h3 class="section-gap">Sub-KO share by date and type</h3><div class="table-wrap"><table><thead><tr><th>Date</th><th>Type</th><th class="num">Txns</th><th class="num">Via sub-KO</th><th class="num">Count share</th><th class="num">Amount share</th></tr></thead><tbody>${
    sub.map((b) => `<tr><td>${esc(b.day)}</td><td>${esc(b.type)}</td><td class="num">${b.count}</td><td class="num">${b.sub_ko_count}</td><td class="num">${pctTxt(b.count_share * 100)}</td><td class="num">${pctTxt(b.amount_share * 100)}</td></tr>`).join("")}</tbody></table></div>`;
  html += `<h3 class="section-gap">Alerts (${d.alerts.length})</h3><div class="table-wrap"><table id="d-alerts"><thead><tr><th>Time</th><th>Severity</th><th>Rule</th><th>Account</th><th class="num">Amount</th><th>Details</th></tr></thead><tbody>${
    d.alerts.map((a) => `<tr class="click" data-txn="${esc(a.txn_id)}"><td>${esc(when(a.timestamp))}</td><td>${badge(a.severity)}</td><td>${esc(ruleName(a.rule))}</td><td class="mono">${esc(a.account || "")}</td><td class="num">${inr(a.amount)}</td><td class="msg">${esc(a.message)}</td></tr>`).join("") || `<tr><td colspan="6" class="empty">No alerts</td></tr>`}</tbody></table></div>`;
  openDrawer(html);
  bindRows("#d-alerts");
}

async function openTxn(id) {
  openDrawer(`<div class="empty">Loading ${esc(id)}…</div>`);
  let d;
  try { d = await api("/api/v1/transactions/" + encodeURIComponent(id)); } catch (e) { openDrawer(`<div class="empty">${esc(e.message)}</div>`); return; }
  const t = d.transaction;
  let html = `<h2 class="mono">${esc(t.txn_id)}</h2><p class="sub">${esc(when(t.timestamp))} · <a href="#" data-csp="${esc(t.csp_id)}">${esc(t.csp_id)}</a> · ${badge(t.risk_level, `${t.action} · risk ${t.risk_score}`)}</p>
    ${kv([["Type", esc(t.txn_type || t.kind)], ["Amount", inr(t.amount)], ["Customer charge", inr(t.customer_charge)], ["From", `<span class="mono">${esc(t.from_account || "—")}</span>`],
      ["To", `<span class="mono">${esc(t.to_account || "—")}</span>`], ["KO", `<span class="mono">${esc(t.ko_id || "—")}</span>`], ["Channel", esc(t.channel || "ONUS")], ["Status", t.failed ? "Failed" : "Success"]])}
    <h3 class="section-gap">Signals</h3>${t.signals.length ? t.signals.map(signalHTML).join("") : `<p class="muted">No rule fired on this transaction.</p>`}`;
  openDrawer(html);
  $$("#drawer-body a[data-csp]").forEach((a) => (a.onclick = (e) => { e.preventDefault(); openCSP(a.dataset.csp); }));
  $$("#drawer-body [data-txn]").forEach((a) => (a.onclick = (e) => { e.preventDefault(); openTxn(a.dataset.txn); }));
}
function signalHTML(s) {
  const rel = (s.related_txns || []).map((r) => `<a href="#" class="mono" data-txn="${esc(r)}">${esc(r)}</a>`).join(", ");
  return `<div class="signal">${badge(s.severity)} <span class="r">${esc(ruleName(s.rule))}</span> <span class="muted">weight ${s.weight}</span><div>${esc(s.message)}</div>${rel ? `<div class="muted">Related: ${rel}</div>` : ""}</div>`;
}

// ---------- network ----------
let net = null;
async function loadNetwork() {
  net = await api("/api/v1/network");
  $("#ring-table").innerHTML = `<thead><tr><th>#</th><th class="num">CSPs</th><th class="num">Accounts</th><th class="num">Alerts</th><th class="num">Cycles</th><th>Rules</th></tr></thead><tbody>${
    net.rings.map((r) => `<tr class="click" data-ring="${r.id}"><td>${r.id}</td><td class="num">${r.csps.length}</td><td class="num">${r.accounts.length}</td><td class="num">${r.alerts}</td><td class="num">${r.cycles}</td><td>${Object.entries(r.rules).map(([k, v]) => `<span class="chip">${esc(ruleName(k))} ${v}</span>`).join("")}</td></tr>`).join("") || `<tr><td colspan="6" class="empty">No rings detected</td></tr>`}</tbody>`;
  $$("#ring-table tr[data-ring]").forEach((tr) => (tr.onclick = () => { $("#ring-pick").value = tr.dataset.ring; drawRing(); }));
  $("#ring-pick").innerHTML = net.rings.map((r) => `<option value="${r.id}">Ring #${r.id} · ${r.csps.length} CSPs · ${r.accounts.length} accounts</option>`).join("");
  $("#ring-pick").onchange = drawRing;
  drawRing();
  $("#cycle-table").innerHTML = `<thead><tr><th>#</th><th>CSPs</th><th class="num">Accounts</th><th class="num">Txns</th><th class="num">Amount</th><th class="num">Days</th></tr></thead><tbody>${
    net.cycles.slice(0, 100).map((c) => `<tr title="${esc(c.accounts.join(" → "))}"><td>${c.id}</td><td>${c.csps.map((x) => `<a href="#" class="chip mono" data-csp="${esc(x)}">${esc(x)}</a>`).join("")}${c.cross_csp ? " " + badge("high", "cross-CSP") : ""}</td><td class="num">${c.accounts.length}</td><td class="num">${c.txns}</td><td class="num">${inr(c.amount)}</td><td class="num">${c.days}</td></tr>`).join("") || `<tr><td colspan="6" class="empty">No cycles</td></tr>`}</tbody>`;
  $("#hub-table").innerHTML = `<thead><tr><th>Account</th><th class="num">CSPs</th><th class="num">Txns</th><th class="num">Amount</th></tr></thead><tbody>${
    net.hubs.slice(0, 100).map((h) => `<tr title="${esc(h.csps.join(", "))}"><td class="mono">${esc(h.account)}</td><td class="num">${h.csp_count}</td><td class="num">${h.txns}</td><td class="num">${inr(h.amount)}</td></tr>`).join("") || `<tr><td colspan="4" class="empty">No hub accounts</td></tr>`}</tbody>`;
  bindRows("#cycle-table");
}
loaders.network = loadNetwork;

function drawRing() {
  const svg = $("#net-svg");
  const ring = net && net.rings.find((r) => String(r.id) === $("#ring-pick").value);
  if (!ring) { svg.innerHTML = `<text x="20" y="30">No ring selected</text>`; return; }
  const W = svg.clientWidth || 700, H = 520;
  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);
  const nodes = new Map();
  const node = (id, type) => {
    if (!nodes.has(id)) nodes.set(id, { id, type, x: W / 2 + (Math.random() - 0.5) * W * 0.6, y: H / 2 + (Math.random() - 0.5) * H * 0.6, vx: 0, vy: 0, deg: 0 });
    return nodes.get(id);
  };
  ring.csps.forEach((c) => node("c:" + c, "csp"));
  const edges = ring.edges.map((e) => ({ a: node("c:" + e.csp, "csp"), b: node("a:" + e.account, "acct"), w: e.weight }));
  if (!edges.length) ring.accounts.slice(0, 60).forEach((a) => ring.csps.forEach((c) => edges.push({ a: node("c:" + c, "csp"), b: node("a:" + a, "acct"), w: 1 })));
  edges.forEach((e) => { e.a.deg++; e.b.deg++; });
  // Fruchterman–Reingold layout with cooling.
  const list = [...nodes.values()];
  const kk = Math.sqrt((W * H) / Math.max(list.length, 1)) * 0.5;
  let temp = W / 8;
  for (let it = 0; it < 300; it++) {
    list.forEach((n) => { n.vx = 0; n.vy = 0; });
    for (let i = 0; i < list.length; i++) for (let j = i + 1; j < list.length; j++) {
      const p = list[i], q = list[j];
      const dx = p.x - q.x, dy = p.y - q.y, d = Math.max(0.5, Math.hypot(dx, dy));
      const f = (kk * kk) / d;
      p.vx += (dx / d) * f; p.vy += (dy / d) * f; q.vx -= (dx / d) * f; q.vy -= (dy / d) * f;
    }
    edges.forEach((e) => {
      const dx = e.b.x - e.a.x, dy = e.b.y - e.a.y, d = Math.max(0.5, Math.hypot(dx, dy));
      const f = (d * d) / kk;
      e.a.vx += (dx / d) * f; e.a.vy += (dy / d) * f; e.b.vx -= (dx / d) * f; e.b.vy -= (dy / d) * f;
    });
    list.forEach((n) => {
      n.vx += (W / 2 - n.x) * 0.3; n.vy += (H / 2 - n.y) * 0.3;
      const len = Math.max(0.01, Math.hypot(n.vx, n.vy)), step = Math.min(len, temp);
      n.x = Math.max(30, Math.min(W - 90, n.x + (n.vx / len) * step));
      n.y = Math.max(20, Math.min(H - 20, n.y + (n.vy / len) * step));
    });
    temp = Math.max(1, temp * 0.97);
  }
  const maxW = Math.max(...edges.map((e) => e.w), 1);
  let s = edges.map((e) => `<line class="edge" x1="${e.a.x}" y1="${e.a.y}" x2="${e.b.x}" y2="${e.b.y}" stroke-width="${1 + (e.w / maxW) * 3}"/>`).join("");
  s += list.map((n) => {
    const r = n.type === "csp" ? 9 : 5 + Math.min(4, n.deg);
    const label = n.id.slice(2);
    return `<g data-id="${esc(n.id)}"><circle class="${n.type === "csp" ? "node-csp" : "node-acct"}" cx="${n.x}" cy="${n.y}" r="${r}"/>${n.type === "csp" || list.length < 40 ? `<text x="${n.x + r + 3}" y="${n.y + 4}">${esc(label)}</text>` : ""}</g>`;
  }).join("");
  svg.innerHTML = s;
  $$("g[data-id]", svg).forEach((g) => {
    const n = nodes.get(g.dataset.id);
    g.onmousemove = (e) => showTip(`<b>${n.type === "csp" ? "CSP" : "Account"}</b> ${esc(n.id.slice(2))}<br>${n.deg} link(s)`, e);
    g.onmouseleave = hideTip;
    if (n.type === "csp") { g.style.cursor = "pointer"; g.onclick = () => openCSP(n.id.slice(2)); }
  });
}

// ---------- transactions ----------
let txPage = 0;
async function loadTxns() {
  const p = new URLSearchParams({ limit: 100, offset: txPage * 100 });
  if ($("#tx-csp").value.trim()) p.set("csp", $("#tx-csp").value.trim());
  if ($("#tx-acct").value.trim()) p.set("account", $("#tx-acct").value.trim());
  if ($("#tx-flagged").checked) p.set("flagged", "true");
  const r = await api("/api/v1/transactions?" + p);
  $("#tx-table").innerHTML = `<thead><tr><th>Time</th><th>Txn</th><th>CSP</th><th>Type</th><th>From</th><th>To</th><th class="num">Amount</th><th>Risk</th><th>Signals</th></tr></thead><tbody>${
    r.items.map((t) => `<tr class="click" data-txn="${esc(t.txn_id)}"><td>${esc(when(t.timestamp))}</td><td class="mono">${esc(t.txn_id)}</td><td class="mono"><a href="#" data-csp="${esc(t.csp_id)}">${esc(t.csp_id)}</a></td>
      <td>${esc(t.txn_type || t.kind)}${t.failed ? ' <span class="muted">(failed)</span>' : ""}</td><td class="mono">${esc(t.from_account || "")}</td><td class="mono">${esc(t.to_account || "")}</td><td class="num">${inr(t.amount)}</td>
      <td>${badge(t.risk_level, t.risk_score)}</td><td>${t.signals.map((s) => `<span class="chip">${esc(ruleName(s.rule))}</span>`).join("")}</td></tr>`).join("") || `<tr><td colspan="9" class="empty">No transactions</td></tr>`}</tbody>`;
  bindRows("#tx-table");
  pager("#tx-pager", r.total, txPage, 100, (p) => { txPage = p; loadTxns(); });
}
loaders.txns = loadTxns;
["#tx-csp", "#tx-acct"].forEach((id) => ($(id).oninput = debounce(() => { txPage = 0; loadTxns(); }, 300)));
$("#tx-flagged").onchange = () => { txPage = 0; loadTxns(); };
$("#tx-find").onclick = () => $("#tx-id").value.trim() && openTxn($("#tx-id").value.trim());
$("#tx-id").onkeydown = (e) => e.key === "Enter" && $("#tx-find").click();

// ---------- upload & webhook ----------
const drop = $("#drop");
drop.onclick = () => $("#file").click();
$("#file").onchange = () => $("#file").files[0] && upload($("#file").files[0]);
drop.ondragover = (e) => { e.preventDefault(); drop.classList.add("over"); };
drop.ondragleave = () => drop.classList.remove("over");
drop.ondrop = (e) => { e.preventDefault(); drop.classList.remove("over"); e.dataTransfer.files[0] && upload(e.dataTransfer.files[0]); };

function upload(file) {
  const out = $("#upload-result");
  out.innerHTML = `<p>Uploading <b>${esc(file.name)}</b>… <span id="up-pct">0%</span></p>`;
  const fd = new FormData();
  fd.append("file", file);
  const xhr = new XMLHttpRequest();
  xhr.open("POST", "/api/v1/upload");
  if (apiKey) xhr.setRequestHeader("X-API-Key", apiKey);
  xhr.upload.onprogress = (e) => { if (e.lengthComputable && $("#up-pct")) $("#up-pct").textContent = Math.round((e.loaded / e.total) * 100) + "% (analysing after upload)"; };
  xhr.onload = () => {
    let d = {};
    try { d = JSON.parse(xhr.responseText); } catch {}
    if (xhr.status !== 200) { out.innerHTML = `<p class="badge high">${esc(d.error || xhr.statusText)}</p>`; return; }
    const s = d.summary;
    out.innerHTML = `${kv([["Rows", num(s.received)], ["Accepted", num(s.accepted)], ["Duplicates", num(s.duplicates)], ["Invalid", num(s.invalid)],
        ["Flagged", num(s.flagged)], ["To block", num(s.high_risk)], ["To review", num(s.review)], ["Processing", `${s.elapsed_ms} ms`]])}
      ${Object.keys(s.signals_by_rule).length ? `<p class="section-gap">${Object.entries(s.signals_by_rule).sort((a, b) => b[1] - a[1]).map(([k, v]) => `<span class="chip">${esc(ruleName(k))} ${num(v)}</span>`).join("")}</p>` : ""}
      ${s.errors?.length ? `<details class="section-gap"><summary>${s.errors.length} row error(s)</summary><pre class="code">${esc(s.errors.map((e) => `row ${e.row}: ${e.error}`).join("\n"))}</pre></details>` : ""}
      <p><a href="#csps" id="go-csps">See CSP risk →</a></p>`;
    $("#go-csps").onclick = (e) => { e.preventDefault(); show("csps"); };
    loadFeed();
  };
  xhr.onerror = () => (out.innerHTML = `<p class="badge high">Upload failed</p>`);
  xhr.send(fd);
}

const example = {
  txn_id: "DEMO-" + Date.now().toString().slice(-6),
  timestamp: new Date().toISOString(),
  csp_id: "CSP0013",
  ko_id: "KO100013",
  from_account: "300000000001",
  txn_type: "Cash Withdrawal",
  amount: 5050,
  customer_charge: 0,
  channel: "ONUS",
  status: "SUCCESS",
};
$("#tx-json").value = JSON.stringify(example, null, 2);
$("#tx-send").onclick = async () => {
  const out = $("#tx-verdict");
  try {
    const t0 = performance.now();
    const v = await api("/api/v1/transactions", { method: "POST", headers: { "Content-Type": "application/json" }, body: $("#tx-json").value });
    $("#tx-lat").textContent = `round trip ${Math.round(performance.now() - t0)} ms · engine ${v.latency_us} µs`;
    out.innerHTML = `<div class="verdict"><div class="head"><span class="score">${v.risk_score}</span>${badge(v.risk_level, v.action)}${v.duplicate ? badge("info", "already seen") : ""}</div>
      ${v.signals.length ? v.signals.map(signalHTML).join("") : `<p class="muted">No rule fired.</p>`}</div>`;
    $$("#tx-verdict [data-txn]").forEach((a) => (a.onclick = (e) => { e.preventDefault(); openTxn(a.dataset.txn); }));
  } catch (e) {
    out.innerHTML = `<p class="badge high">${esc(e.message)}</p>`;
  }
};
$("#curl-example").textContent = `curl -X POST ${location.origin}/api/v1/transactions \\
  -H 'Content-Type: application/json'${apiKey ? " \\\n  -H 'X-API-Key: <key>'" : ""} \\
  -d '{"txn_id":"T1001","timestamp":"2026-10-27 14:05:00","csp_id":"CSP0042","ko_id":"KO100042",
       "from_account":"300012345678","to_account":"300087654321","txn_type":"Money Transfer",
       "amount":5050,"customer_charge":10,"channel":"ONUS","status":"SUCCESS"}'

# → {"txn_id":"T1001","risk_score":0.4,"risk_level":"low","action":"ALLOW","signals":[...],"latency_us":6.2}

# Batch / sheet upload
curl -F file=@transactions.csv ${location.origin}/api/v1/upload`;
$("#reset").onclick = async () => {
  if (!confirm("Delete all cached transactions and alerts? This cannot be undone.")) return;
  await api("/api/v1/reset", { method: "POST" });
  loaders[current] && loaders[current]();
};

// ---------- rules ----------
let rulesData = null;
async function loadRules() {
  if (!rulesData) rulesData = await api("/api/v1/rules");
  $("#rules-table").innerHTML = `<thead><tr><th>Rule</th><th>Id</th><th>Severity</th><th class="num">Weight</th><th>Stored as</th><th>Description</th></tr></thead><tbody>${
    rulesData.rules.map((r) => `<tr><td><b>${esc(r.name)}</b></td><td class="mono">${esc(r.id)}</td><td>${badge(r.severity)}</td><td class="num">${r.weight}</td><td>${r.tag_only ? "tag (score only)" : "alert"}</td><td>${esc(r.description)}</td></tr>`).join("")}</tbody>`;
  $("#config").textContent = JSON.stringify(rulesData.config, null, 2);
}
loaders.rules = loadRules;

// ---------- boot ----------
(async () => {
  try {
    rulesData = await api("/api/v1/rules");
    rulesData.rules.forEach((r) => (ruleNames[r.id] = r.name));
    $("#al-rule").innerHTML += rulesData.rules.filter((r) => !r.tag_only).map((r) => `<option value="${esc(r.id)}">${esc(r.name)}</option>`).join("");
  } catch (e) { console.error(e); }
  show(location.hash.slice(1) || "overview");
  loadFeed().catch(console.error);
  connect();
})();
addEventListener("hashchange", () => location.hash.slice(1) !== current && show(location.hash.slice(1)));
addEventListener("resize", debounce(() => current === "overview" && loadOverview(), 250));
