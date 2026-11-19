"use strict";
// Uses the console's authenticated API, shared draft and full-version publisher.
(() => {
  let editing = null,
    initial = null,
    form = null,
    testRevision = 0;
  let cursor = "",
    query = null,
    generation = 0,
    loading = false;
  const rules = () => draft.error_rules || [];
  const names = {
    http: "HTTP 错误",
    json: "200 JSON 错误",
    sse: "SSE 错误事件",
  };
  const reasons = {
    rule_matched: "规则命中",
    unmatched_error: "未命中自定义规则",
    error_body_unreadable: "错误正文读取失败或超限",
    unsupported_response: "不支持的响应格式",
    invalid_encoding: "压缩响应无效",
    json_inspection_limit_or_read_failed: "JSON 检查超限或读取失败",
    invalid_json: "JSON 格式无效",
    sse_inspection_limit_or_read_failed: "SSE 检查超限或读取失败",
    invalid_sse_data: "SSE 数据格式无效",
  };
  errors.original_decryption_failed =
    "原文解密失败，请检查节点主密钥；元数据仍可查询。";
  errors.interception_expired_or_missing =
    "这条日志已过期清理或不存在，请刷新列表。";
  errors.log_database_unavailable =
    "日志数据库暂时不可用；不能据此判断没有拦截。";
  errors.invalid_error_sample = "样本状态、来源或渠道协议不一致，请检查输入。";
  function modal(id, title, drawer = false) {
    const d = el(
      "dialog",
      undefined,
      drawer ? "drawer rule-drawer" : "details-modal",
    );
    d.id = id;
    const head = el("div", undefined, "drawer-header"),
      body = el("div", undefined, drawer ? "drawer-body" : "modal-body");
    const heading = el("h2", title);
    heading.id = id + "-title";
    d.setAttribute("aria-labelledby", heading.id);
    const close = button("✕", () => closeEditor(d), "quiet");
    close.setAttribute("aria-label", "关闭" + title);
    head.append(heading, close);
    d.append(head, body);
    document.querySelector("main").append(d);
    return { d, body, head };
  }
  const editorModal = modal("er-editor", "错误规则", true),
    info = modal("er-info", "详情");
  const { d: dialog, body } = editorModal;
  const editorForm = el("form");
  editorForm.noValidate = true;
  const footer = el("div", undefined, "drawer-actions");
  footer.append(
    el("span", "保存后需发布完整配置才会生效", "muted small"),
    button("取消", () => closeEditor(dialog)),
  );
  const save = button("保存到草稿", () => {});
  save.className = "";
  save.type = "submit";
  footer.append(save);
  editorForm.append(editorModal.head, body, footer);
  dialog.append(editorForm);
  const tabs = el("div", undefined, "er-editor-tabs"),
    configure = el("div"),
    test = el("div"),
    feedbackNode = el("p", undefined, "form-error");
  feedbackNode.id = "er-editor-feedback";
  feedbackNode.setAttribute("role", "alert");
  function editorFeedback(message = "") {
    feedbackNode.textContent = message;
    feedbackNode.hidden = !message;
  }
  editorFeedback();
  const configureButton = button("规则配置", () => tab(false)),
    testButton = button("样本试匹配", () => tab(true));
  tabs.append(configureButton, testButton);
  body.append(tabs, configure, test, feedbackNode);
  function tab(testing) {
    configure.hidden = testing;
    test.hidden = !testing;
    configureButton.classList.toggle("active", !testing);
    testButton.classList.toggle("active", testing);
  }
  function changed() {
    return (
      !!editing &&
      (!equal(editing, initial) ||
        form.statuses !==
          (initial.statuses || [])
            .map((s) => (s.from === s.to ? s.from : `${s.from}-${s.to}`))
            .join(", "))
    );
  }
  function closeEditor(d) {
    if (d === dialog && changed() && !confirm("放弃当前规则尚未保存的修改？"))
      return;
    d.close();
    if (d === dialog) editing = null;
  }
  dialog.addEventListener("cancel", (e) => {
    e.preventDefault();
    closeEditor(dialog);
  });
  function update() {
    mark();
    render();
  }
  function statusText(r) {
    return (
      (r.statuses || [])
        .map((v) => (v.from === v.to ? v.from : `${v.from}–${v.to}`))
        .join(", ") || "所有错误"
    );
  }
  function ordered() {
    return rules()
      .filter((r) => r.channels?.length)
      .concat(rules().filter((r) => !r.channels?.length));
  }
  function render() {
    $("er-inspect").checked = !!draft.inspect_embedded_errors;
    $("inspection-summary").textContent = draft.inspect_embedded_errors
      ? "草稿已开启正文检查，仅对命中 JSON / SSE 模板的接口生效；未命中的接口默认透传。"
      : "草稿未开启正文检查；仅对关联模板命中的接口处理 HTTP 错误。未命中的接口默认透传，模板关联在渠道编辑中设置。";
    $("error-rule-count").textContent = rules().length;
    const list = $("er-list");
    list.replaceChildren();
    const search = $("er-search").value.toLowerCase(),
      state = $("er-state").value;
    const filtered = ordered().filter(
      (r) =>
        JSON.stringify([r.name, r.id, r.groups])
          .toLowerCase()
          .includes(search) &&
        (!state || r.enabled === (state === "enabled")),
    );
    for (const r of filtered) {
      const row = el("tr"),
        order = el("td"),
        buttons = el("div", undefined, "er-order");
      const group = rules().filter(
          (x) => !!x.channels?.length === !!r.channels?.length,
        ),
        index = group.indexOf(r);
      buttons.append(el("span", index + 1, "muted"));
      for (const [label, step] of [
        ["↑", -1],
        ["↓", 1],
      ]) {
        const b = button(
          label,
          () => {
            if (busy) return;
            const other = group[index + step];
            if (!other) return;
            const a = rules().indexOf(r),
              b = rules().indexOf(other);
            [draft.error_rules[a], draft.error_rules[b]] = [
              draft.error_rules[b],
              draft.error_rules[a],
            ];
            update();
          },
          "quiet",
        );
        b.disabled = !group[index + step];
        b.setAttribute("aria-label", (step < 0 ? "上移" : "下移") + r.name);
        buttons.append(b);
      }
      order.append(buttons);
      const name = el("td");
      name.append(
        el("strong", r.name),
        el(
          "span",
          r.id +
            " · " +
            ((r.groups || [])
              .map((g) =>
                g.conditions
                  .map((c) => c.values?.join(" / ") || "字段存在")
                  .join(" 且 "),
              )
              .join(" 或 ") || "仅按范围匹配"),
          "er-sub",
        ),
      );
      const pending = pendingChanges().find(
        (x) => x.kind === "错误规则" && x.id === r.id,
      );
      if (pending)
        name.append(el("span", pending.action + "待发布", "pending-label"));
      const scope = el("td");
      scope.append(
        el(
          "span",
          r.channels?.length ? r.channels.join(", ") : "公共规则",
          "badge",
        ),
        el(
          "span",
          r.protocols?.length
            ? r.protocols.map((p) => shortLabels[p]).join(" / ")
            : "全部协议",
          "er-sub",
        ),
      );
      const enabled = el("td"),
        toggle = button(
          "",
          () => {
            if (busy) return;
            r.enabled = !r.enabled;
            update();
          },
          "er-toggle",
        );
      toggle.setAttribute("role", "switch");
      toggle.setAttribute("aria-checked", String(r.enabled));
      toggle.setAttribute("aria-label", "启用" + r.name);
      enabled.append(toggle);
      const actions = el("td");
      actions.append(
        button("编辑", () => open(r), "quiet"),
        button(
          "删除",
          () => {
            if (
              busy ||
              !confirm(`从草稿删除「${r.name}」？发布后生效，历史日志仍保留。`)
            )
              return;
            draft.error_rules = rules().filter((x) => x !== r);
            update();
          },
          "quiet danger",
        ),
      );
      row.append(
        order,
        name,
        scope,
        el("td", statusText(r), "mono"),
        el("td", r.return_status || "保留原状态"),
        enabled,
        actions,
      );
      list.append(row);
    }
    if (!filtered.length) {
      const row = el("tr"),
        cell = el(
          "td",
          "暂无符合条件的规则。可从预设添加；通用安全兜底始终生效。",
          "empty",
        );
      cell.colSpan = 7;
      row.append(cell);
      list.append(row);
    }
    $("er-count").textContent =
      `${filtered.length} 条规则 · ${rules().filter((r) => r.enabled).length} 条启用`;
    const selection = $("er-log-channel").value;
    $("er-log-channel").replaceChildren(new Option("全部渠道", ""));
    for (const ch of draft.channels)
      $("er-log-channel").add(new Option(ch.name, ch.id));
    if (selection && !draft.channels.some((ch) => ch.id === selection))
      $("er-log-channel").add(
        new Option(selection + "（历史渠道）", selection),
      );
    $("er-log-channel").value = selection;
  }
  function select(label, choices, value, onchange) {
    const l = el("label", label),
      s = el("select");
    for (const [id, name] of choices) s.add(new Option(name, id));
    s.setAttribute("aria-label", label);
    s.value = value;
    s.onchange = () => onchange(s.value);
    l.append(s);
    return l;
  }
  function textarea(label, value, onchange, max = 1024) {
    const l = el("label", label),
      t = el("textarea");
    t.value = value;
    t.rows = 3;
    t.maxLength = max;
    t.oninput = () => onchange(t.value);
    l.append(t);
    return l;
  }
  function checks(items, selected, onchange) {
    const div = el("div", undefined, "er-rule-choices");
    for (const [id, name] of items)
      div.append(
        check(name, selected.includes(id), (on) => {
          onchange(on ? [...selected, id] : selected.filter((x) => x !== id));
        }),
      );
    return div;
  }
  function parseStatuses(value) {
    if (!value.trim()) return [];
    return value.split(/[,，]/).map((s) => {
      const match = s.trim().match(/^(\d{3})(?:\s*[-–]\s*(\d{3}))?$/);
      if (!match) throw Error("状态码格式：402, 403 或 400-499。");
      const from = Number(match[1]),
        to = Number(match[2] || match[1]);
      if (from < 100 || to > 599 || to < from)
        throw Error("状态范围必须在 100–599 内，起始值不能大于结束值。");
      return { from, to };
    });
  }
  function open(rule) {
    if (busy) return;
    editing = structuredClone(
      rule || {
        id: "rule-" + crypto.randomUUID().slice(0, 8),
        name: "",
        enabled: true,
        channels: [],
        protocols: [],
        sources: ["http", "json", "sse"],
        statuses: [],
        groups: [],
        ignore_case: true,
        return_status: 0,
        message: "上游服务暂时不可用，请联系管理员。",
      },
    );
    initial = structuredClone(editing);
    form = {
      isNew: !rules().some((r) => r.id === editing.id),
      scope: editing.channels?.length ? "selected" : "all",
      advanced: false,
      statuses: (editing.statuses || [])
        .map((s) => (s.from === s.to ? s.from : `${s.from}-${s.to}`))
        .join(", "),
    };
    editorFeedback();
    drawConfigure();
    drawTest();
    tab(false);
    dialog.showModal();
    body.scrollTop = 0;
  }
  function drawConfigure() {
    configure.replaceChildren();
    const grid = el("div", undefined, "grid");
    grid.append(field("规则名称", editing.name, (v) => (editing.name = v)));
    const advanced = el("details", undefined, "advanced-settings");
    advanced.open = form.advanced;
    const currentForm = form;
    advanced.addEventListener("toggle", () => {
      if (advanced.isConnected) currentForm.advanced = advanced.open;
    });
    const restricted =
      (editing.protocols || []).length || editing.sources.length !== 3;
    advanced.append(
      el(
        "summary",
        "高级匹配设置" + (restricted ? " · 已设置协议或错误来源限制" : ""),
      ),
    );
    const ruleID = field("规则 ID", editing.id, (v) => (editing.id = v));
    ruleID.querySelector("input").disabled = !form.isNew;
    advanced.append(ruleID);
    configure.append(grid);
    configure.append(
      el("h3", "1 · 匹配什么错误"),
      el(
        "p",
        "只处理接口模板已选中或正文检查已识别的错误。这里的状态条件不会扩大接口模板的检查范围。",
        "muted small",
      ),
    );
    configure.append(
      select(
        "适用渠道",
        [
          ["all", "全部渠道（公共规则）"],
          ["selected", "指定渠道（优先匹配）"],
        ],
        form.scope,
        (v) => {
          form.scope = v;
          if (v === "all") editing.channels = [];
          drawConfigure();
        },
      ),
    );
    if (form.scope === "selected") {
      if (!draft.channels.length)
        configure.append(el("p", "请先新增渠道。", "form-error"));
      configure.append(
        checks(
          draft.channels.map((ch) => [ch.id, ch.name]),
          editing.channels || [],
          (v) => {
            editing.channels = v;
            drawConfigure();
          },
        ),
      );
    }
    advanced.append(
      el("p", "适用协议（不勾选表示全部）", "muted small"),
      checks(Object.entries(labels), editing.protocols || [], (v) => {
        editing.protocols = v;
        drawConfigure();
      }),
    );
    advanced.append(
      el("p", "错误来源", "muted small"),
      checks(Object.entries(names), editing.sources, (v) => {
        editing.sources = v;
        drawConfigure();
      }),
    );
    configure.append(
      advanced,
      field(
        "匹配上游 HTTP 状态",
        form.statuses,
        (v) => {
          form.statuses = v;
        },
        "text",
        "402, 403 或 400-499；留空匹配所有已识别错误",
      ),
    );
    const details = el("details");
    details.open = !!form.conditionsOpen;
    details.className = "advanced-settings";
    details.addEventListener("toggle", () => {
      if (details.isConnected) currentForm.conditionsOpen = details.open;
    });
    details.append(
      el(
        "summary",
        `错误内容条件 · ${(editing.groups || []).length} 组${editing.groups?.length ? "（满足任一组，组内全部满足）" : "（未设置，按以上范围匹配）"}`,
      ),
    );
    (editing.groups || []).forEach((group, gi) => {
      const box = el("div", undefined, "er-condition"),
        head = el("div", undefined, "er-condition-title");
      head.append(
        el("strong", `条件组 ${gi + 1}`),
        button(
          "删除组",
          () => {
            editing.groups.splice(gi, 1);
            drawConfigure();
          },
          "quiet danger",
        ),
      );
      box.append(head);
      group.conditions.forEach((condition, ci) => {
        const row = el("div", undefined, "er-condition-row");
        row.append(
          select(
            "字段",
            [
              ["code", "错误代码"],
              ["message", "错误消息"],
              ["type", "错误类型"],
            ],
            condition.field,
            (v) => (condition.field = v),
          ),
          select(
            "匹配",
            [
              ["equals", "等于任一值"],
              ["contains", "包含任一值"],
              ["exists", "字段存在"],
            ],
            condition.operator,
            (v) => {
              condition.operator = v;
              condition.values = v === "exists" ? [] : [""];
              drawConfigure();
            },
          ),
        );
        if (condition.operator !== "exists")
          row.append(
            textarea(
              "匹配值（每行一个）",
              (condition.values || []).join("\n"),
              (v) => (condition.values = v.split("\n")),
              4096,
            ),
          );
        else row.append(el("span", "无需填写值", "muted small"));
        row.append(
          button(
            "×",
            () => {
              group.conditions.splice(ci, 1);
              if (!group.conditions.length) editing.groups.splice(gi, 1);
              drawConfigure();
            },
            "quiet danger",
          ),
        );
        box.append(row);
      });
      if (group.conditions.length < 8)
        box.append(
          button(
            "＋ 同时满足的条件",
            () => {
              group.conditions.push({
                field: "message",
                operator: "contains",
                values: [""],
              });
              drawConfigure();
            },
            "quiet",
          ),
        );
      details.append(box);
    });
    if ((editing.groups || []).length < 8)
      details.append(
        button(
          "＋ 添加备选条件组",
          () => {
            editing.groups ||= [];
            editing.groups.push({
              conditions: [
                { field: "message", operator: "contains", values: [""] },
              ],
            });
            drawConfigure();
          },
          "secondary",
        ),
      );
    details.append(
      el("p", "无内容条件时，仅按渠道、协议、来源和状态匹配。", "muted small"),
      check(
        "忽略英文大小写",
        editing.ignore_case,
        (v) => (editing.ignore_case = v),
      ),
    );
    configure.append(details);
    configure.append(
      el("h3", "2 · 返回什么结果"),
      select(
        "返回 HTTP 状态",
        [
          ["preserve", "保留原错误状态（非 4xx/5xx 使用 502）"],
          ["override", "指定错误状态"],
        ],
        editing.return_status ? "override" : "preserve",
        (v) => {
          editing.return_status = v === "override" ? 503 : 0;
          drawConfigure();
        },
      ),
    );
    if (editing.return_status)
      configure.append(
        field(
          "指定状态码（400–599）",
          editing.return_status,
          (v) => (editing.return_status = Number(v)),
          "number",
        ),
      );
    configure.append(
      textarea("对外公开文案", editing.message, (v) => (editing.message = v)),
      el(
        "p",
        "固定文案不会插入原始错误。SSE 已开始时保留实际 HTTP 状态，发送安全错误事件并结束。",
        "callout",
      ),
      check("发布后启用", editing.enabled, (v) => (editing.enabled = v)),
    );
  }
  function prepared() {
    const r = structuredClone(editing);
    r.statuses = parseStatuses(form.statuses);
    if (form.scope === "selected" && !r.channels?.length)
      throw Error("请选择至少一个渠道。");
    if (!r.name.trim() || !r.message.trim())
      throw Error("请填写规则名称和公开文案。");
    if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(r.id))
      throw Error("规则 ID 使用小写字母、数字、下划线或短横线，最长 64 位。");
    if (form.isNew && rules().some((x) => x.id === r.id))
      throw Error("规则 ID 已存在。");
    for (const group of r.groups || [])
      for (const c of group.conditions) {
        c.values = (c.values || []).map((v) => v.trim()).filter(Boolean);
        if (c.operator !== "exists" && !c.values.length)
          throw Error("请填写条件值，或删除空条件组。");
      }
    if (!r.sources.length) throw Error("请选择至少一种错误来源。");
    if (
      r.return_status &&
      (!Number.isInteger(r.return_status) ||
        r.return_status < 400 ||
        r.return_status > 599)
    )
      throw Error("返回状态必须为 400–599 的整数。");
    return r;
  }
  function testConfig(r) {
    const cfg = structuredClone(draft);
    cfg.error_rules = rules().filter((x) => x.id !== initial.id);
    const index = rules().findIndex((x) => x.id === initial.id);
    cfg.error_rules.splice(index < 0 ? cfg.error_rules.length : index, 0, r);
    return cfg;
  }
  editorForm.onsubmit = async (e) => {
    e.preventDefault();
    if (busy) return;
    save.disabled = true;
    editorFeedback();
    try {
      const r = prepared();
      const cfg = testConfig(r);
      // Server validates exactly the same schema as publication, without calling upstream.
      const ch = cfg.channels[0];
      if (!ch) throw Error("请先新增至少一个渠道，再保存规则。");
      await api("/api/error-test", "POST", {
        config: cfg,
        sample: {
          channel: ch.id,
          protocol: Object.keys(ch.protocols)[0],
          source: "normal",
          status: 200,
          error: {},
        },
      });
      draft.error_rules = cfg.error_rules;
      editing = null;
      dialog.close();
      update();
      draftNotice();
    } catch (e) {
      editorFeedback(e.message);
    } finally {
      save.disabled = false;
    }
  };
  function drawTest() {
    test.replaceChildren();
    const sample = {
      channel: draft.channels[0]?.id || "",
      protocol:
        Object.keys(draft.channels[0]?.protocols || {})[0] || "openai_chat",
      source: "http",
      status: 402,
      committed: false,
      error: {
        code: "insufficient_quota",
        type: "",
        message: "Your credit balance is too low.",
      },
    };
    const result = el("div", undefined, "er-test-result");
    result.hidden = true;
    const paint = () => {
      test.replaceChildren();
      test.append(
        el(
          "p",
          "测试当前表单替换草稿后的完整规则链。仅处理已提取的错误字段，不发起上游请求，也不记录样本。",
          "callout",
        ),
      );
      test.append(
        select(
          "场景",
          [
            ["balance", "余额不足"],
            ["sse", "SSE 中途报错"],
            ["normal", "正常回答提到余额不足"],
          ],
          sample.source === "normal"
            ? "normal"
            : sample.source === "sse"
              ? "sse"
              : "balance",
          (v) => {
            sample.source = v === "balance" ? "http" : v;
            sample.status = v === "balance" ? 402 : 200;
            sample.committed = v === "sse";
            sample.error.message =
              v === "normal"
                ? "余额不足时可以在账户设置中充值。"
                : "Your credit balance is too low.";
            paint();
          },
        ),
      );
      test.append(
        select(
          "渠道",
          draft.channels.map((ch) => [ch.id, ch.name]),
          sample.channel,
          (v) => {
            sample.channel = v;
            sample.protocol =
              Object.keys(
                draft.channels.find((ch) => ch.id === v)?.protocols || {},
              )[0] || "";
            paint();
          },
        ),
        select(
          "协议",
          Object.keys(
            draft.channels.find((ch) => ch.id === sample.channel)?.protocols ||
              {},
          ).map((p) => [p, labels[p]]),
          sample.protocol,
          (v) => (sample.protocol = v),
        ),
        select(
          "识别来源",
          [...Object.entries(names), ["normal", "正常回答"]],
          sample.source,
          (v) => {
            sample.source = v;
            if (v !== "sse") sample.committed = false;
            paint();
          },
        ),
        field(
          "上游状态",
          sample.status,
          (v) => (sample.status = Number(v)),
          "number",
        ),
        field("错误代码", sample.error.code, (v) => (sample.error.code = v)),
        field("错误类型", sample.error.type, (v) => (sample.error.type = v)),
        textarea(
          "错误消息 / 正常回答",
          sample.error.message,
          (v) => (sample.error.message = v),
          64000,
        ),
        check(
          "SSE 已开始（HTTP 200 已提交）",
          sample.committed,
          (v) => (sample.committed = v),
        ),
      );
      const run = button("运行试匹配", async () => {
        const revision = ++testRevision;
        run.disabled = true;
        try {
          const response = await api("/api/error-test", "POST", {
            config: testConfig(prepared()),
            sample,
          });
          if (revision !== testRevision || !dialog.open) return;
          result.hidden = false;
          result.textContent = !response.intercepted
            ? response.reason === "normal_response"
              ? "正常回答，不参与错误匹配，不产生拦截日志。"
              : "未开启 200 JSON / SSE 检查，此响应按原逻辑转发。"
            : `${response.decision.rule_id ? "命中规则：" + response.decision.rule_id + " · 条件组 " + response.decision.group : "通用安全兜底"}\n上游 ${sample.status} → 拟返回 ${response.decision.status} → 实际 ${response.actual_status}\n${response.status_override_skipped ? "HTTP 已提交，不能改写状态；发送安全错误事件并结束流。\n" : ""}\n${response.decision.message}`;
        } catch (e) {
          if (revision === testRevision) {
            result.hidden = false;
            result.textContent = e.message;
          }
        } finally {
          run.disabled = false;
        }
      });
      test.append(run, result);
    };
    paint();
  }
  editorForm.addEventListener("input", () => {
    testRevision++;
    const result = test.querySelector(".er-test-result");
    if (result) result.hidden = true;
  });
  $("er-add").onclick = () => open();
  $("er-presets").onclick = () =>
    action(async () => {
      const presets = await api("/api/error-presets");
      info.body.replaceChildren(
        el("p", "预设复制后可自由编辑，请使用实际脱敏样本验证。", "muted"),
      );
      $("er-info-title").textContent = "从预设添加";
      for (const preset of presets)
        info.body.append(
          button(
            preset.name + " → " + (preset.return_status || "保留原状态"),
            () => {
              info.d.close();
              const rule = structuredClone(preset);
              if (rules().some((r) => r.id === rule.id))
                rule.id += "-" + crypto.randomUUID().slice(0, 6);
              open(rule);
            },
            "secondary er-preset",
          ),
        );
      info.d.showModal();
    });
  $("er-search").oninput = render;
  $("er-state").onchange = render;
  $("er-inspect").onchange = () => {
    if (busy) {
      render();
      return;
    }
    draft.inspect_embedded_errors = $("er-inspect").checked;
    update();
    window.inspectionTools?.render();
  };
  function status(state) {
    const node = $("er-log-health");
    node.replaceChildren();
    let warning = false;
    if (!state.nodes.length)
      node.append(el("p", "尚无节点心跳，日志状态未知。"));
    for (const n of state.nodes) {
      const s = n.logs || {};
      const abnormal =
        !n.online ||
        (s.dropped || 0) > 0 ||
        (s.pending || 0) > 0 ||
        (s.last_failure_unix || 0) > (s.last_success_unix || 0);
      warning ||= abnormal;
      const last = s.last_success_unix
        ? new Date(s.last_success_unix * 1000).toLocaleString()
        : "暂无成功写入";
      node.append(
        el(
          "p",
          `${n.id} · 待写 ${s.pending || 0} · 排队载荷 ${Math.ceil((s.pending_bytes || 0) / 1024)} KiB · 本进程丢弃 ${s.dropped || 0} · 写入失败 ${s.write_failures || 0} · 最后成功 ${last}${n.online ? "" : " · 节点离线，状态未知"}`,
        ),
      );
    }
    node.classList.toggle("warn", warning);
    if (warning)
      node.append(
        el(
          "p",
          "日志可能延迟或不完整；查询不到不代表请求未被拦截。心跳最多约 5 秒更新，进程崩溃前的未上报记录可能丢失。",
          "muted small",
        ),
      );
  }
  function details(title, fields, note) {
    $("er-info-title").textContent = title;
    info.body.replaceChildren();
    const dl = el("dl", undefined, "er-definition");
    for (const [name, value] of fields)
      dl.append(el("dt", name), el("dd", String(value)));
    info.body.append(dl, el("p", note, "callout"));
    info.d.showModal();
  }
  $("er-policy").onclick = () =>
    details(
      "记录策略",
      [
        ["记录范围", "每个被拦截请求至多一条"],
        ["每节点队列", "最多 1,024 条且编码载荷最多 1 MiB，含重试批次"],
        ["原始错误", "每条最多 2 KiB；加密保存，管理员按需查看"],
        ["后台写入", "每秒最多 1 批，每批最多 50 条；独立连接池最多 1 个连接"],
        ["保留目标", "最近 3 天 / 最近 50,000 条；每分钟小批量清理"],
        ["队列满时", "丢弃新增明细，继续执行拦截"],
        ["故障重试", "指数退避，最长 30 秒；正常关闭最多排空 2 秒"],
      ],
      "资源策略不随规则版本回滚。保留数量是定时清理目标，并非数据库磁盘硬上限；不保证故障时日志零丢失。",
    );
  function logDetails(log) {
    details(
      "拦截详情",
      [
        ["时间", new Date(log.time).toLocaleString()],
        ["请求编号", log.request_id],
        ["渠道 / 协议", log.channel + " / " + labels[log.protocol]],
        ["节点 / 实例", log.node + " / " + log.instance],
        ["历史版本", "V" + log.version],
        ["规则 ID", log.rule_id || "通用兜底"],
        ["条件组", log.group || "无内容条件"],
        ["原因", reasons[log.reason] || log.reason],
        ["错误来源", names[log.source]],
        ["上游状态", log.upstream_status],
        ["拟返回状态", log.desired_status],
        ["实际状态", log.actual_status],
        [
          "处理方式",
          log.action === "safe_sse_error"
            ? "发送安全错误事件并结束流"
            : "返回安全错误响应",
        ],
      ],
      log.action === "safe_sse_error"
        ? "HTTP 状态已提交，无法改写。此请求是流内失败。下方可查看触发拦截的原始事件，并结合历史版本核对规则。"
        : "下方可查看被拦截的上游响应片段，并结合历史版本核对规则。原文不向下游透传。",
    );
    const section = el("details", undefined, "er-original");
    section.append(el("summary", "上游原始错误 · 仅管理员可见"));
    const hint = el(
      "p",
      "最多保留 2 KiB；加密存储。查看后可对照规则排查误拦。",
      "muted small",
    );
    const content = el("pre", undefined, "er-original-content");
    content.hidden = true;
    const load = button(
      "查看原始错误",
      async () => {
        load.disabled = true;
        try {
          const data = await api(
            "/api/interceptions/" + encodeURIComponent(log.id) + "/original",
          );
          if (!section.isConnected) return;
          if (data.state !== "captured") {
            const states = {
              legacy: "这条旧日志没有采集原文，无法补回。",
              unavailable: "未能取得可保存的正文，例如编码不支持或读取失败。",
              encryption_failed: "原文加密失败，已保留拦截元数据。",
            };
            hint.textContent = states[data.state] || "原文不可用。";
          } else {
            hint.textContent =
              (data.truncated
                ? "已截断或读取不完整，仅展示保留片段。"
                : "已保留完整错误正文。") +
              (data.redacted
                ? " 检测到凭证并做了脱敏。"
                : " 未命中自动凭证脱敏规则。") +
              " 普通错误字段保留，便于核对误拦。";
            content.textContent = data.original || "（上游正文为空）";
            content.hidden = false;
          }
          load.hidden = true;
        } catch (e) {
          hint.textContent = e.message;
          load.disabled = false;
        }
      },
      "secondary",
    );
    section.append(hint, load, content);
    info.body.append(section);
  }
  async function loadLogs(next = false) {
    if (loading) return;
    loading = true;
    const requestGeneration = ++generation;
    $("er-log-error").hidden = true;
    $("er-log-next").disabled = true;
    if (!next) {
      const until = new Date(),
        since = new Date(until - Number($("er-log-time").value) * 3600000);
      query = new URLSearchParams({
        since: since.toISOString(),
        until: until.toISOString(),
        limit: "50",
      });
      for (const [key, id] of [
        ["request_id", "er-request"],
        ["rule", "er-log-rule"],
        ["channel", "er-log-channel"],
        ["node", "er-log-node"],
        ["status", "er-log-status"],
      ]) {
        if ($(id).value.trim()) query.set(key, $(id).value.trim());
      }
      cursor = "";
    }
    const params = new URLSearchParams(query);
    if (next && cursor) params.set("cursor", cursor);
    try {
      const page = await api("/api/interceptions?" + params);
      if (requestGeneration !== generation) return;
      const list = $("er-log-list");
      list.replaceChildren();
      for (const log of page.events) {
        const row = el("tr"),
          time = el("td"),
          channel = el("td"),
          rule = el("td"),
          node = el("td"),
          statusCell = el("td"),
          actions = el("td");
        time.append(
          el("span", new Date(log.time).toLocaleString()),
          el("span", log.request_id, "er-sub mono"),
        );
        channel.append(
          el(
            "span",
            draft.channels.find((ch) => ch.id === log.channel)?.name ||
              log.channel,
          ),
          el("span", labels[log.protocol], "er-sub"),
        );
        rule.append(
          el("span", log.rule_id || "通用兜底"),
          el("span", reasons[log.reason] || log.reason, "er-sub"),
        );
        statusCell.append(
          el("span", `${log.upstream_status} → ${log.actual_status}`, "mono"),
        );
        statusCell.append(
          el(
            "span",
            log.action === "safe_sse_error"
              ? "已替换为安全错误事件 · HTTP 已提交"
              : "已替换为安全错误响应",
            "er-sub",
          ),
        );
        node.append(
          el("span", log.node),
          el("span", "V" + log.version, "er-sub"),
        );
        actions.append(button("详情", () => logDetails(log), "quiet"));
        row.append(time, channel, rule, statusCell, node, actions);
        list.append(row);
      }
      cursor = page.next_cursor || "";
      $("er-log-next").disabled = !cursor;
      $("er-log-meta").textContent =
        `本页 ${page.events.length} 条 · 查询截至 ${new Date(query.get("until")).toLocaleTimeString()}${page.events.length ? "" : " · 当前范围没有已写入记录"}`;
    } catch (e) {
      if (requestGeneration === generation) {
        $("er-log-error").hidden = false;
        $("er-log-error").textContent = e.message;
        $("er-log-meta").textContent = "查询失败；上次显示的记录可能已过期。";
      }
    } finally {
      if (requestGeneration === generation) loading = false;
    }
  }
  $("er-log-refresh").onclick = () => loadLogs();
  $("er-log-query").onclick = () => loadLogs();
  $("er-log-next").onclick = () => loadLogs(true);
  window.errorTools = {
    render,
    status,
    logs: () => {
      render();
      loadLogs();
    },
    changed,
    clear: () => {
      generation++;
      loading = false;
      testRevision++;
      editing = null;
      dialog.close();
      info.d.close();
      info.body.replaceChildren();
      configure.replaceChildren();
      test.replaceChildren();
      $("er-log-list").replaceChildren();
      $("er-log-health").replaceChildren();
      cursor = "";
      query = null;
    },
  };
})();
