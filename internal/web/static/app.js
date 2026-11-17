"use strict";

const state = {
  apps: [],
  meta: {},
  filter: "all",
  query: "",
  selected: new Set(),
  job: null,
  events: [],
  progress: new Map(),
  source: null,
};

const $ = (sel) => document.querySelector(sel);

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null) node.setAttribute(k, v);
  }
  for (const c of children) {
    if (c === null || c === undefined) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
}

function chip(text, cls = "") {
  return el("span", { class: "chip " + cls, text });
}

function fmtBytes(n) {
  if (!n) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return n.toFixed(i === 0 ? 0 : 1) + " " + units[i];
}

function toast(message, kind = "") {
  const node = el("div", { class: "toast " + kind, text: message });
  $("#toasts").append(node);
  setTimeout(() => node.remove(), kind === "error" ? 7000 : 4000);
}

async function api(path, body) {
  const opts = { headers: {} };
  if (body !== undefined) {
    opts.method = "POST";
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  let data = {};
  try { data = await res.json(); } catch (_) { /* empty body */ }
  if (!res.ok) throw new Error(data.error || res.statusText || ("HTTP " + res.status));
  return data;
}

// ---- rendering ----

function visibleApps() {
  const q = state.query.trim().toLowerCase();
  return state.apps.filter((a) => {
    if (q && !(a.id + " " + a.repo + " " + (a.description || "")).toLowerCase().includes(q)) return false;
    switch (state.filter) {
      case "updates": return a.update_available;
      case "installed": return a.installed;
      case "missing": return !a.installed;
      default: return true;
    }
  });
}

function statusChip(a) {
  if (a.check_error) return chip("检查失败", "danger");
  if (a.pinned) return chip("已固定", "warn");
  if (!a.installed) return chip("未安装");
  if (a.update_available) return chip(a.comparable ? "可更新" : "版本不同", "warn");
  if (!a.latest_version) return chip("未检查");
  return chip("已是最新", "ok");
}

function versionCell(a) {
  if (!a.installed) return el("span", { class: "muted", text: "—" });
  const box = el("span", { class: "ver" }, a.current_version || "?");
  if (a.update_available && a.latest_tag) {
    box.append(
      el("span", { class: "arrow", text: "→" }),
      el("span", { class: "old", text: a.latest_tag }),
    );
  }
  return box;
}

function row(a) {
  const box = el("input", { type: "checkbox", "aria-label": "选择 " + a.id });
  box.checked = state.selected.has(a.id);
  box.addEventListener("change", () => {
    if (box.checked) state.selected.add(a.id); else state.selected.delete(a.id);
    node.classList.toggle("selected", box.checked);
    updateBulk();
  });

  const name = el("div", { class: "name" },
    el("strong", { text: a.id }),
    a.signature ? el("span", { class: "sig", text: " · 签名 " + a.signature }) : null,
    el("div", { class: "repo", text: a.repo, title: a.repo }),
  );

  const ops = el("div", { class: "ops" });
  if (!a.installed) {
    ops.append(el("button", { class: "primary", onclick: () => installIds([a.id]), text: "安装" }));
  } else {
    if (a.update_available) {
      ops.append(el("button", { class: "primary", onclick: () => installIds([a.id]), text: "升级" }));
    }
    ops.append(el("button", { class: "ghost", onclick: () => showDetail(a.id), text: "详情" }));
    ops.append(el("button", {
      class: "ghost",
      onclick: async () => {
        try {
          await api("/api/pin", { id: a.id, pinned: !a.pinned });
          toast((a.pinned ? "已取消固定 " : "已固定 ") + a.id);
          await load();
        } catch (e) { toast(e.message, "error"); }
      },
      text: a.pinned ? "取消固定" : "固定",
    }));
    ops.append(el("button", {
      class: "ghost danger",
      onclick: () => removeApp(a.id, false),
      text: "移除",
    }));
  }

  const node = el("div", { class: "row" + (state.selected.has(a.id) ? " selected" : ""), "data-id": a.id },
    box,
    name,
    versionCell(a),
    el("div", { class: "status" }, statusChip(a)),
    el("div", { class: "muted", text: a.channel && a.channel !== "latest" ? a.channel : "" }),
    ops,
  );
  return node;
}

function render() {
  const list = $("#list");
  list.replaceChildren();
  const apps = visibleApps();
  for (const a of apps) list.append(row(a));
  const empty = $("#empty");
  if (apps.length === 0) {
    empty.hidden = false;
    empty.textContent = state.apps.length === 0
      ? "还没有配置应用。点击右上角“添加应用”，输入 GitHub 仓库即可。"
      : "没有符合当前筛选条件的应用。";
  } else {
    empty.hidden = true;
  }
  updateBulk();
}

function updateBulk() {
  const any = state.selected.size > 0;
  $("#btn-install-sel").disabled = !any;
  $("#btn-update-sel").disabled = !any;
}

async function load() {
  try {
    const data = await api("/api/apps");
    state.apps = data.apps || [];
    state.meta = data;
    const known = new Set(state.apps.map((a) => a.id));
    state.selected = new Set([...state.selected].filter((id) => known.has(id)));
    $("#platform").textContent = data.platform + " · v" + data.version;
    $("#config-path").textContent = data.config;
    const token = $("#token");
    token.hidden = false;
    token.textContent = data.token ? "已认证 GITHUB_TOKEN" : "未设置 GITHUB_TOKEN";
    token.className = "chip " + (data.token ? "ok" : "warn");
    token.title = data.token ? "API 配额 5000/小时" : "API 配额 60/小时；设置 GITHUB_TOKEN 可提升";
    render();
  } catch (e) {
    toast("加载失败：" + e.message, "error");
  }
}

// ---- actions ----

function installIds(ids) {
  if (!ids.length) return;
  startJob("/api/install", { ids });
}

function startJob(path, body) {
  (async () => {
    try {
      const job = await api(path, body);
      follow(job.id, job.kind);
    } catch (e) {
      toast(e.message, "error");
    }
  })();
}

function jobTitle(kind) {
  return { check: "检查更新", install: "安装 / 升级", update: "升级", add: "添加应用" }[kind] || "任务";
}

function resetDrawer(kind) {
  state.events = [];
  state.progress.clear();
  state.job = null;
  $("#drawer").hidden = false;
  $("#job-title").textContent = jobTitle(kind);
  $("#job-status").textContent = "进行中";
  $("#job-status").className = "chip warn";
  $("#progress-list").replaceChildren();
  const log = $("#log");
  log.textContent = "";
  log.hidden = false;
  renderProgress();
}

function closeSource() {
  if (state.source) { state.source.close(); state.source = null; }
}

function follow(id, kind) {
  closeSource();
  resetDrawer(kind);
  let finished = false;
  const done = (job) => {
    if (finished) return;
    finished = true;
    closeSource();
    state.job = job;
    const failed = job.status === "failed";
    $("#job-status").textContent = failed ? "失败" : "已完成";
    $("#job-status").className = "chip " + (failed ? "danger" : "ok");
    if (failed && job.error) toast("任务失败：" + job.error, "error");
    summarize(job);
    load();
  };

  const source = new EventSource("/api/jobs/" + encodeURIComponent(id) + "/events");
  state.source = source;
  source.onmessage = (msg) => {
    let data;
    try { data = JSON.parse(msg.data); } catch (_) { return; }
    if (data.type === "snapshot") {
      if (data.job && data.job.kind) $("#job-title").textContent = jobTitle(data.job.kind);
    } else if (data.type === "event" && data.event) {
      applyEvent(data.event);
    } else if (data.type === "done" && data.job) {
      done(data.job);
    }
  };
  source.onerror = () => {
    closeSource();
    pollJob(id, done);
  };
}

async function pollJob(id, done) {
  for (let i = 0; i < 3600; i++) {
    try {
      const job = await api("/api/jobs/" + encodeURIComponent(id));
      for (const ev of job.events || []) applyEvent(ev, true);
      if (job.status !== "running") { done(job); return; }
    } catch (e) {
      toast("丢失任务状态：" + e.message, "error");
      return;
    }
    await new Promise((r) => setTimeout(r, 800));
  }
}

function applyEvent(ev, silent = false) {
  const key = ev.id || "_";
  let p = state.progress.get(key);
  if (!p) { p = { id: key, text: "", percent: 0, state: "" }; state.progress.set(key, p); }
  if (ev.percent !== undefined && ev.percent >= 0) p.percent = ev.percent;
  switch (ev.kind) {
    case "resolve": p.text = "解析 release…"; break;
    case "install": p.text = "安装中…"; break;
    case "download":
      if (ev.percent > 0) {
        p.percent = ev.percent;
        p.text = ev.speed ? fmtBytes(ev.speed) + "/s" : "下载中…";
      } else {
        p.text = "下载中…";
      }
      break;
    case "done":
      p.percent = 100; p.state = "done";
      p.text = ev.message ? "✓ " + ev.message : "完成";
      break;
    case "error":
      p.state = "error";
      p.text = "✗ " + (ev.error || ev.message || "失败");
      break;
    case "check":
      if (ev.error) { p.state = "error"; p.text = "✗ " + ev.error; }
      else if (ev.message && !ev.message.startsWith("checking")) { p.text = ev.message; }
      else { p.text = "检查中…"; }
      break;
  }
  const log = $("#log");
  if (!silent) {
    const parts = [new Date().toLocaleTimeString(), ev.kind];
    if (ev.id) parts.push(ev.id);
    if (ev.message) parts.push(ev.message);
    if (ev.error) parts.push(ev.error);
    if (ev.percent > 0 && ev.kind === "download") parts.push(ev.percent.toFixed(0) + "%");
    log.textContent += parts.join("  ") + "\n";
    if (log.textContent.length > 120000) log.textContent = log.textContent.slice(-60000);
    log.scrollTop = log.scrollHeight;
  }
  renderProgress();
}

function renderProgress() {
  const list = $("#progress-list");
  list.replaceChildren();
  const items = [...state.progress.values()].sort((a, b) => a.id.localeCompare(b.id));
  for (const p of items) {
    const pct = Math.max(0, Math.min(100, p.percent || 0));
    const bar = el("div", { class: "bar" }, el("i"));
    bar.firstChild.style.width = pct + "%";
    const label = el("div", { class: "label", text: p.id + (p.text ? " · " + p.text : ""), title: p.id + " " + (p.text || "") });
    if (p.state === "error") label.style.color = "var(--danger)";
    const mark = p.state === "done" ? "✓" : p.state === "error" ? "✗" : (p.percent > 0 ? pct.toFixed(0) + "%" : "");
    list.append(el("div", { class: "progress-item " + p.state },
      label,
      bar,
      el("div", { class: "pct", text: mark }),
    ));
  }
}

function summarize(job) {
  const result = job.result;
  if (!result) { if (job.kind === "check") load(); return; }
  if (Array.isArray(result) && job.kind === "check") {
    const updates = result.filter((r) => r.update_available && !r.pinned && !r.error).length;
    const errors = result.filter((r) => r.error).length;
    toast("检查完成：" + updates + " 个可用更新" + (errors ? "，" + errors + " 个失败" : ""), errors ? "error" : "ok");
  } else if (Array.isArray(result)) {
    const errors = result.filter((r) => r.error);
    toast(errors.length ? "完成，" + errors.length + " 个失败" : "完成：" + result.length + " 个应用", errors.length ? "error" : "ok");
  } else if (result.installed !== undefined) {
    const n = (result.installed || []).length;
    const errors = (result.installed || []).filter((r) => r.error).length;
    toast("已升级 " + (n - errors) + " 个应用" + (errors ? "，" + errors + " 个失败" : ""), errors ? "error" : "ok");
  } else if (result.id) {
    toast("已添加 " + result.id, "ok");
  }
}

async function removeApp(id, purge) {
  const ok = confirm(purge
    ? "删除 " + id + " 的安装文件并从配置中移除？"
    : "删除 " + id + " 的安装文件和软链（保留配置）？");
  if (!ok) return;
  try {
    await api("/api/remove", { id, purge });
    toast("已移除 " + id);
    $("#detail-dialog").close();
    await load();
  } catch (e) { toast(e.message, "error"); }
}

function showDetail(id) {
  const a = state.apps.find((x) => x.id === id);
  if (!a) return;
  $("#detail-title").textContent = a.id;
  const body = $("#detail-body");
  body.replaceChildren();
  const dl = el("dl");
  const add = (k, v) => { dl.append(el("dt", { text: k }), el("dd", { text: v || "—" })); };
  add("仓库", a.repo);
  add("频道", a.channel || "latest");
  add("已安装", a.current_version || "未安装");
  add("最新", a.latest_tag || a.latest_version || "—");
  add("软链", a.link_path);
  if (a.signature) add("签名", a.signature);
  body.append(dl);

  const versions = a.versions || [];
  if (versions.length) {
    body.append(el("h3", { text: "已保留版本" }));
    const ul = el("ul", { class: "version-list" });
    for (const v of [...versions].reverse()) {
      const current = v === a.current_version;
      ul.append(el("li", { class: current ? "current" : "" },
        el("span", { class: "ver", text: v }),
        el("span", { class: "spacer" }),
        current ? chip("当前", "ok") : el("button", {
          class: "ghost",
          onclick: async () => {
            try {
              await api("/api/rollback", { id: a.id, version: v });
              toast("已回滚 " + a.id + " → " + v, "ok");
              $("#detail-dialog").close();
              await load();
            } catch (e) { toast(e.message, "error"); }
          },
          text: "回滚",
        }),
      ));
    }
    body.append(ul);
  }
  $("#detail-purge").onclick = () => removeApp(a.id, true);
  $("#detail-dialog").showModal();
}

// ---- wiring ----

function wire() {
  $("#search").addEventListener("input", (e) => { state.query = e.target.value; render(); });
  document.querySelectorAll(".filters button").forEach((btn) => {
    btn.addEventListener("click", () => {
      document.querySelectorAll(".filters button").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      state.filter = btn.dataset.filter;
      render();
    });
  });
  $("#btn-check").addEventListener("click", () => startJob("/api/check", {}));
  $("#btn-add").addEventListener("click", () => {
    $("#add-form").reset();
    $("#add-install").checked = true;
    $("#add-dialog").showModal();
    $("#add-repo").focus();
  });
  $("#add-cancel").addEventListener("click", () => $("#add-dialog").close());
  $("#add-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const repo = $("#add-repo").value.trim();
    if (!repo) return;
    const body = {
      repo,
      id: $("#add-id").value.trim(),
      channel: $("#add-channel").value,
      install: $("#add-install").checked,
    };
    $("#add-dialog").close();
    startJob("/api/add", body);
  });
  $("#btn-install-sel").addEventListener("click", () => installIds([...state.selected]));
  $("#btn-update-sel").addEventListener("click", () => {
    const ids = [...state.selected];
    if (ids.length) startJob("/api/update", { ids });
  });
  $("#drawer-close").addEventListener("click", () => { $("#drawer").hidden = true; });
  $("#detail-close").addEventListener("click", () => $("#detail-dialog").close());
  document.addEventListener("keydown", (e) => {
    if (e.key === "/" && document.activeElement !== $("#search")) {
      e.preventDefault();
      $("#search").focus();
    }
    if (e.key === "Escape") {
      if ($("#add-dialog").open) $("#add-dialog").close();
      if ($("#detail-dialog").open) $("#detail-dialog").close();
    }
  });
}

wire();
load();
