"use strict";

// Isolated design prototype: all state is fictional and held in memory. No APIs.
const $ = (id) => document.getElementById(id);
const escapeHTML = (value) =>
  String(value).replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const presets = [
  {
    name: "上游余额不足",
    description: "隐藏余额与采购账户信息，返回统一提示。",
    statuses: "200, 402, 403, 429",
    codes: "insufficient_quota, insufficient_balance",
    keywords: "余额不足\ninsufficient balance\ncredit balance is too low",
    action: "override",
    returnStatus: 503,
    message: "上游服务暂时不可用，请联系管理员。",
  },
  {
    name: "上游认证失效",
    description: "隔离上游认证错误，避免暴露凭证信息。",
    statuses: "401, 403",
    codes: "invalid_api_key, authentication_error",
    keywords: "invalid api key\n认证失败",
    action: "override",
    returnStatus: 502,
    message: "上游服务认证异常，请联系管理员。",
  },
  {
    name: "上游请求限流",
    description: "保留限流状态，使用统一的公开文案。",
    statuses: "429",
    codes: "rate_limit_exceeded",
    keywords: "rate limit\n请求过于频繁",
    action: "preserve",
    returnStatus: 429,
    message: "上游请求繁忙，请稍后再试。",
  },
  {
    name: "上游服务不可用",
    description: "按状态范围匹配，无需填写错误关键词。",
    statuses: "500-599",
    codes: "",
    keywords: "",
    action: "override",
    returnStatus: 503,
    message: "上游服务暂时不可用，请稍后再试。",
  },
];
const ruleFromPreset = (index, id) => ({
  ...presets[index],
  id,
  scope: "all",
  channels: [],
  protocol: "all",
  sources: ["HTTP", "JSON", "SSE"],
  ignoreCase: true,
  enabled: true,
});
let rules = [
  ruleFromPreset(0, "balance"),
  ruleFromPreset(1, "auth"),
  ruleFromPreset(2, "rate"),
  { ...ruleFromPreset(3, "unavailable"), enabled: false },
];
let published = structuredClone(rules);
let version = 12;
let editedID = null;
let deleteID = null;
let toastTimer;
let syncTimer;
const now = new Date();
const logSeed = [
  [
    3,
    "Vendor A",
    "OpenAI Chat",
    "上游余额不足",
    "balance",
    402,
    503,
    503,
    "gateway-a",
    "HTTP",
    "A · 错误代码",
    "余额或额度不足",
  ],
  [
    18,
    "Sub2API",
    "Anthropic Messages",
    "上游余额不足",
    "balance",
    200,
    503,
    200,
    "gateway-b",
    "SSE",
    "B · 错误关键词",
    "余额或额度不足",
  ],
  [
    43,
    "Kiro2CC",
    "OpenAI Chat",
    "上游请求限流",
    "rate",
    429,
    429,
    429,
    "gateway-a",
    "HTTP",
    "A · 错误代码",
    "上游限流",
  ],
  [
    85,
    "Vendor A",
    "Gemini",
    "上游认证失效",
    "auth",
    401,
    502,
    502,
    "gateway-b",
    "HTTP",
    "B · 错误关键词",
    "上游认证失败",
  ],
  [
    140,
    "Sub2API",
    "Responses",
    "通用安全兜底",
    "fallback",
    502,
    502,
    502,
    "gateway-a",
    "HTTP",
    "—",
    "未命中自定义规则",
  ],
  [
    260,
    "Vendor A",
    "OpenAI Chat",
    "上游余额不足",
    "balance",
    200,
    503,
    503,
    "gateway-a",
    "JSON",
    "A · 错误代码",
    "余额或额度不足",
  ],
  [
    4100,
    "Kiro2CC",
    "OpenAI Chat",
    "通用安全兜底",
    "fallback",
    503,
    503,
    503,
    "gateway-b",
    "HTTP",
    "—",
    "未命中自定义规则",
  ],
  [
    90000,
    "Sub2API",
    "Anthropic Messages",
    "上游余额不足",
    "balance",
    403,
    503,
    503,
    "gateway-b",
    "HTTP",
    "B · 错误关键词",
    "余额或额度不足",
  ],
];
const logs = logSeed.map((row, i) => {
  const [
    age,
    channel,
    protocol,
    rule,
    ruleID,
    upstream,
    desired,
    actual,
    node,
    source,
    group,
    reason,
  ] = row;
  return {
    id: `demo-req-${String(i + 1).padStart(4, "0")}`,
    time: new Date(now - age * 1000),
    age,
    channel,
    protocol,
    rule,
    ruleID,
    upstream,
    desired,
    actual,
    node,
    source,
    group,
    reason,
    version: 12,
  };
});
function toast(message) {
  clearTimeout(toastTimer);
  $("toast").textContent = message;
  $("toast").hidden = false;
  toastTimer = setTimeout(() => ($("toast").hidden = true), 3500);
}
function dirty() {
  return JSON.stringify(rules) !== JSON.stringify(published);
}
function updateDraft() {
  const changed = dirty();
  $("draft-state").textContent = changed ? "有修改待发布" : "与已发布一致";
  $("draft-state").className = changed ? "green" : "";
  $("publish").disabled = !changed;
  $("version").textContent = `V${version}`;
  $("rule-count").textContent = rules.length;
}
function sortedRules() {
  return rules
    .filter((r) => r.scope === "selected")
    .concat(rules.filter((r) => r.scope === "all"));
}
function renderRules() {
  const query = $("rule-search").value.trim().toLowerCase();
  const state = $("rule-state").value;
  const ordered = sortedRules();
  const filtered = ordered.filter(
    (r) =>
      `${r.name} ${r.keywords} ${r.codes}`.toLowerCase().includes(query) &&
      (!state || r.enabled === (state === "enabled")),
  );
  $("rule-summary").textContent =
    `${filtered.length} 条规则 · ${rules.filter((r) => r.enabled).length} 条启用`;
  $("rules-body").innerHTML =
    filtered
      .map((r) => {
        const group = ordered.filter((other) => other.scope === r.scope);
        const i = group.findIndex((other) => other.id === r.id);
        const summary =
          [r.codes, r.keywords.split("\n").join(" / ")]
            .filter(Boolean)
            .join(" 或 ") || "按状态匹配，无内容条件";
        return `<tr><td><div class="order-buttons"><span>${i + 1}</span><button class="quiet" data-action="up" data-id="${escapeHTML(r.id)}" aria-label="上移${escapeHTML(r.name)}" ${i === 0 ? "disabled" : ""}>↑</button><button class="quiet" data-action="down" data-id="${escapeHTML(r.id)}" aria-label="下移${escapeHTML(r.name)}" ${i === group.length - 1 ? "disabled" : ""}>↓</button></div></td><td><strong>${escapeHTML(r.name)}</strong><span class="sub" title="${escapeHTML(summary)}">${escapeHTML(summary)}</span></td><td><span class="badge ${r.scope === "all" ? "neutral" : "blue"}">${r.scope === "all" ? "公共规则" : escapeHTML(r.channels.join("、"))}</span><span class="sub">${r.protocol === "all" ? "全部协议" : escapeHTML(r.protocol)}</span></td><td><span class="mono">${escapeHTML(r.statuses || "所有已识别错误")}</span></td><td>${r.action === "preserve" ? '<span class="muted">保留原状态</span>' : `<span class="status">${r.returnStatus}</span>`}</td><td><button class="switch" role="switch" aria-checked="${r.enabled}" aria-label="启用${escapeHTML(r.name)}" data-action="toggle" data-id="${escapeHTML(r.id)}"></button></td><td class="align-right"><button class="quiet" data-action="edit" data-id="${escapeHTML(r.id)}">编辑</button><button class="quiet danger" data-action="delete" data-id="${escapeHTML(r.id)}">删除</button></td></tr>`;
      })
      .join("") ||
    '<tr><td colspan="7" class="empty">没有符合条件的规则，请调整筛选条件。</td></tr>';
  updateDraft();
}
$("rules-body").addEventListener("click", (event) => {
  const button = event.target.closest("button[data-action]");
  if (!button) return;
  const r = rules.find((r) => r.id === button.dataset.id);
  if (!r) return;
  const action = button.dataset.action;
  if (action === "edit") {
    openEditor(r);
    return;
  }
  if (action === "delete") {
    deleteID = r.id;
    $("delete-description").textContent = `将从草稿中移除「${r.name}」。`;
    $("delete-dialog").showModal();
    return;
  }
  if (action === "toggle") {
    r.enabled = !r.enabled;
    toast("启用状态已更新到草稿，发布后生效。");
  }
  if (action === "up" || action === "down") {
    const group = rules.filter((other) => other.scope === r.scope);
    const current = group.indexOf(r);
    const other = group[current + (action === "up" ? -1 : 1)];
    if (other) {
      const a = rules.indexOf(r),
        b = rules.indexOf(other);
      [rules[a], rules[b]] = [rules[b], rules[a]];
    }
  }
  renderRules();
});
$("confirm-delete").onclick = () => {
  rules = rules.filter((r) => r.id !== deleteID);
  $("delete-dialog").close();
  renderRules();
  toast("已从草稿删除，历史日志仍然保留。");
};
for (const id of ["rule-search", "rule-state"])
  $(id).addEventListener("input", renderRules);
