// Requires an empty, dedicated acceptance database. Never run against user data.
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright");
const base = process.env.GATEWAY_RULES_CHECK_ADMIN;
const data = process.env.GATEWAY_RULES_CHECK_DATA;
const second = process.env.GATEWAY_RULES_CHECK_SECOND_ADMIN;
const secondData = process.env.GATEWAY_RULES_CHECK_SECOND_DATA;
const token = fs
  .readFileSync(process.env.GATEWAY_RULES_CHECK_TOKEN_FILE, "utf8")
  .trim();
const output =
  process.env.GATEWAY_BROWSER_OUTPUT || "build/error-rules-browser";
async function api(url, method = "GET", body) {
  const response = await fetch(url, {
    method,
    headers: {
      Authorization: "Bearer " + token,
      ...(body ? { "Content-Type": "application/json" } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  assert(response.ok, "API status " + response.status);
  return response.json();
}
(async () => {
  assert(
    base && data && second,
    "Dedicated admin/data/second admin URLs required",
  );
  assert.equal(
    (await api(base + "/api/status")).latest_version,
    0,
    "Refusing nonempty database",
  );
  let modelCalls = 0;
  const mock = http.createServer((req, res) => {
    if (req.url.startsWith("/v1/models")) {
      modelCalls++;
      assert.equal(req.method, "GET");
      assert.equal(req.headers.authorization, "Bearer test-placeholder");
      if (!req.url.includes("fail=1")) {
        res.writeHead(200, {
          "Content-Type": "application/json",
          "X-Model-Catalog": "preserved",
        });
        res.end(
          JSON.stringify({
            object: "list",
            data: [{ id: "sample-model", object: "model" }],
          }),
        );
        return;
      }
    }
    const status = Number(
      new URL(req.url, "http://mock").searchParams.get("status") || 403,
    );
    res.writeHead(status, {
      "Content-Type": "application/json",
      Location: "/never-follow",
    });
    res.end(
      JSON.stringify({
        error: {
          code: "insufficient_quota",
          message:
            "PRIVATE_BALANCE_ADMIN_ONLY <script>window.originalXSS=true</script>",
          api_key: "SAMPLE_API_KEY",
        },
      }),
    );
  });
  await new Promise((resolve) => mock.listen(0, "127.0.0.1", resolve));
  const upstream = "http://127.0.0.1:" + mock.address().port;
  const cfg = {
    schema_version: 7,
    inspection_templates: await api(base + "/api/inspection-presets"),
    inspect_embedded_errors: false,
    channels: [
      {
        id: "test-provider",
        name: "测试上游",
        allow_http: true,
        base_url: upstream,
        protocols: { openai_chat: { auth: "bearer" }, anthropic_messages: {} },
        inspection_templates: ["openai-chat", "anthropic-messages"],
      },
    ],
  };
  await api(base + "/api/publish", "POST", {
    base_version: 0,
    note: "isolated browser acceptance",
    config: cfg,
  });
  fs.mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({
    headless: true,
    args: ["--no-sandbox"],
  });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("dialog", (d) => d.accept());
  try {
    await page.goto(base);
    await page.locator("#admin-token").fill(token);
    await page.locator("#login-submit").click();
    await page.locator("#workspace").waitFor({ state: "visible" });
    await page.locator(".traffic-settings summary").click();
    await page.locator("#node-concurrency").fill("64");
    await page.locator("#upstream-concurrency").fill("128");
    assert.equal(
      await page.locator("#traffic-form").evaluate((f) => f.checkValidity()),
      false,
    );
    await page.locator("#pending-changes").click();
    assert.equal(
      (await api(base + "/api/status")).latest_version,
      1,
      "invalid limits must not publish",
    );
    await page.locator("#upstream-concurrency").fill("48");
    assert.match(
      await page.locator("#draft-state").innerText(),
      /1 项修改尚未发布/,
    );
    await page.screenshot({ path: path.join(output, "traffic-settings.png") });
    await page.locator('[data-tab="error-rules"]').click();
    await page.locator("#er-inspect").check();
    await page.locator("#er-presets").click();
    await page
      .getByRole("button", { name: "上游余额不足 → 503", exact: true })
      .click();
    const feedback = page.locator("#er-editor-feedback");
    assert.equal(await feedback.isVisible(), false);
    assert.equal(await feedback.textContent(), "");
    const ruleName = page.getByLabel("规则名称", { exact: true });
    const originalName = await ruleName.inputValue();
    await ruleName.fill(" ");
    await page
      .locator("#er-editor")
      .getByRole("button", { name: "保存到草稿", exact: true })
      .click();
    await feedback.waitFor({ state: "visible" });
    assert.match(await feedback.textContent(), /请填写规则名称/);
    await ruleName.fill(originalName);
    await page.getByRole("button", { name: "样本试匹配", exact: true }).click();
    await page.getByRole("button", { name: "运行试匹配", exact: true }).click();
    await page
      .locator(".er-test-result")
      .filter({ hasText: "拟返回 503" })
      .waitFor();
    await page.getByLabel("场景", { exact: true }).selectOption("normal");
    await page.getByRole("button", { name: "运行试匹配", exact: true }).click();
    await page
      .locator(".er-test-result")
      .filter({ hasText: "正常回答，不参与" })
      .waitFor();
    await page.getByLabel("场景", { exact: true }).selectOption("sse");
    await page.getByRole("button", { name: "运行试匹配", exact: true }).click();
    await page
      .locator(".er-test-result")
      .filter({ hasText: "实际 200" })
      .waitFor();
    await page.getByRole("button", { name: "规则配置", exact: true }).click();
    await page.screenshot({ path: path.join(output, "editor.png") });
    await page
      .locator("#er-editor")
      .getByRole("button", { name: "保存到草稿", exact: true })
      .click();
    await page.locator("#er-editor").waitFor({ state: "hidden" });
    assert.equal(await feedback.textContent(), "");
    assert.match(
      await page.locator("#draft-state").innerText(),
      /3 项修改尚未发布/,
    );
    await page.locator("#pending-changes").click();
    await page.locator("#publish").click();
    await page.locator("#latest").filter({ hasText: "V2" }).waitFor();
    assert.equal(
      await page.locator("#pending-changes").textContent(),
      "无待发布修改",
    );
    await page.locator("#reload-config").click();
    await page.locator("#reload-config").waitFor({ state: "visible" });
    await page.locator("#reload-config:not(:disabled)").waitFor();
    assert.equal(await page.locator("#er-list .pending-label").count(), 0);
    const deadline = Date.now() + 15000;
    let synced = false;
    while (Date.now() < deadline) {
      const status = await api(second + "/api/status");
      if (status.local.applied_version === 2) {
        synced = true;
        break;
      }
      await new Promise((r) => setTimeout(r, 300));
    }
    assert(synced, "second node sync");
    const syncedConfig = await api(second + "/api/versions/2");
    assert.deepEqual(syncedConfig.config.traffic_limits, {
      node_concurrency: 64,
      upstream_concurrency: 48,
    });
    await page.locator('[data-tab="channels"]').click();
    assert.equal(await page.locator("#node-concurrency").inputValue(), "64");
    assert.equal(
      await page.locator("#upstream-concurrency").inputValue(),
      "48",
    );
    await page.locator('[data-tab="error-rules"]').click();
    const result = await fetch(data + "/r/test-provider/v1/chat/completions", {
      method: "POST",
      headers: {
        Authorization: "Bearer test-placeholder",
        "Content-Type": "application/json",
      },
      body: "{}",
    });
    assert.equal(result.status, 503);
    assert(!(await result.text()).includes("PRIVATE_BALANCE"));
    await page.screenshot({ path: path.join(output, "rules.png") });
    await page.locator('[data-tab="intercept-logs"]').click();
    const loggedDeadline = Date.now() + 5000;
    while (Date.now() < loggedDeadline) {
      await page.locator("#er-log-refresh").click();
      if (await page.locator("#er-log-list tr").count()) break;
      await new Promise((r) => setTimeout(r, 200));
    }
    assert.equal(await page.locator("#er-log-list tr").count(), 1);
    assert.match(await page.locator("#er-log-list").textContent(), /balance/);
    assert(
      !(await page.locator("#er-log-list").textContent()).includes(
        "PRIVATE_BALANCE",
      ),
    );
    await page.screenshot({ path: path.join(output, "logs.png") });
    await page
      .locator("#er-log-list")
      .getByRole("button", { name: "详情", exact: true })
      .click();
    assert.match(await page.locator("#er-info").textContent(), /V2/);
    await page.locator(".er-original summary").click();
    await page
      .getByRole("button", { name: "查看原始错误", exact: true })
      .click();
    await page.locator(".er-original-content").waitFor({ state: "visible" });
    assert.match(
      await page.locator(".er-original-content").textContent(),
      /PRIVATE_BALANCE_ADMIN_ONLY/,
    );
    assert(
      !(await page.locator(".er-original-content").textContent()).includes(
        "SAMPLE_API_KEY",
      ),
    );
    assert.match(
      await page.locator(".er-original-content").textContent(),
      /REDACTED/,
    );
    assert.equal(await page.evaluate(() => window.originalXSS), undefined);
    await page.screenshot({ path: path.join(output, "detail.png") });
    await page.locator("#er-info .quiet").click();
    // Query failure is visible rather than an empty-success state.
    await page.route("**/api/interceptions?**", (route) =>
      route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ error: "log_database_unavailable" }),
      }),
    );
    await page.locator("#er-log-refresh").click();
    await page.locator("#er-log-error").waitFor({ state: "visible" });
    assert.match(await page.locator("#er-log-error").textContent(), /不可用/);
    await page.unroute("**/api/interceptions?**");
    await page.locator('[data-tab="error-rules"]').click();
    await page
      .locator("#er-list")
      .getByRole("button", { name: "编辑", exact: true })
      .click();
    await page.getByLabel("规则名称", { exact: true }).fill("余额不足统一处理");
    await page
      .locator("#er-editor")
      .getByRole("button", { name: "保存到草稿", exact: true })
      .click();
    await page.locator("#er-editor").waitFor({ state: "hidden" });
    await page.locator("#pending-changes").click();
    await page.locator("#publish").click();
    await page.locator("#latest").filter({ hasText: "V3" }).waitFor();
    await page.locator("#er-list").getByRole("switch").click();
    await page.locator("#pending-changes").click();
    await page.locator("#publish").click();
    await page.locator("#latest").filter({ hasText: "V4" }).waitFor();
    await page
      .locator("#er-list")
      .getByRole("button", { name: "删除", exact: true })
      .click();
    await page.locator("#pending-changes").click();
    await page.locator("#publish").click();
    await page.locator("#latest").filter({ hasText: "V5" }).waitFor();
    // Old logs retain the immutable version and rule ID even after deletion.
    await page.locator('[data-tab="intercept-logs"]').click();
    await page
      .locator("#er-log-list")
      .getByRole("button", { name: "详情", exact: true })
      .click();
    assert.match(await page.locator("#er-info").textContent(), /balance/);
    assert.match(await page.locator("#er-info").textContent(), /V2/);
    await page.locator("#er-info .quiet").click();
    // Auxiliary interfaces are now forwarded without registering paths.
    const modelHeaders = { Authorization: "Bearer test-placeholder" };
    for (const target of [data, secondData]) {
      const catalog = await fetch(
        target + "/r/test-provider/v1/models?limit=5",
        { headers: modelHeaders },
      );
      assert.equal(catalog.status, 200);
      assert.equal(catalog.headers.get("X-Model-Catalog"), "preserved");
      assert.equal((await catalog.json()).data[0].id, "sample-model");
      const failure = await fetch(
        target + "/r/test-provider/v1/models?fail=1",
        { headers: modelHeaders },
      );
      assert.equal(failure.status, 403);
      assert.match(await failure.text(), /PRIVATE_BALANCE_ADMIN_ONLY/);
    }
    await page.locator('[data-tab="inspection-templates"]').click();
    const templateRows = page.locator("#template-list .template-card");
    const templateTotal = await templateRows.count();
    assert.equal(templateTotal, 9);
    await page.locator("#template-search").fill("/v1/images/edits");
    assert.equal(await templateRows.count(), 1);
    await templateRows.locator('td[data-label="接口规则"] summary').click();
    assert.match(await templateRows.innerText(), /images\/edits/);
    await page.locator("#template-search").fill("no-such-template");
    assert(await page.locator("#template-empty").isVisible());
    await page.locator("#template-search").fill("");
    await page.locator("#template-protocol-filter").selectOption("gemini");
    assert.equal(await templateRows.count(), 1);
    await page.locator("#template-usage-filter").selectOption("used");
    assert.equal(await templateRows.count(), 0);
    await page.locator("#template-usage-filter").selectOption("unused");
    assert.equal(await templateRows.count(), 1);
    await page.locator("#template-protocol-filter").selectOption("");
    await page.locator("#template-usage-filter").selectOption("");
    assert.equal(await templateRows.count(), templateTotal);
    await page.screenshot({
      path: path.join(output, "template-table-desktop.png"),
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await templateRows
      .first()
      .locator('td[data-label="接口规则"] summary')
      .click();
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      ),
      false,
    );
    await page.screenshot({
      path: path.join(output, "template-table-mobile.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.locator("#create-template").click();
    await page.getByLabel("模板标识", { exact: true }).fill("catalog-check");
    await page.getByLabel("模板名称", { exact: true }).fill("模型目录错误检查");
    await page
      .getByLabel("检查协议", { exact: true })
      .selectOption("openai_responses");
    const match = page.locator("#template-matches fieldset").first();
    await match.getByLabel("请求方法", { exact: true }).selectOption("GET");
    await match.getByLabel("匹配方式", { exact: true }).selectOption("regex");
    await match.getByLabel("路径或表达式", { exact: true }).fill("[");
    await page.locator("#save-template").click();
    await page.locator("#template-editor-error").waitFor({ state: "visible" });
    assert.match(
      await page.locator("#template-editor-error").textContent(),
      /正则表达式无效/,
    );
    await match.getByLabel("路径或表达式", { exact: true }).fill("/v1/models");
    const statusChoice = page.getByLabel("HTTP 状态码", { exact: true });
    const customStatus = page.getByLabel("自定义 HTTP 状态码", { exact: true });
    assert.equal(await statusChoice.inputValue(), "400-599");
    await statusChoice.selectOption("500-599");
    assert.equal(await customStatus.isVisible(), false);
    await statusChoice.selectOption("429");
    await statusChoice.selectOption("custom");
    await customStatus.fill("500-400");
    await page.locator("#save-template").click();
    await page
      .locator("#template-editor-error")
      .filter({ hasText: /范围起始值不能大于结束值/ })
      .waitFor();
    await customStatus.fill("301,400-500,503");
    await page.getByText("测试路径匹配", { exact: true }).click();
    await page.locator("#test-template").click();
    await page
      .locator("#template-test-result")
      .filter({ hasText: /^命中/ })
      .waitFor();
    await page.setViewportSize({ width: 390, height: 844 });
    const size = await page
      .locator("#template-editor .drawer-body")
      .evaluate((e) => ({ width: e.clientWidth, scroll: e.scrollWidth }));
    assert(
      size.scroll <= size.width + 1,
      "template editor horizontal overflow",
    );
    await page.screenshot({
      path: path.join(output, "inspection-template-mobile.png"),
    });
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.screenshot({
      path: path.join(output, "inspection-template-editor.png"),
    });
    await page.locator("#save-template").click();
    await page.locator("#template-editor").waitFor({ state: "hidden" });
    assert.equal(
      await page.locator("#pending-changes").textContent(),
      "待发布 1 项",
    );
    await page.locator('[data-tab="channels"]').click();
    await page
      .locator("#channel-list")
      .getByRole("button", { name: "编辑", exact: true })
      .click();
    assert.equal(
      await page.locator("#channel-advanced").getAttribute("open"),
      null,
    );
    assert(
      await page
        .getByLabel("关联 模型目录错误检查", { exact: true })
        .isVisible(),
    );
    await page.getByLabel("关联 模型目录错误检查", { exact: true }).check();
    assert(
      await page
        .locator("#channel-editor")
        .getByLabel("OpenAI Responses", { exact: true })
        .isChecked(),
    );
    assert(
      await page
        .locator("#channel-editor")
        .getByLabel("OpenAI Responses", { exact: true })
        .isDisabled(),
    );
    await page.locator("#cancel-editor").click();
    await page
      .locator("#channel-list")
      .getByRole("button", { name: "编辑", exact: true })
      .click();
    assert.equal(
      await page
        .getByLabel("关联 模型目录错误检查", { exact: true })
        .isChecked(),
      false,
    );
    assert.equal(
      await page
        .locator("#channel-editor")
        .getByLabel("OpenAI Responses", { exact: true })
        .isChecked(),
      false,
    );
    await page.getByLabel("关联 模型目录错误检查", { exact: true }).check();
    assert.equal(
      await page.locator("#inspection-links .template-link").count(),
      3,
    );
    await page.locator("#inspection-order > summary").click();
    await page
      .locator("#inspection-links .template-link")
      .last()
      .getByRole("button", { name: "上移", exact: true })
      .click();
    await page.locator("#inspection-order > summary").click();
    await page.screenshot({ path: path.join(output, "channel-simple.png") });
    await page.locator("#save-channel").click();
    assert.equal(
      await page.locator("#pending-changes").textContent(),
      "待发布 2 项",
    );
    await page.locator("#pending-changes").click();
    await page.locator("#publish").click();
    await page.locator("#latest").filter({ hasText: /^V6$/ }).waitFor();
    async function waitForApplied(version) {
      for (const target of [base, second]) {
        const deadline = Date.now() + 15000;
        while (
          (await api(target + "/api/status")).local.applied_version !==
            version &&
          Date.now() < deadline
        )
          await new Promise((r) => setTimeout(r, 200));
        assert.equal(
          (await api(target + "/api/status")).local.applied_version,
          version,
        );
      }
    }
    await waitForApplied(6);
    const snapshot = (await api(base + "/api/versions/6")).config;
    assert.equal(
      snapshot.inspection_templates.find((t) => t.id === "catalog-check")
        .http_statuses,
      "301,400-500,503",
    );
    assert.deepEqual(snapshot.channels[0].protocols.openai_chat, {
      auth: "bearer",
    });
    assert(snapshot.channels[0].protocols.openai_responses);
    assert.deepEqual(snapshot.channels[0].inspection_templates, [
      "openai-chat",
      "catalog-check",
      "anthropic-messages",
    ]);
    assert.deepEqual((await api(second + "/api/versions/6")).config, snapshot);
    for (const target of [data, secondData]) {
      const response = await fetch(
        target + "/r/test-provider/v1/models?fail=1",
        { headers: modelHeaders },
      );
      assert.equal(response.status, 403);
      assert(!/PRIVATE_BALANCE/.test(await response.text()));
      for (const status of [301, 500, 501, 502, 503]) {
        const selected = [301, 500, 503].includes(status);
        const result = await fetch(
          target + "/r/test-provider/v1/models?fail=1&status=" + status,
          {
            headers: modelHeaders,
            redirect: "manual",
          },
        );
        const body = await result.text();
        assert.equal(
          /PRIVATE_BALANCE/.test(body),
          !selected,
          "status selection " + status,
        );
        assert.equal(
          result.headers.get("Location"),
          selected ? null : "/never-follow",
        );
        if (!selected) assert.equal(result.status, status);
      }
    }
    // A referenced template cannot be deleted. Editing a shared template changes
    // matching only after publication; method matching is exact.
    await page.locator('[data-tab="inspection-templates"]').click();
    const card = page
      .locator(".template-card")
      .filter({ hasText: "模型目录错误检查" });
    await card.getByRole("button", { name: "删除", exact: true }).click();
    assert.match(await page.locator("#notice").textContent(), /仍被渠道关联/);
    await card.getByRole("button", { name: "编辑", exact: true }).click();
    assert.equal(
      await page.locator("#template-advanced").getAttribute("open"),
      null,
    );
    await page.screenshot({ path: path.join(output, "template-simple.png") });
    await page.locator("#template-advanced > summary").click();
    await page.getByLabel("检查协议", { exact: true }).selectOption("gemini");
    assert.equal(await statusChoice.inputValue(), "custom");
    assert.equal(await customStatus.inputValue(), "301,400-500,503");
    await page
      .locator("#template-matches")
      .getByLabel("请求方法", { exact: true })
      .selectOption("HEAD");
    await page.locator("#save-template").click();
    await page.locator("#template-editor").waitFor({ state: "hidden" });
    await page.locator("#pending-changes").click();
    await page.locator("#publish").click();
    await page.locator("#latest").filter({ hasText: /^V7$/ }).waitFor();
    await waitForApplied(7);
    const updated = (await api(base + "/api/versions/7")).config;
    assert(
      updated.channels[0].protocols.gemini,
      "shared template edits automatically enable the required protocol",
    );
    assert.deepEqual(updated.channels[0].protocols.openai_chat, {
      auth: "bearer",
    });
    for (const target of [data, secondData]) {
      const response = await fetch(
        target + "/r/test-provider/v1/models?fail=1",
        { headers: modelHeaders },
      );
      assert.equal(response.status, 403);
      assert.match(await response.text(), /PRIVATE_BALANCE/);
    }
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      ),
      false,
    );
    await page.screenshot({ path: path.join(output, "mobile.png") });
    assert.deepEqual(errors, []);
    console.log(
      "Real browser/API checks passed: presets, sample match, full publication, two nodes, upstream interception, log query/detail/failure, edit/disable/delete, historical logs, inspection template validation, channel associations, ordering, publication, default passthrough, matching and shared edits, mobile layout.",
    );
  } finally {
    await browser.close();
    await new Promise((resolve) => mock.close(resolve));
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
