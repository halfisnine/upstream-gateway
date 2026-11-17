"use strict";
const $ = (id) => document.getElementById(id);
const labels = {
  openai_chat: "OpenAI Chat",
  openai_responses: "OpenAI Responses",
  anthropic_messages: "Anthropic Messages",
  gemini: "Gemini",
};
const errors = {
  unauthorized: "管理员令牌无效，请重新登录。",
  version_conflict:
    "其他操作已发布了新版本。请重新加载最新配置后再编辑，当前内容尚未发布。",
  database_unavailable: "暂时无法连接配置数据库，节点可能仍在使用旧配置。",
  snapshot_unreadable: "该版本无法解密或校验，请检查主密钥和数据完整性。",
  legacy_configuration_requires_republish:
    "旧版配置需重新发布；请在 New API 填写真实上游 Key。",
  snapshot_validation_failed: "配置校验失败，继续使用旧版本。",
  node_id_in_use: "节点标识重复，冲突节点已停止接收新请求。",
  cross_origin_rejected: "已拒绝跨站请求。",
  publication_failed: "发布失败，请检查数据库并刷新状态确认结果。",
};
let token = "",
  latest = 0,
  base = 0,
  draft = { schema_version: 4, channels: [] },
  timer = null,
  busy = false,
  dirty = false,
  historyBefore = 0,
  refreshing = false,
  publishedIDs = new Set(),
  publishedConfig = { schema_version: 4, channels: [] },
  channelPage = 1,
  editor = null,
  detailVersion = null,
  noticeTimer = null,
  lastStatusAt = 0;