document.querySelectorAll("[data-page]").forEach(
  (button) =>
    (button.onclick = () => {
      document.querySelectorAll("[data-page]").forEach((tab) => {
        tab.classList.toggle("active", tab === button);
        if (tab === button) tab.setAttribute("aria-current", "page");
        else tab.removeAttribute("aria-current");
      });
      $("rules-page").hidden = button.dataset.page !== "rules";
      $("logs-page").hidden = button.dataset.page !== "logs";
    }),
);
document
  .querySelectorAll(".close")
  .forEach(
    (button) => (button.onclick = () => button.closest("dialog").close()),
  );
document.querySelectorAll("dialog").forEach((dialog) =>
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) {
      const r = dialog.getBoundingClientRect();
      if (
        event.clientX < r.left ||
        event.clientX > r.right ||
        event.clientY < r.top ||
        event.clientY > r.bottom
      )
        dialog.close();
    }
  }),
);
function setEditorTab(tab) {
  document
    .querySelectorAll("[data-editor-tab]")
    .forEach((b) => b.classList.toggle("active", b.dataset.editorTab === tab));
  $("configure-pane").hidden = tab !== "configure";
  $("test-pane").hidden = tab !== "test";
}
document
  .querySelectorAll("[data-editor-tab]")
  .forEach((b) => (b.onclick = () => setEditorTab(b.dataset.editorTab)));
