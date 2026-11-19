"use strict";
(() => {
  let editing = null,
    presets = null,
    loading = false;
  const methods = ["GET", "POST", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS"];
  const statusChoices = [
    ["400-599", "常见错误：400-599"],
    ["500-599", "服务端错误：500-599"],
    ["429", "请求限流：429"],
    ["300-599", "重定向及错误：300-599"],
    ["", "不按 HTTP 状态码检查"],
    ["custom", "自定义"],
  ];
  const statusExpression = (t) =>
    t.http_statuses ?? (t.mode === "json_sse" ? "201-599" : "400-599");
  const modes = {
    json_sse: "检查 HTTP 200 JSON / SSE（需开启正文检查）",
    http_errors: "不检查返回内容",
  };
  Object.assign(errors, {
    invalid_inspection_statuses:
      "状态码格式：301,400-500,503。支持 200–599，范围起始值不能大于结束值，最多 32 项。",
    invalid_inspection_template:
      "模板标识、名称、协议、检查方式或规则数量无效。",
    invalid_template_match: "请求方法或匹配规则无效，请检查是否重复。",
    invalid_template_path:
      "请填写合法路径，不含查询参数、编码字符、重复斜线或路径穿越。",
    invalid_template_regex: "正则表达式无效，请使用 Go RE2 支持的语法。",
    invalid_match_sample: "测试路径需以 / 开头，且不含查询参数。",
  });
  const templates = () => draft.inspection_templates || [];
  const usages = (id) =>
    draft.channels.filter((ch) => (ch.inspection_templates || []).includes(id));
  const changed = () => !!editing && !equal(editing.value, editing.initial);
  function choice(title, options, value, update) {
    const label = el("label", title),
      select = el("select");
    select.setAttribute("aria-label", title);
    for (const [key, text] of options) {
      const option = el("option", text);
      option.value = key;
      select.append(option);
    }
    select.value = value;
    select.addEventListener("change", () => update(select.value));
    label.append(select);
    return label;
  }
  function renderLinks() {
    if (!editor) return;
    const ch = editor.channel,
      ids = ch.inspection_templates || [];
    $("inspection-link-count").textContent = `（${ids.length} 个模板）`;
    const selected = $("inspection-links");
    selected.replaceChildren();
    ids.forEach((id, index) => {
      const t = templates().find((t) => t.id === id);
      const row = el("div", undefined, "template-link");
      row.append(
        el(
          "span",
          `${index + 1}. ${t?.name || id} · ${labels[t?.protocol] || "模板不存在"}`,
        ),
      );
      for (const [text, delta] of [
        ["上移", -1],
        ["下移", 1],
      ]) {
        const b = button(
          text,
          () => {
            [ids[index], ids[index + delta]] = [ids[index + delta], ids[index]];
            renderLinks();
          },
          "quiet",
        );
        b.disabled = index + delta < 0 || index + delta >= ids.length;
        row.append(b);
      }
      row.append(
        button(
          "取消关联",
          () => {
            ch.inspection_templates = ids.filter((x) => x !== id);
            renderLinks();
          },
          "quiet danger",
        ),
      );
      selected.append(row);
    });
    if (!ids.length)
      selected.append(
        el("p", "未关联模板：此渠道的所有接口默认透传。", "muted"),
      );
    const available = $("inspection-choices");
    available.replaceChildren();
    for (const t of templates()) {
      const label = el("label", undefined, "template-choice");
      const check = el("input");
      check.type = "checkbox";
      check.checked = ids.includes(t.id);
      check.disabled = !check.checked && ids.length >= 64;
      check.setAttribute("aria-label", `关联 ${t.name}`);
      check.addEventListener("change", () => {
        if (check.checked && !ch.protocols[t.protocol])
          ch.protocols[t.protocol] = structuredClone(
            editor.protocolCache[t.protocol] || {
              compatibility: "standard_v1",
            },
          );
        ch.inspection_templates = check.checked
          ? [...ids, t.id]
          : ids.filter((id) => id !== t.id);
        renderLinks();
      });
      label.append(
        check,
        el("span", t.name + (check.disabled ? "（最多关联 64 个）" : "")),
      );
      available.append(label);
    }
    if (!templates().length)
      available.append(
        el("p", "尚无模板，请先在“错误检查接口”页面从内置模板创建。", "muted"),
      );
    $("inspection-order").hidden = ids.length < 2;
    renderProtocolOptions();
  }
  function render() {
    $("template-count").textContent = templates().length;
    const list = $("template-list");
    list.replaceChildren();
    const query = $("template-search").value.trim().toLowerCase();
    const protocol = $("template-protocol-filter").value;
    const usage = $("template-usage-filter").value;
    const visible = templates().filter((t) => {
      const count = usages(t.id).length;
      const text = [
        t.name,
        t.id,
        ...t.matches.map((m) => m.path),
        ...(t.exclude || []).map((m) => m.path),
      ]
        .join(" ")
        .toLowerCase();
      return (
        (!query || text.includes(query)) &&
        (!protocol || t.protocol === protocol) &&
        (!usage || (usage === "used" ? count > 0 : count === 0))
      );
    });
    $("template-result-count").textContent =
      `显示 ${visible.length} / ${templates().length} 个模板`;
    $("template-range-note").hidden = !visible.some(
      (t) => statusExpression(t) === "201-599",
    );
    $("template-empty").hidden = visible.length > 0;
    $("template-empty").textContent = templates().length
      ? "没有匹配的模板，请调整搜索或筛选条件。"
      : "尚无模板，可从内置模板创建，也可以新增自定义模板。";
    for (const t of visible) {
      const card = el("tr", undefined, "template-card");
      const cell = (label) => {
        const td = el("td");
        td.dataset.label = label;
        card.append(td);
        return td;
      };
      const title = cell("模板名称");
      title.append(el("strong", t.name));
      const protocolCell = cell("检查协议");
      protocolCell.append(
        el("span", labels[t.protocol], "template-protocol-tag"),
      );
      const statuses = cell("上游状态范围");
      statuses.append(
        el("span", statusExpression(t) || "不按状态检查", "mono"),
      );
      if (statusExpression(t) === "201-599")
        statuses.append(el("small", "含成功 / 重定向", "template-range-hint"));
      const body = cell("200 正文检查");
      body.append(
        el(
          "span",
          t.mode === "json_sse"
            ? draft.inspect_embedded_errors
              ? "检查 JSON / SSE"
              : "未开启（总开关关闭）"
            : "不检查正文",
          "muted small",
        ),
      );
      const channels = cell("关联渠道"),
        used = usages(t.id);
      if (used.length) {
        const associated = el("details");
        associated.append(el("summary", `${used.length} 个渠道`));
        for (const ch of used) associated.append(el("p", ch.name, "small"));
        channels.append(associated);
      } else channels.append(el("span", "未关联", "muted small"));
      const rules = cell("接口规则"),
        details = el("details");
      details.append(
        el(
          "summary",
          `${t.matches.length} 条匹配${t.exclude?.length ? ` / ${t.exclude.length} 条排除` : ""}`,
        ),
      );
      for (const [entries, prefix] of [
        [t.matches, ""],
        [t.exclude || [], "排除 "],
      ])
        for (const m of entries)
          details.append(
            el(
              "p",
              `${prefix}${m.method} · ${{ exact: "精确", prefix: "前缀", regex: "正则" }[m.kind]} · ${m.path}`,
              "mono template-path",
            ),
          );
      rules.append(details);
      const actions = el("div", undefined, "template-row-actions");
      actions.append(
        button("编辑", () => open(t), "quiet"),
        button("复制", () => open(t, true), "quiet"),
        button(
          "删除",
          () => {
            if (busy) return;
            if (usages(t.id).length) {
              notice("模板仍被渠道关联，请先在渠道中取消关联。", true);
              return;
            }
            if (!confirm(`从草稿中删除“${t.name}”？发布后生效。`)) return;
            draft.inspection_templates = templates().filter(
              (x) => x.id !== t.id,
            );
            mark();
            render();
          },
          "quiet danger",
        ),
      );
      cell("操作").append(actions);
      list.append(card);
    }
    if (!presets && !loading && token) {
      loading = true;
      const session = authEpoch;
      api("/api/inspection-presets")
        .then((items) => {
          if (session !== authEpoch) return;
          presets = items;
          $("inspection-preset").replaceChildren();
          for (const p of presets) {
            const option = el("option", p.name);
            option.value = p.id;
            $("inspection-preset").append(option);
          }
        })
        .catch((e) => {
          if (session === authEpoch) notice(e.message, true);
        })
        .finally(() => {
          loading = false;
        });
    }
  }
  function uniqueID(base) {
    let id = base,
      n = 2;
    while (templates().some((t) => t.id === id))
      id = base.slice(0, 55) + "-" + n++;
    return id;
  }
  function open(source, copy = false) {
    if (busy) return;
    const value = source
      ? structuredClone(source)
      : {
          id: uniqueID("custom-template"),
          name: "",
          protocol: "openai_chat",
          mode: "http_errors",
          http_statuses: "400-599",
          matches: [{ method: "POST", kind: "exact", path: "" }],
        };
    value.http_statuses = statusExpression(value);
    if (copy) {
      value.id = uniqueID(source.id);
      value.name = source.name + " 副本";
    }
    editing = {
      id: source && !copy ? source.id : null,
      value,
      initial: structuredClone(value),
      trigger: document.activeElement,
    };
    $("template-editor-title").textContent = editing.id
      ? "编辑匹配模板"
      : "新增匹配模板";
    const used = editing.id ? usages(editing.id) : [];
    $("template-usage").textContent = used.length
      ? `发布后将影响关联渠道：${used.map((ch) => ch.name).join("、")}。修改检查协议时，这些渠道也会自动补齐协议设置。`
      : "保存后，在渠道中勾选这个模板，再统一发布生效。";
    $("template-editor-error").hidden = true;
    $("template-test-result").textContent = "";
    const fields = $("template-fields");
    fields.replaceChildren();
    const id = field("模板标识", value.id, (v) => (value.id = v));
    id.querySelector("input").disabled = !!editing.id;
    const advanced = $("template-advanced-fields");
    advanced.replaceChildren(
      id,
      choice(
        "检查协议",
        Object.entries(labels),
        value.protocol,
        (v) => (value.protocol = v),
      ),
    );
    fields.append(
      field("模板名称", value.name, (v) => (value.name = v)),
      choice(
        "返回内容检查",
        Object.entries(modes),
        value.mode,
        (v) => (value.mode = v),
      ),
    );
    const custom = field(
      "自定义 HTTP 状态码",
      value.http_statuses,
      (v) => (value.http_statuses = v),
      "text",
      "例如：301,400-500,503",
    );
    custom.querySelector("input").maxLength = 512;
    const preset = statusChoices
      .slice(0, -1)
      .some(([v]) => v === value.http_statuses)
      ? value.http_statuses
      : "custom";
    custom.hidden = preset !== "custom";
    const statusChoice = choice("HTTP 状态码", statusChoices, preset, (v) => {
      custom.hidden = v !== "custom";
      if (v !== "custom") {
        value.http_statuses = v;
        custom.querySelector("input").value = v;
      } else custom.querySelector("input").focus();
    });
    fields.append(statusChoice, custom);
    fields.append(
      el(
        "p",
        "选中的上游状态按 HTTP 错误规则处理；其他状态直接透传。范围包含两端。HTTP 200 的返回内容检查可单独启用。",
        "muted small",
      ),
    );
    $("template-path-summary").textContent =
      `${value.matches.length} 条接口规则${value.exclude?.length ? `，${value.exclude.length} 条排除规则` : ""}。修改接口范围请展开高级设置。`;
    $("template-advanced").open = !source;
    renderMatches();
    $("template-exclusions").open = !!value.exclude?.length;
    $("template-editor").showModal();
    fields.querySelector("input:not(:disabled)").focus();
  }
  function close(force = false) {
    if (!force && busy) return;
    if (!force && changed() && !confirm("放弃未保存的模板修改？")) return;
    const trigger = editing?.trigger;
    editing = null;
    $("template-editor").close();
    if (trigger?.isConnected) trigger.focus();
  }
  function renderMatches() {
    for (const [key, id, title, max] of [
      ["matches", "template-matches", "匹配", 32],
      ["exclude", "template-excludes", "排除", 64],
    ]) {
      const container = $(id);
      container.replaceChildren();
      (editing.value[key] || []).forEach((m, index) => {
        const row = el("fieldset", undefined, "allowed-path-row");
        row.append(el("legend", `${title}规则 ${index + 1}`));
        const grid = el("div", undefined, "grid");
        grid.append(
          choice(
            "请求方法",
            methods.map((m) => [m, m]),
            m.method,
            (v) => (m.method = v),
          ),
          choice(
            "匹配方式",
            [
              ["exact", "精确路径"],
              ["prefix", "路径前缀"],
              ["regex", "正则表达式"],
            ],
            m.kind,
            (v) => (m.kind = v),
          ),
          field(
            "路径或表达式",
            m.path,
            (v) => (m.path = v),
            "text",
            "/v1/chat/completions",
          ),
        );
        grid.querySelector("input").maxLength = 1024;
        row.append(
          grid,
          button(
            "删除规则",
            () => {
              editing.value[key].splice(index, 1);
              if (key === "exclude" && !editing.value[key].length)
                delete editing.value.exclude;
              renderMatches();
            },
            "quiet danger",
          ),
        );
        container.append(row);
      });
      $(
        key === "matches" ? "add-template-match" : "add-template-exclude",
      ).disabled = (editing.value[key] || []).length >= max;
    }
  }
  function problem(message) {
    $("template-editor-error").textContent = message;
    $("template-editor-error").hidden = false;
    $("template-advanced").open = true;
    $("template-editor-error").scrollIntoView({ block: "nearest" });
  }
  for (const [id, key] of [
    ["add-template-match", "matches"],
    ["add-template-exclude", "exclude"],
  ])
    $(id).addEventListener("click", () => {
      if (!editing || busy) return;
      (editing.value[key] ||= []).push({
        method: "POST",
        kind: "exact",
        path: "",
      });
      renderMatches();
    });
  $("create-template").addEventListener("click", () => open());
  $("create-from-preset").addEventListener("click", () => {
    const t = presets?.find((p) => p.id === $("inspection-preset").value);
    if (t) open(t, true);
    else notice("内置模板尚未加载，请稍后重试。", true);
  });
  for (const id of ["cancel-template", "close-template"])
    $(id).addEventListener("click", () => close());
  $("template-editor").addEventListener("cancel", (e) => {
    e.preventDefault();
    close();
  });
  $("test-template").addEventListener("click", () =>
    action(async () => {
      if (!editing) return;
      try {
        const result = await api("/api/inspection-test", "POST", {
          template: editing.value,
          method: $("template-test-method").value,
          path: $("template-test-path").value,
        });
        $("template-test-result").textContent = result.matched
          ? "命中：此请求会进入该模板的错误检查。"
          : "未命中：此模板不检查该请求。";
      } catch (e) {
        problem(e.message);
      }
    }),
  );
  $("template-editor-form").addEventListener("submit", (e) => {
    e.preventDefault();
    if (!editing || busy) return;
    action(async () => {
      const current = editing,
        t = structuredClone(current.value);
      t.id = t.id.trim();
      t.name = t.name.trim();
      if (templates().some((x) => x.id === t.id && x.id !== current.id)) {
        problem("模板标识已存在。");
        return;
      }
      try {
        await api("/api/inspection-test", "POST", {
          template: t,
          method: "GET",
          path: "/",
        });
      } catch (e) {
        problem(e.message);
        return;
      }
      if (editing !== current) return;
      // Shared edits add required protocols without replacing existing overrides.
      if (current.id)
        for (const ch of usages(current.id))
          ch.protocols[t.protocol] ||= { compatibility: "standard_v1" };
      if (current.id)
        draft.inspection_templates = templates().map((x) =>
          x.id === current.id ? t : x,
        );
      else (draft.inspection_templates ||= []).push(t);
      close(true);
      mark();
      renderChannels();
      render();
    });
  });
  $("template-search").addEventListener("input", render);
  for (const id of ["template-protocol-filter", "template-usage-filter"])
    $(id).addEventListener("change", render);
  window.inspectionTools = {
    render,
    renderLinks,
    changed,
    clear() {
      editing = null;
      presets = null;
      for (const id of [
        "template-search",
        "template-protocol-filter",
        "template-usage-filter",
      ])
        $(id).value = "";
    },
  };
})();