function el(tag, text, cls) {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}
function notice(text, bad = false) {
  $("notice").textContent = text;
  $("notice").className = bad ? "error" : "";
  $("notice").hidden = false;
  clearTimeout(noticeTimer);
  if (!bad) noticeTimer = setTimeout(() => ($("notice").hidden = true), 5000);
}
function canonical(value) {
  if (Array.isArray(value)) return value.map(canonical);
  if (value && typeof value === "object")
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, canonical(value[key])]),
    );
  return value;
}
function equal(a, b) {
  return JSON.stringify(canonical(a)) === JSON.stringify(canonical(b));
}
function channelState(ch) {
  const previous = publishedConfig.channels.find((item) => item.id === ch.id);
  return !previous
    ? "新增待发布"
    : equal(previous, ch)
      ? "已发布"
      : "修改待发布";
}
function mark() {
  const added = draft.channels.filter(
    (ch) => channelState(ch) === "新增待发布",
  ).length;
  const modified = draft.channels.filter(
    (ch) => channelState(ch) === "修改待发布",
  ).length;
  const removed = publishedConfig.channels.filter(
    (ch) => !draft.channels.some((item) => item.id === ch.id),
  ).length;
  const ruleChanged =
    !equal(draft.error_rules || [], publishedConfig.error_rules || []) ||
    !!draft.inspect_embedded_errors !==
      !!publishedConfig.inspect_embedded_errors;
  dirty = added + modified + removed > 0 || ruleChanged;
  $("draft-indicator").classList.toggle("pending", dirty);
  $("draft-state").textContent = dirty
    ? `待发布：新增 ${added} · 修改 ${modified} · 删除 ${removed}${ruleChanged ? " · 错误处理有修改" : ""}`
    : "当前配置没有未发布修改。";
}
async function api(path, method = "GET", body) {
  let response;
  try {
    response = await fetch(path, {
      method,
      headers: {
        Authorization: "Bearer " + token,
        ...(body ? { "Content-Type": "application/json" } : {}),
      },
      body: body ? JSON.stringify(body) : undefined,
      cache: "no-store",
      signal: AbortSignal.timeout(12000),
    });
  } catch {
    throw new Error(
      method === "GET"
        ? "网络连接失败，请稍后刷新。"
        : "未能确认提交结果。请刷新版本历史核对，避免重复发布。",
    );
  }
  const result = await response.json();
  if (!response.ok)
    throw new Error(errors[result.error] || "操作失败：" + result.error);
  return result;
}
async function action(fn) {
  if (busy) return;
  busy = true;
  setBusy(true);
  try {
    await fn();
  } catch (e) {
    if ($("workspace").hidden) {
      token = "";
      $("login-error").textContent = e.message;
      $("login-error").hidden = false;
    } else if ($("details").open) {
      $("detail-feedback").textContent = e.message;
      $("detail-feedback").hidden = false;
    } else notice(e.message, true);
  } finally {
    busy = false;
    setBusy(false);
  }
}
function setBusy(value) {
  for (const id of [
    "publish",
    "reload-config",
    "add-channel",
    "logout",
    "login-submit",
    "restore-details",
  ])
    $(id).disabled = value;
  document
    .querySelectorAll(".edit-channel,.delete-channel,.history-actions button")
    .forEach((b) => {
      b.disabled = value || b.dataset.current === "true";
    });
  $("login-submit").textContent =
    value && !$("login").hidden ? "正在连接…" : "进入控制台";
}
async function copyText(value, feedback) {
  try {
    await navigator.clipboard.writeText(value);
    feedback("已复制到剪贴板。");
  } catch {
    feedback("复制失败，请手动选择内容复制，或使用导出按钮。");
  }
}
function feedback(text) {
  $("detail-feedback").textContent = text;
  $("detail-feedback").hidden = false;
}
function field(text, value, onchange, type = "text", placeholder = "") {
  const l = el("label", text),
    i = el("input");
  i.type = type;
  i.setAttribute("aria-label", text);
  i.value = value || "";
  i.placeholder = placeholder;
  if (type === "password") i.autocomplete = "new-password";
  i.addEventListener("input", () => {
    onchange(i.value);
  });
  l.append(i);
  return l;
}
function check(text, checked, onchange) {
  const l = el("label", undefined, "check"),
    i = el("input");
  i.type = "checkbox";
  i.checked = checked;
  i.addEventListener("change", () => {
    onchange(i.checked);
  });
  l.append(i, document.createTextNode(text));
  return l;
}
function button(text, fn, cls = "secondary") {
  const b = el("button", text, cls);
  b.type = "button";
  b.addEventListener("click", fn);
  return b;
}
const shortLabels = {
  openai_chat: "Chat",
  openai_responses: "Responses",
  anthropic_messages: "Messages",
  gemini: "Gemini",
};
function renderChannels() {
  const list = $("channel-list");
  list.replaceChildren();
  const query = $("channel-search").value.trim().toLowerCase();
  const protocolFilter = $("protocol-filter").value;
  const stateFilter = $("state-filter").value;
  $("channel-tab-count").textContent = String(draft.channels.length);
  const filtered = draft.channels
    .map((ch, index) => ({ ch, index }))
    .filter(({ ch }) =>
      [
        ch.name,
        "/r/" + ch.id,
        ch.base_url,
        ...Object.keys(ch.protocols).map((p) => labels[p]),
      ]
        .join(" ")
        .toLowerCase()
        .includes(query),
    )
    .filter(
      ({ ch }) =>
        (!protocolFilter || ch.protocols[protocolFilter]) &&
        (!stateFilter || channelState(ch) === stateFilter),
    );
  const size = Number($("channel-page-size").value);
  const pages = Math.max(1, Math.ceil(filtered.length / size));
  channelPage = Math.max(1, Math.min(channelPage, pages));
  $("channel-count").textContent =
    query || protocolFilter || stateFilter
      ? `找到 ${filtered.length} / ${draft.channels.length} 个渠道`
      : `共 ${draft.channels.length} 个渠道`;
  $("empty-channels").hidden = filtered.length > 0;
  $("empty-channels").textContent = draft.channels.length
    ? "没有匹配的渠道，请尝试其他关键词。"
    : "还没有渠道，点击“新增渠道”开始配置。";
  $("channel-table-wrap").hidden = filtered.length === 0;
  $("channel-pagination").hidden = filtered.length === 0;
  $("channel-prev").disabled = channelPage <= 1;
  $("channel-next").disabled = channelPage >= pages;
  $("channel-page").textContent = `${channelPage} / ${pages} 页`;
  filtered
    .slice((channelPage - 1) * size, channelPage * size)
    .forEach(({ ch, index }) => {
      const row = el("tr");
      row.dataset.channelId = ch.id;
      const name = el("td", undefined, "channel-identity");
      name.append(el("strong", ch.name), el("code", "/r/" + ch.id));
      name.title = ch.name + " · /r/" + ch.id;
      const address = el("td", undefined, "channel-address");
      const url = el("span", ch.base_url, "truncate");
      url.title = ch.base_url;
      const addressLine = el("div", undefined, "address-line");
      const copy = button(
        "",
        () => copyText(ch.base_url, (message) => notice(message)),
        "quiet copy-address",
      );
      copy.setAttribute("aria-label", "复制上游地址：" + ch.name);
      copy.title = "复制完整上游地址";
      const icon = document.createElementNS(
        "http://www.w3.org/2000/svg",
        "svg",
      );
      icon.setAttribute("viewBox", "0 0 24 24");
      icon.setAttribute("aria-hidden", "true");
      const shape = document.createElementNS(
        "http://www.w3.org/2000/svg",
        "path",
      );
      shape.setAttribute("d", "M9 9h11v11H9z M15 9V4H4v11h5");
      icon.append(shape);
      copy.append(icon);
      addressLine.append(url, copy);
      address.append(addressLine);
      const overrides = Object.values(ch.protocols).filter(
        (ep) => ep.base_url,
      ).length;
      if (overrides)
        address.append(el("small", `${overrides} 个协议使用独立地址`, "muted"));
      const protocols = el("td", undefined, "channel-protocols");
      const badges = el("div", undefined, "protocol-badges");
      Object.keys(labels)
        .filter((p) => ch.protocols[p])
        .forEach((p) => {
          const badge = el("span", shortLabels[p], "protocol-badge " + p);
          badge.title = labels[p];
          badges.append(badge);
        });
      protocols.append(badges);
      const state = channelState(ch);
      const status = el("td", undefined, "channel-status");
      status.append(
        el("span", state, "badge" + (state !== "已发布" ? " warn" : "")),
      );
      const actions = el("td", undefined, "channel-actions");
      actions.append(
        button("编辑", () => openEditor(index), "quiet edit-channel"),
        button(
          "删除",
          () => {
            if (busy) return;
            if (
              !confirm(
                `从草稿中删除“${ch.name}”？发布后才会停止接收该渠道的新请求。`,
              )
            )
              return;
            if (
              (draft.error_rules || []).some((r) =>
                (r.channels || []).includes(ch.id),
              )
            ) {
              notice("此渠道被错误规则引用，请先修改或删除关联规则。", true);
              return;
            }
            draft.channels.splice(index, 1);
            mark();
            renderChannels();
          },
          "quiet danger delete-channel",
        ),
      );
      row.append(name, address, protocols, status, actions);
      list.append(row);
    });
}
function editorChanged() {
  return editor && !equal(editor.channel, editor.initial);
}
function closeEditor(force = false) {
  if (
    !force &&
    editorChanged() &&
    !confirm("放弃这次编辑？尚未保存到草稿的修改将丢失。")
  )
    return;
  const trigger = editor?.trigger;
  editor = null;
  $("channel-editor").close();
  if (trigger?.isConnected) trigger.focus();
  else $("channel-search").focus();
}
function renderProtocolDetails() {
  const container = $("editor-protocol-details");
  const opened = new Set(
    [...container.querySelectorAll("details[open]")].map(
      (d) => d.dataset.protocol,
    ),
  );
  container.replaceChildren();
  Object.keys(labels)
    .filter((p) => editor.channel.protocols[p])
    .forEach((p) => {
      const ep = editor.channel.protocols[p];
      const details = el("details", undefined, "protocol");
      details.dataset.protocol = p;
      details.open = opened.has(p);
      details.append(el("summary", labels[p] + " · 可选覆盖设置"));
      const grid = el("div", undefined, "grid");
      grid.append(
        field(
          "该协议上游地址",
          ep.base_url,
          (v) => (ep.base_url = v),
          "url",
          "留空使用渠道默认地址",
        ),
      );
      const label = el("label", "上游认证头（不改变 Key）"),
        select = el("select");
      [
        ["", "协议默认"],
        ["bearer", "Authorization Bearer"],
        ["x_api_key", "x-api-key"],
        ["x_goog_api_key", "x-goog-api-key"],
      ].forEach(([value, name]) => {
        const option = el("option", name);
        option.value = value;
        select.append(option);
      });
      select.value = ep.auth || "";
      select.addEventListener("change", () => (ep.auth = select.value));
      label.append(select);
      grid.append(label);
      details.append(grid);
      container.append(details);
    });
}
function openEditor(index = null) {
  if (busy) return;
  const ch =
    index === null
      ? {
          id: "",
          name: "",
          base_url: "",
          protocols: { openai_chat: { compatibility: "standard_v1" } },
        }
      : structuredClone(draft.channels[index]);
  editor = {
    index,
    channel: ch,
    initial: structuredClone(ch),
    protocolCache: structuredClone(ch.protocols),
    trigger: document.activeElement,
  };
  $("editor-title").textContent =
    index === null ? "新增渠道" : "编辑渠道 · " + ch.name;
  $("editor-error").hidden = true;
  $("editor-route").textContent =
    index === null ? "新增渠道 · 路由配置" : "渠道设置 · /r/" + ch.id;
  const fields = $("editor-fields");
  fields.replaceChildren();
  const id = field(
    "渠道标识",
    ch.id,
    (v) => (ch.id = v),
    "text",
    "例如 vendor-a",
  );
  const idInput = id.querySelector("input");
  idInput.required = true;
  idInput.pattern = "[a-z0-9][a-z0-9_-]{0,63}";
  idInput.disabled = index !== null && publishedIDs.has(ch.id);
  const name = field(
    "渠道名称",
    ch.name,
    (v) => (ch.name = v),
    "text",
    "例如 中转站 A",
  );
  name.querySelector("input").required = true;
  const address = field(
    "默认上游地址",
    ch.base_url,
    (v) => (ch.base_url = v),
    "url",
    "https://api.example.com，不含接口路径",
  );
  address.querySelector("input").required = true;
  id.append(
    el(
      "small",
      idInput.disabled
        ? "已发布的标识固定，避免影响 New API 路由。"
        : "小写字母、数字、横线或下划线，最长 64 字符。",
      "field-hint",
    ),
  );
  name.append(el("small", "用于控制台管理识别", "field-hint"));
  address.append(
    el(
      "small",
      "填写上游根地址；可包含路径前缀，不重复附加协议接口路径。",
      "field-hint",
    ),
  );
  fields.append(id, name, address);
  $("editor-http").replaceChildren(
    check(
      "允许 HTTP（仅用于受控网络或本地测试）",
      !!ch.allow_http,
      (v) => (ch.allow_http = v),
    ),
  );
  const protocols = $("editor-protocol-options");
  protocols.replaceChildren();
  $("editor-protocol-details").replaceChildren();
  const protocolPaths = {
    openai_chat: "/v1/chat/completions",
    openai_responses: "/v1/responses",
    anthropic_messages: "/v1/messages",
    gemini: ":generateContent / :streamGenerateContent",
  };
  Object.entries(labels).forEach(([p, label]) => {
    const option = check(label, !!ch.protocols[p], (enabled) => {
      if (enabled)
        ch.protocols[p] = editor.protocolCache[p] || {
          compatibility: "standard_v1",
        };
      else {
        editor.protocolCache[p] = ch.protocols[p];
        delete ch.protocols[p];
      }
      renderProtocolDetails();
    });
    option.querySelector("input").setAttribute("aria-label", label);
    const caption = el("span", undefined, "protocol-label");
    caption.append(el("span", label), el("small", protocolPaths[p]));
    option.lastChild.replaceWith(caption);
    protocols.append(option);
  });
  renderProtocolDetails();
  $("channel-editor").showModal();
  (idInput.disabled ? name.querySelector("input") : idInput).focus();
}
$("channel-editor-form").addEventListener("submit", (event) => {
  event.preventDefault();
  if (!editor) return;
  const ch = editor.channel;
  ch.id = ch.id.trim();
  ch.name = ch.name.trim();
  ch.base_url = ch.base_url.trim();
  let problem = "";
  if (!ch.name) problem = "请填写渠道名称。";
  else if (new TextEncoder().encode(ch.name).length > 100)
    problem = "渠道名称过长，请使用更简短的名称。";
  else if (
    draft.channels.some(
      (item, index) => index !== editor.index && item.id === ch.id,
    )
  )
    problem = "此渠道标识已存在，请使用不同标识。";
  else if (!Object.keys(ch.protocols).length) problem = "请至少启用一种协议。";
  if (problem) {
    $("editor-error").textContent = problem;
    $("editor-error").hidden = false;
    return;
  }
  const isNew = editor.index === null;
  if (isNew) {
    draft.channels.push(structuredClone(ch));
    $("channel-search").value = "";
    $("protocol-filter").value = "";
    $("state-filter").value = "";
    channelPage = Math.ceil(
      draft.channels.length / Number($("channel-page-size").value),
    );
  } else draft.channels[editor.index] = structuredClone(ch);
  const savedID = ch.id;
  closeEditor(true);
  mark();
  renderChannels();
  const savedRow = [...$("channel-list").children].find(
    (row) => row.dataset.channelId === savedID,
  );
  (savedRow?.querySelector(".edit-channel") || $("channel-search")).focus();
  notice("已保存到草稿。点击“发布完整配置”后才会生效。");
});
$("close-editor").addEventListener("click", () => closeEditor());
$("cancel-editor").addEventListener("click", () => closeEditor());
$("channel-editor").addEventListener("cancel", (event) => {
  event.preventDefault();
  closeEditor();
});
$("channel-search").addEventListener("input", () => {
  channelPage = 1;
  renderChannels();
});
for (const id of ["protocol-filter", "state-filter"])
  $(id).addEventListener("change", () => {
    channelPage = 1;
    renderChannels();
  });