function syncEditor() {
  $("channel-options").hidden = $("edit-scope").value !== "selected";
  $("edit-return").disabled = $("edit-action").value !== "override";
}
$("edit-scope").onchange = syncEditor;
$("edit-action").onchange = syncEditor;
function openEditor(rule) {
  editedID = rule.id || null;
  $("editor-title").textContent = editedID ? "编辑规则" : "新增规则";
  for (const [id, key] of [
    ["edit-name", "name"],
    ["edit-scope", "scope"],
    ["edit-protocol", "protocol"],
    ["edit-status", "statuses"],
    ["edit-codes", "codes"],
    ["edit-keywords", "keywords"],
    ["edit-action", "action"],
    ["edit-return", "returnStatus"],
    ["edit-message", "message"],
  ])
    $(id).value = rule[key];
  $("edit-ignore-case").checked = rule.ignoreCase;
  $("edit-enabled").checked = rule.enabled;
  document
    .querySelectorAll('[name="source"]')
    .forEach((input) => (input.checked = rule.sources.includes(input.value)));
  document
    .querySelectorAll('[name="channel"]')
    .forEach((input) => (input.checked = rule.channels.includes(input.value)));
  $("conditions").open = true;
  $("form-error").hidden = true;
  $("test-result").hidden = true;
  syncEditor();
  setEditorTab("configure");
  $("sample-preset").value = "balance";
  setSample();
  $("editor").showModal();
  $("editor").querySelector(".drawer-body").scrollTop = 0;
}
$("new-rule").onclick = () =>
  openEditor({
    ...ruleFromPreset(0, null),
    name: "",
    statuses: "",
    codes: "",
    keywords: "",
    action: "preserve",
  });
$("presets").onclick = () => $("preset-dialog").showModal();
$("preset-list").innerHTML = presets
  .map(
    (p, i) =>
      `<button class="preset-card" data-preset="${i}"><div><strong>${escapeHTML(p.name)}</strong><p>${escapeHTML(p.description)}</p></div><span>＋</span></button>`,
  )
  .join("");
$("preset-list").onclick = (event) => {
  const button = event.target.closest("[data-preset]");
  if (button) {
    $("preset-dialog").close();
    openEditor(ruleFromPreset(Number(button.dataset.preset), null));
  }
};
function readRule() {
  return {
    id: editedID || `custom-${Date.now()}`,
    name: $("edit-name").value.trim(),
    scope: $("edit-scope").value,
    channels: [...document.querySelectorAll('[name="channel"]:checked')].map(
      (e) => e.value,
    ),
    protocol: $("edit-protocol").value,
    sources: [...document.querySelectorAll('[name="source"]:checked')].map(
      (e) => e.value,
    ),
    statuses: $("edit-status").value.trim(),
    codes: $("edit-codes").value.trim(),
    keywords: $("edit-keywords").value.trim(),
    ignoreCase: $("edit-ignore-case").checked,
    action: $("edit-action").value,
    returnStatus: Number($("edit-return").value),
    message: $("edit-message").value.trim(),
    enabled: $("edit-enabled").checked,
  };
}
function statusRanges(value) {
  if (!value.trim()) return [];
  return value.split(/[,，]/).map((v) => {
    const match = v.trim().match(/^(\d{3})(?:\s*[-–]\s*(\d{3}))?$/);
    if (!match) throw Error("上游状态格式不正确，请使用 402, 403 或 400-499。");
    const start = Number(match[1]),
      end = Number(match[2] || match[1]);
    if (start < 100 || end > 599 || start > end)
      throw Error("上游状态范围必须在 100–599 之间，起始值不能大于结束值。");
    return [start, end];
  });
}
function validateRule(r) {
  if (!r.name) throw Error("请填写规则名称。");
  if (!r.message) throw Error("请填写对外公开文案。");
  if (!r.sources.length) throw Error("请至少选择一种错误来源。");
  if (r.scope === "selected" && !r.channels.length)
    throw Error("请至少选择一个适用渠道。");
  statusRanges(r.statuses);
  if (
    r.action === "override" &&
    (!Number.isInteger(r.returnStatus) ||
      r.returnStatus < 400 ||
      r.returnStatus > 599)
  )
    throw Error("返回 HTTP 状态必须是 400–599 之间的整数。");
}
$("rule-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const rule = readRule();
  try {
    validateRule(rule);
  } catch (error) {
    setEditorTab("configure");
    $("form-error").textContent = error.message;
    $("form-error").hidden = false;
    $("form-error").scrollIntoView({ block: "nearest" });
    return;
  }
  const i = rules.findIndex((r) => r.id === editedID);
  if (i < 0) rules.push(rule);
  else rules[i] = rule;
  $("editor").close();
  renderRules();
  toast("已保存到本页草稿，点击「预览发布」查看生效流程。");
});
// Don't leave a stale sample verdict visible after changing inputs.
$("rule-form").addEventListener("input", () => {
  $("test-result").hidden = true;
  $("form-error").hidden = true;
});
function setSample() {
  const kind = $("sample-preset").value;
  $("sample-status").value = kind === "balance" ? 402 : 200;
  $("sample-source").value =
    kind === "balance" ? "HTTP" : kind === "sse" ? "SSE" : "normal";
  $("sample-code").value = kind === "normal" ? "" : "insufficient_quota";
  $("sample-message").value =
    kind === "normal"
      ? "如果余额不足，你可以在账户设置中查看充值方式。"
      : "Your credit balance is too low.";
  $("sample-committed").checked = kind === "sse";
  $("test-result").hidden = true;
}
$("sample-preset").onchange = setSample;
$("sample-source").onchange = () => {
  if ($("sample-source").value !== "SSE") $("sample-committed").checked = false;
};
$("run-test").onclick = () => {
  const r = readRule(),
    result = $("test-result");
  result.hidden = false;
  try {
    validateRule(r);
  } catch (error) {
    result.innerHTML = `<h3>请先完善规则</h3><p>${escapeHTML(error.message)}</p>`;
    return;
  }
  const status = Number($("sample-status").value),
    source = $("sample-source").value,
    committed = $("sample-committed").checked;
  if (!Number.isInteger(status) || status < 100 || status > 599) {
    result.innerHTML =
      "<h3>样本状态无效</h3><p>请填写 100–599 之间的整数。</p>";
    return;
  }
  if (
    (source === "JSON" && status !== 200) ||
    (source === "HTTP" && status === 200) ||
    (committed && (source !== "SSE" || status !== 200))
  ) {
    result.innerHTML =
      "<h3>样本场景不一致</h3><p>200 内嵌错误请选择 JSON 或 SSE；本预览的已开始 SSE 场景需要上游 HTTP 200。</p>";
    return;
  }
  if (source === "normal") {
    result.innerHTML =
      "<h3>正常回答，不参与错误匹配</h3><p>即使正文出现「余额不足」等关键词，也按正常回答转发，不产生拦截日志。</p>";
    return;
  }
  const ranges = statusRanges(r.statuses),
    codes = r.codes
      .split(/[,，\n]/)
      .map((s) => s.trim())
      .filter(Boolean),
    keywords = r.keywords
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean);
  const normalize = (s) => (r.ignoreCase ? s.toLowerCase() : s);
  const codeMatch = codes.some(
    (code) => normalize(code) === normalize($("sample-code").value),
  );
  const keywordMatch = keywords.some((word) =>
    normalize($("sample-message").value).includes(normalize(word)),
  );
  const checks = [
    [r.enabled, "规则已停用"],
    [
      r.scope === "all" || r.channels.includes($("sample-channel").value),
      "渠道不在适用范围",
    ],
    [
      r.protocol === "all" || r.protocol === $("sample-protocol").value,
      "协议不在适用范围",
    ],
    [r.sources.includes(source), "错误来源不匹配"],
    [
      !ranges.length || ranges.some(([a, b]) => status >= a && status <= b),
      "上游 HTTP 状态不匹配",
    ],
    [
      (!codes.length && !keywords.length) || codeMatch || keywordMatch,
      "错误内容不匹配",
    ],
  ];
  const failure = checks.find(([ok]) => !ok),
    matched = !failure;
  const preserved = status >= 400 && status <= 599 ? status : 502;
  const desired =
    matched && r.action === "override" ? r.returnStatus : preserved;
  const actual = committed ? 200 : desired;
  result.innerHTML = `<h3>${matched ? "命中当前规则" : "未命中当前规则"}</h3><p>${matched ? `命中${codeMatch ? "条件组 A · 错误代码" : keywordMatch ? "条件组 B · 错误关键词" : "状态条件"}。` : escapeHTML(failure[1]) + "。在仅测试当前规则的演示中，将执行通用安全兜底。"}</p><p>上游 <b>${status}</b> → 拟返回 <b>${desired}</b> → 实际 HTTP <b>${actual}</b></p>${committed ? '<div class="callout warning">HTTP 200 已提交，状态改写未执行。发送安全错误事件并结束流。</div>' : ""}<p class="muted">${committed ? "安全错误事件中的公开文案" : "对外公开文案"}</p><pre>${escapeHTML(matched ? r.message : "上游请求失败，请联系管理员。")}</pre><p class="muted">本次仅测试当前表单，不代表完整规则链或实际协议解析结果。</p>`;
};
$("publish").onclick = () => {
  const added = rules.filter(
    (r) => !published.some((old) => old.id === r.id),
  ).length;
  const removed = published.filter(
    (old) => !rules.some((r) => r.id === old.id),
  ).length;
  const edited = rules.filter((r) =>
    published.some(
      (old) => old.id === r.id && JSON.stringify(old) !== JSON.stringify(r),
    ),
  ).length;
  const reordered =
    rules
      .filter((r) => published.some((old) => old.id === r.id))
      .map((r) => r.id)
      .join() !==
    published
      .filter((old) => rules.some((r) => r.id === old.id))
      .map((r) => r.id)
      .join();
  $("publish-summary").innerHTML =
    `<h3>V${version} → V${version + 1}</h3><dl class="definition"><dt>规则总数</dt><dd>${rules.length} 条，其中 ${rules.filter((r) => r.enabled).length} 条启用</dd><dt>草稿变更</dt><dd>新增 ${added} · 修改 ${edited} · 删除 ${removed}${reordered ? " · 已调整顺序" : ""}</dd><dt>生效方式</dt><dd>节点同步后，新请求使用新规则</dd><dt>正在进行的请求</dt><dd>继续使用原版本，不中断 SSE</dd></dl>`;
  $("publish-dialog").showModal();
};
$("confirm-publish").onclick = () => {
  version++;
  published = structuredClone(rules);
  updateDraft();
  $("publish-dialog").close();
  $("sync-state").textContent = "0 / 2 等待同步";
  clearTimeout(syncTimer);
  syncTimer = setTimeout(() => {
    $("sync-state").textContent = "2 / 2 已同步";
    toast(`演示节点已同步到 V${version}；历史日志仍保留 V12。`);
  }, 1800);
  toast(`已模拟发布 V${version}，等待示例节点同步。`);
};
function timeLabel(time) {
  return time.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}