$("channel-page-size").addEventListener("change", () => {
  channelPage = 1;
  renderChannels();
});
$("channel-prev").addEventListener("click", () => {
  channelPage--;
  renderChannels();
});
$("channel-next").addEventListener("click", () => {
  channelPage++;
  renderChannels();
});
async function loadConfig() {
  const state = await api("/api/status");
  latest = state.latest_version;
  const v = await api("/api/versions/" + latest);
  base = latest;
  draft = v.config;
  window.errorTools?.render();
  publishedConfig = structuredClone(draft);
  publishedIDs = new Set(draft.channels.map((ch) => ch.id));
  $("migration-notice").hidden = !v.requires_republish;
  dirty = false;
  $("baseline").textContent = "V" + base;
  mark();
  renderChannels();
  renderStatus(state);
}
function renderStatus(s) {
  window.errorTools?.status(s);
  latest = s.latest_version;
  $("latest").textContent = latest ? "V" + latest : "尚未发布";
  const synced = s.nodes.filter(
    (n) => n.online && !n.error && n.applied_version === latest && latest > 0,
  );
  $("synced").textContent = synced.length + " / " + s.nodes.length;
  $("synced").className =
    "badge" +
    (synced.length === s.nodes.length && s.nodes.length ? "" : " warn");
  $("node-tab-count").textContent = synced.length + "/" + s.nodes.length;
  $("history-tab-version").textContent = latest ? "V" + latest : "—";
  $("inflight").textContent = String(
    s.nodes.filter((n) => n.online).reduce((total, n) => total + n.inflight, 0),
  );
  $("node-summary").textContent = !s.nodes.length
    ? "暂无节点"
    : synced.length === s.nodes.length
      ? "全部节点已同步"
      : `${s.nodes.length - synced.length} 个节点待同步或异常`;
  lastStatusAt = Date.now();
  $("last-check").textContent = "最近刷新 " + new Date().toLocaleTimeString();
  $("connection").textContent = "配置状态已更新";
  $("connection").classList.remove("stale");
  document.querySelectorAll("#history-list tr").forEach((row) => {
    const current = Number(row.dataset.version) === latest;
    row.classList.toggle("current-version", current);
    row.querySelector(".version-status").textContent = current
      ? "最新发布"
      : "历史版本";
    const restore = row.querySelector(".restore-version");
    restore.dataset.current = String(current);
    restore.disabled = busy || current;
  });
  const list = $("node-list");
  list.replaceChildren();
  $("node-empty").hidden = s.nodes.length > 0;
  s.nodes.forEach((n) => {
    const tr = el("tr");
    let state = "等待首版配置",
      cls = "warn";
    if (!n.online) {
      state = "离线 / 状态未知";
      cls = "error";
    } else if (n.error) {
      state = errors[n.error] || n.error;
      cls = "error";
    } else if (n.applied_version === latest && latest > 0) {
      state = "已同步";
      cls = "";
    } else if (latest > 0) {
      state = "待同步";
    }
    const status = el("td");
    status.append(el("span", state, "badge " + cls));
    tr.append(
      el("td", n.id),
      status,
      el("td", "V" + n.desired_version + " / V" + n.applied_version),
      el("td", new Date(n.last_seen).toLocaleString()),
      el("td", String(n.inflight)),
    );
    const more = el("td");
    if (n.applied_version > 0)
      more.append(
        button(
          "查看已生效内容",
          () => action(() => showVersion(n.applied_version, n.id)),
          "quiet",
        ),
      );
    tr.append(more);
    list.append(tr);
  });
}
async function refresh() {
  if (refreshing || !token) return;
  refreshing = true;
  $("refresh").disabled = true;
  const requestToken = token;
  try {
    const status = await api("/api/status");
    if (token === requestToken) renderStatus(status);
  } catch (e) {
    if (token === requestToken) {
      $("connection").textContent = "状态更新失败";
      $("connection").classList.add("stale");
      $("synced").className = "badge warn";
      $("node-summary").textContent = "刷新失败，以下为上次读取的状态";
      notice(e.message, true);
    }
  } finally {
    refreshing = false;
    $("refresh").disabled = false;
  }
}
function renderJSON(target, value) {
  target.replaceChildren();
  // Native details provide keyboard-accessible folding. All values remain text nodes.
  function node(value, key, depth, comma, channel = false) {
    const container = value !== null && typeof value === "object";
    const entries = container ? Object.entries(value) : [];
    const array = Array.isArray(value);
    const opening = array ? "[" : "{";
    const closing = array ? "]" : "}";
    const prefix = () =>
      key === null ? [] : [el("span", JSON.stringify(key) + ": ", "syn-key")];
    if (!container || !entries.length) {
      const row = el("div", undefined, "json-leaf");
      const text = container ? opening + closing : JSON.stringify(value);
      const cls =
        typeof value === "string"
          ? "syn-str"
          : typeof value === "number"
            ? "syn-num"
            : "syn-bool";
      row.append(
        ...prefix(),
        el("span", text, container ? "" : cls),
        document.createTextNode(comma),
      );
      return row;
    }
    const branch = el("details", undefined, "json-branch");
    branch.open = depth < 2;
    if (channel) branch.dataset.channelId = value.id;
    const summary = el("summary");
    summary.append(...prefix(), document.createTextNode(opening));
    summary.append(el("span", " … " + closing + comma, "json-collapsed"));
    const description = channel
      ? `${value.name} · ${value.id}`
      : `${entries.length} ${array ? "项" : "个字段"}`;
    summary.append(el("span", description, "json-summary-note"));
    const children = el("div", undefined, "json-children");
    entries.forEach(([childKey, child], index) => {
      children.append(
        node(
          child,
          array ? null : childKey,
          depth + 1,
          index < entries.length - 1 ? "," : "",
          key === "channels" && array,
        ),
      );
    });
    branch.append(
      summary,
      children,
      el("div", closing + comma, "json-closing"),
    );
    return branch;
  }
  target.append(node(value, null, 0, ""));
}
for (const [id, open] of [
  ["expand-json", true],
  ["collapse-json", false],
]) {
  $(id).addEventListener("click", () => {
    $("detail-content")
      .querySelectorAll("details")
      .forEach((branch) => (branch.open = open));
  });
}
async function showVersion(id, node = "") {
  const v = await api("/api/versions/" + id);
  detailVersion = { id, config: v.config };
  $("detail-title").textContent =
    (node ? node + " · " : "") + "V" + id + " 配置";
  $("detail-kind").textContent = node
    ? "节点报告的已生效版本"
    : "不可变配置快照";
  const created = v.version.created_at
    ? new Date(v.version.created_at).toLocaleString()
    : "";
  $("detail-meta").textContent = [
    created,
    v.version.digest ? "SHA256 " + v.version.digest : "",
    v.version.note,
  ]
    .filter(Boolean)
    .join(" · ");
  $("detail-feedback").hidden = true;
  $("detail-diff").hidden = true;
  $("restore-details").hidden = !!node || id === latest;
  renderJSON($("detail-content"), v.config);
  $("details").showModal();
  $("details").querySelector(".modal-body").scrollTop = 0;
}
async function restoreVersion(id) {
  if (
    !confirm(
      `将 V${id} 的路由和协议配置发布为新版本？现有草稿将被替换。旧版本中的凭证不会恢复，Key 仍由 New API 携带。`,
    )
  )
    return;
  const result = await api("/api/rollback", "POST", {
    base_version: latest,
    source_version: id,
    note: "运维页面恢复",
  });
  $("details").close();
  await loadConfig();
  await history();
  notice("已发布 V" + result.id + "。请到节点状态确认生效。");
}
async function history(reset = true) {
  const vs = await api(
    "/api/versions" +
      (!reset && historyBefore ? "?before=" + historyBefore : ""),
  );
  if (reset) {
    historyBefore = 0;
    $("history-list").replaceChildren();
  }
  vs.forEach((v) => {
    const current = v.id === latest;
    const row = el("tr", undefined, current ? "current-version" : "");
    row.dataset.version = String(v.id);
    const digest = el("td", v.digest.slice(0, 12), "mono muted small");
    digest.title = v.digest;
    row.append(
      el("td", "V" + v.id, "history-version"),
      digest,
      el("td", v.note || "未填写发布说明", "history-note"),
      el("td", new Date(v.created_at).toLocaleString(), "mono muted small"),
      el("td", current ? "最新发布" : "历史版本", "version-status"),
    );
    const actions = el("td"),
      buttons = el("div", undefined, "history-actions");
    const restore = button(
      "恢复此版本",
      () => action(() => restoreVersion(v.id)),
      "secondary restore-version",
    );
    restore.dataset.current = String(current);
    restore.disabled = current;
    buttons.append(
      button("查看内容", () => action(() => showVersion(v.id))),
      restore,
    );
    actions.append(buttons);
    row.append(actions);
    $("history-list").append(row);
  });
  historyBefore = vs.length ? vs.at(-1).id : 0;
  $("more-history").hidden = vs.length < 100;
  const count = $("history-list").children.length;
  $("history-empty").hidden = count > 0;
  $("history-count").textContent = `已加载 ${count} 个版本`;
}
$("login-form").addEventListener("submit", (e) => {
  e.preventDefault();
  action(async () => {
    $("login-error").hidden = true;
    token = $("admin-token").value.trim();
    await loadConfig();
    $("admin-token").value = "";
    $("login").hidden = true;
    $("workspace").hidden = false;
    $("logout").hidden = false;
    $("connection").hidden = false;
    $("admin-token").type = "password";
    $("toggle-token").textContent = "显示";
    $("toggle-token").setAttribute("aria-label", "显示令牌");
    $("toggle-token").setAttribute("aria-pressed", "false");
    $("notice").hidden = true;
    clearInterval(timer);
    timer = setInterval(refresh, 5000);
  });
});
$("logout").addEventListener("click", () => {
  if (dirty && !confirm("退出登录将丢弃未发布草稿，确定退出？")) return;
  token = "";
  window.errorTools?.clear();
  dirty = false;
  publishedIDs.clear();
  detailVersion = null;
  $("publish-note").value = "";
  $("channel-search").value = "";
  $("protocol-filter").value = "";
  $("state-filter").value = "";
  $("connection").hidden = true;
  clearInterval(timer);
  draft = { schema_version: 4, channels: [] };
  $("workspace").hidden = true;
  $("login").hidden = false;
  $("logout").hidden = true;
  $("notice").hidden = true;
  $("channel-list").replaceChildren();
  $("history-list").replaceChildren();
  $("detail-content").textContent = "";
  $("details").close();
  $("admin-token").focus();
});
$("toggle-token").addEventListener("click", () => {
  const show = $("admin-token").type === "password";
  $("admin-token").type = show ? "text" : "password";
  $("toggle-token").textContent = show ? "隐藏" : "显示";
  $("toggle-token").setAttribute("aria-label", show ? "隐藏令牌" : "显示令牌");
  $("toggle-token").setAttribute("aria-pressed", String(show));
});
$("refresh").addEventListener("click", refresh);
$("add-channel").addEventListener("click", () => openEditor());
$("reload-config").addEventListener("click", () =>
  action(async () => {
    if (dirty && !confirm("放弃未发布修改，加载最新配置？")) return;
    await loadConfig();
    notice("已加载最新配置。");
  }),
);
$("publish").addEventListener("click", () =>
  action(async () => {
    if (
      !confirm(
        "发布当前全部 " +
          draft.channels.length +
          " 个渠道及 " +
          (draft.error_rules || []).length +
          " 条错误规则的完整配置？各节点将在检查到更新后生效。",
      )
    )
      return;
    const v = await api("/api/publish", "POST", {
      base_version: base,
      note: $("publish-note").value,
      config: draft,
    });
    await loadConfig();
    if (!$("history").hidden) await history();
    $("publish-note").value = "";
    notice("V" + v.id + " 发布成功。请查看目标节点是否已生效。");
  }),
);
$("more-history").addEventListener("click", () => action(() => history(false)));
$("close-details").addEventListener("click", () => $("details").close());
document.querySelectorAll("[data-tab]").forEach((b) =>
  b.addEventListener("click", () => {
    document.querySelectorAll("[data-tab]").forEach((t) => {
      t.classList.toggle("active", t === b);
      if (t === b) t.setAttribute("aria-current", "page");
      else t.removeAttribute("aria-current");
    });
    document
      .querySelectorAll(".tab-panel")
      .forEach((p) => (p.hidden = p.id !== b.dataset.tab));
    if (b.dataset.tab === "history") action(() => history());
    if (b.dataset.tab === "nodes") refresh();
    if (b.dataset.tab === "error-rules") window.errorTools?.render();
    if (b.dataset.tab === "intercept-logs") window.errorTools?.logs();
  }),
);
window.addEventListener("beforeunload", (e) => {
  if (dirty || editorChanged() || window.errorTools?.changed()) {
    e.preventDefault();
    e.returnValue = "";
  }
});