function renderLogs() {
  const search = $("log-search").value.trim().toLowerCase(),
    channel = $("log-channel").value,
    status = $("log-status").value,
    node = $("log-node").value,
    hours = Number($("log-time").value);
  const found = logs.filter(
    (log) =>
      (!search || `${log.id} ${log.rule}`.toLowerCase().includes(search)) &&
      (!channel || log.channel === channel) &&
      (!status || String(log.actual) === status) &&
      (!node || log.node === node) &&
      log.age <= hours * 3600,
  );
  $("logs-body").innerHTML =
    found
      .map(
        (log) =>
          `<tr><td><span>${timeLabel(log.time)}</span><span class="sub mono">${log.id}</span></td><td><strong>${log.channel}</strong><span class="sub">${log.protocol}</span></td><td>${log.rule}<span class="sub">${log.source === "HTTP" ? "HTTP 错误" : log.source === "SSE" ? "SSE 错误事件" : "200 JSON 错误"}</span></td><td><span class="status">${log.upstream}</span> <span class="muted">→</span> <span class="status">${log.actual}</span>${log.source === "SSE" ? '<span class="sub"><span class="badge amber">流内失败 · 状态已提交</span></span>' : ""}</td><td><span class="mono">${log.node}</span><span class="sub">V${log.version}</span></td><td class="align-right"><button class="quiet" data-log="${log.id}">详情</button></td></tr>`,
      )
      .join("") ||
    '<tr><td colspan="6" class="empty">当前筛选下没有记录。请调整时间范围或筛选条件。</td></tr>';
  $("log-count").textContent =
    `本页 ${found.length} 条示例记录 · 每页最多 50 条`;
}
for (const id of [
  "log-search",
  "log-channel",
  "log-status",
  "log-node",
  "log-time",
])
  $(id).addEventListener("input", renderLogs);