$("copy-details").addEventListener("click", () => {
  if (detailVersion)
    copyText(JSON.stringify(detailVersion.config, null, 2), feedback);
});
$("export-details").addEventListener("click", () => {
  if (!detailVersion) return;
  const blob = new Blob(
    [JSON.stringify(detailVersion.config, null, 2) + "\n"],
    { type: "application/json" },
  );
  const url = URL.createObjectURL(blob);
  const link = el("a");
  link.href = url;
  link.download = `gateway-config-v${detailVersion.id}.json`;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  feedback("已导出配置 JSON。");
});
$("restore-details").addEventListener("click", () => {
  if (detailVersion) action(() => restoreVersion(detailVersion.id));
});
$("compare-details").addEventListener("click", () => {
  if (!detailVersion) return;
  const lines = [];
  function compare(old, next, path) {
    if (equal(old, next)) return;
    if (
      old &&
      next &&
      typeof old === "object" &&
      typeof next === "object" &&
      !Array.isArray(old) &&
      !Array.isArray(next)
    ) {
      for (const key of new Set([...Object.keys(old), ...Object.keys(next)]))
        compare(old[key], next[key], path + "." + key);
    } else
      lines.push(
        `${path}\n  历史：${old === undefined ? "（不存在）" : JSON.stringify(old)}\n  草稿：${next === undefined ? "（不存在）" : JSON.stringify(next)}`,
      );
  }
  const byID = (cfg) =>
    Object.fromEntries(cfg.channels.map((ch) => [ch.id, ch]));
  compare(byID(detailVersion.config), byID(draft), "channels");
  compare(
    detailVersion.config.error_rules || [],
    draft.error_rules || [],
    "error_rules",
  );
  compare(
    !!detailVersion.config.inspect_embedded_errors,
    !!draft.inspect_embedded_errors,
    "inspect_embedded_errors",
  );
  $("diff-content").textContent = lines.length
    ? lines.join("\n\n")
    : "完整配置与当前草稿一致。";
  $("detail-diff").hidden = false;
  $("detail-diff").scrollIntoView({ block: "nearest" });
});
// Reserve the actual dock height, including wrapped controls at mobile widths.
new ResizeObserver(([entry]) => {
  document.body.style.setProperty(
    "--dock-height",
    Math.ceil(entry.target.getBoundingClientRect().height + 16) + "px",
  );
}).observe($("publish-dock"));
setInterval(() => {
  if (token && lastStatusAt && Date.now() - lastStatusAt > 20000) {
    $("connection").textContent = "状态已过期，等待刷新";
    $("connection").classList.add("stale");
  }
}, 5000);