$("logs-body").onclick = (event) => {
  const button = event.target.closest("[data-log]");
  if (!button) return;
  const log = logs.find((l) => l.id === button.dataset.log);
  const fields = [
    ["发生时间", timeLabel(log.time)],
    ["请求编号", log.id],
    ["渠道 / 协议", `${log.channel} / ${log.protocol}`],
    ["节点 / 进程实例", `${log.node} / demo-01`],
    ["配置版本", `V${log.version}`],
    ["命中规则", `${log.rule} (${log.ruleID})`],
    ["匹配条件组", log.group],
    ["固定原因分类", log.reason],
    ["错误来源", log.source],
    ["上游 HTTP 状态", log.upstream],
    ["拟返回 HTTP 状态", log.desired],
    ["实际 HTTP 状态", log.actual],
  ];
  $("detail-content").innerHTML =
    `<div class="detail-result"><span class="badge good">已拦截</span><h3 style="margin-top:10px">${log.source === "SSE" ? "已发送安全错误事件并结束流" : "已替换为安全错误响应"}</h3><p>上游原始错误未向下游透传。</p></div>${log.source === "SSE" ? '<div class="callout warning"><strong>HTTP 状态改写未执行</strong>发生错误前已经提交 HTTP 200，因此实际状态保持 200。这条请求属于流内失败，不是成功响应。</div>' : ""}<dl class="definition">${fields.map(([label, value]) => `<dt>${escapeHTML(label)}</dt><dd>${escapeHTML(value)}</dd>`).join("")}</dl><details class="original-error" open><summary><strong>上游原始错误</strong> <span class="badge neutral">仅管理员可见</span></summary><p class="muted">示例：完整保留 · 凭证已脱敏 · 最多 2 KiB</p><pre>${escapeHTML(JSON.stringify({ error: { code: log.ruleID === "auth" ? "invalid_api_key" : log.ruleID === "rate" ? "rate_limit_exceeded" : "insufficient_quota", message: log.ruleID === "auth" ? "Invalid API key. Please check your credentials." : log.ruleID === "rate" ? "Rate limit exceeded." : "Your credit balance is too low.", api_key: "[REDACTED]" } }, null, 2))}</pre><p class="muted">对照原始错误与命中规则排查误拦。原文加密保存；长正文或读取不完整时标记截断。不会向下游返回。</p></details><p class="muted">此详情是静态示例，不代表真实请求的执行结果。</p>`;
  $("detail-dialog").showModal();
};
$("resources").onclick = () => $("resource-dialog").showModal();
$("degrade").onchange = () => {
  const degraded = $("degrade").checked;
  $("degraded-banner").hidden = !degraded;
  document.querySelector(".log-health").classList.toggle("degraded", degraded);
  $("health-title").textContent = degraded ? "日志记录不完整" : "日志写入正常";
  $("health-description").textContent = degraded
    ? "gateway-b · 最后已知写入：2 分钟前"
    : "最后成功写入：1 秒前";
  $("pending-count").textContent = degraded ? "1,024" : "0";
  $("drop-count").textContent = degraded ? "128" : "0";
};
$("refresh-logs").onclick = () => {
  renderLogs();
  toast("已刷新本地示例记录，未查询真实数据库。");
};
$("reset-demo").onclick = () => location.reload();
renderRules();
renderLogs();
