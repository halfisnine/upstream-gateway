// UI regression against an in-memory fixture. Never connects to a real gateway/database.
const fs = require("node:fs");
const path = require("node:path");
const http = require("node:http");
const assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright");
const root = path.resolve(__dirname, "..");
const output = path.resolve(
  process.env.GATEWAY_BROWSER_OUTPUT || path.join(root, "build/browser-layout"),
);
const channels = Array.from({ length: 36 }, (_, i) => ({
  id: `test-${String(i + 1).padStart(3, "0")}`,
  name: `示例渠道 ${String(i + 1).padStart(2, "0")}`,
  base_url: `https://provider-${i + 1}.example.invalid`,
  protocols: {
    openai_chat: {},
    openai_responses: {},
    anthropic_messages: {},
    gemini: {},
  },
}));
const snapshots = new Map([
  [1, { schema_version: 5, channels: structuredClone(channels) }],
]);
let latest = 1,
  posts = [],
  rollbacks = [],
  failStatus = false;
const server = http.createServer(async (req, res) => {
  res.setHeader("Cache-Control", "no-store");
  res.setHeader(
    "Content-Security-Policy",
    "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'",
  );
  const url = new URL(req.url, "http://localhost");
  if (url.pathname.startsWith("/api/")) {
    res.setHeader("Content-Type", "application/json");
    if (req.headers.authorization !== "Bearer fixture-admin") {
      res.writeHead(401);
      res.end(JSON.stringify({ error: "unauthorized" }));
      return;
    }
    if (url.pathname === "/api/status" && failStatus) {
      res.writeHead(503);
      res.end(JSON.stringify({ error: "database_unavailable" }));
      return;
    }
    let result;
    if (url.pathname === "/api/status")
      result = {
        latest_version: latest,
        nodes: ["test-a", "test-b"].map((id) => ({
          id,
          desired_version: latest,
          applied_version: latest,
          digest: "fixture",
          error: "",
          last_seen: new Date().toISOString(),
          inflight: 0,
          online: true,
        })),
      };
    else if (url.pathname.startsWith("/api/versions/")) {
      const id = Number(url.pathname.split("/").at(-1));
      result = {
        version: {
          id,
          digest: "fixture-digest-" + id,
          created_at: "2026-11-17T01:30:00Z",
          note: "浏览器模拟版本",
        },
        config: snapshots.get(id),
      };
    } else if (url.pathname === "/api/versions")
      result = [...snapshots.keys()].reverse().map((id) => ({
        id,
        note: "浏览器模拟版本",
        created_at: new Date().toISOString(),
        digest: "fixture",
      }));
    else if (url.pathname === "/api/publish" && req.method === "POST") {
      let raw = "";
      for await (const b of req) raw += b;
      const body = JSON.parse(raw);
      if (body.base_version !== latest) {
        res.writeHead(409);
        res.end(JSON.stringify({ error: "version_conflict" }));
        return;
      }
      posts.push(body);
      latest++;
      snapshots.set(latest, body.config);
      result = { id: latest };
      res.statusCode = 201;
    } else if (url.pathname === "/api/rollback" && req.method === "POST") {
      let raw = "";
      for await (const b of req) raw += b;
      const body = JSON.parse(raw);
      assert.equal(body.base_version, latest);
      rollbacks.push(body);
      latest++;
      snapshots.set(
        latest,
        structuredClone(snapshots.get(body.source_version)),
      );
      result = { id: latest };
    } else {
      res.writeHead(404);
      result = { error: "not_found" };
    }
    res.end(JSON.stringify(result));
    return;
  }
  const assets = {
    "/": "index.html",
    "/app.js": "app.js",
    "/style.css": "style.css",
    "/rules.js": "rules.js",
    "/rules.css": "rules.css",
  };
  const file = assets[url.pathname];
  if (!file) {
    res.writeHead(404);
    res.end();
    return;
  }
  res.setHeader(
    "Content-Type",
    file.endsWith(".js")
      ? "text/javascript"
      : file.endsWith(".css")
        ? "text/css"
        : "text/html",
  );
  res.end(fs.readFileSync(path.join(root, "internal/console/static", file)));
});
(async () => {
  fs.mkdirSync(output, { recursive: true });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const browser = await chromium.launch({
    headless: true,
    args: ["--no-sandbox"],
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1050 },
    locale: "zh-CN",
    timezoneId: "Asia/Shanghai",
  });
  const page = await context.newPage();
  let accept = true;
  const failures = [];
  const externalRequests = [];
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  page.on("request", (request) => {
    if (
      !request.url().startsWith(`http://127.0.0.1:${server.address().port}`) &&
      !request.url().startsWith("blob:")
    )
      externalRequests.push(request.url());
  });
  page.on("dialog", (d) => (accept ? d.accept() : d.dismiss()));
  page.on("pageerror", (e) => failures.push(e.message));
  const rows = page.locator("#channel-list tr");
  const edit = () =>
    rows.first().getByRole("button", { name: "编辑", exact: true }).click();
  const save = () =>
    page.getByRole("button", { name: "保存到草稿", exact: true }).click();
  try {
    await page.goto(`http://127.0.0.1:${server.address().port}`);
    await page.screenshot({
      path: path.join(output, "login.png"),
      fullPage: true,
    });
    await page.locator("#admin-token").fill("invalid-fixture-token");
    await page.getByRole("button", { name: "显示令牌", exact: true }).click();
    assert.equal(
      await page.locator("#admin-token").getAttribute("type"),
      "text",
    );
    await page.getByRole("button", { name: "隐藏令牌", exact: true }).click();
    await page.getByRole("button", { name: "进入控制台" }).click();
    await page.locator("#login-error").waitFor();
    assert.equal(await page.locator("#workspace").isVisible(), false);
    await page.locator("#admin-token").fill("fixture-admin");
    await page.getByRole("button", { name: "进入控制台" }).click();
    await rows.first().waitFor();
    assert.equal(await rows.count(), 10);
    assert.equal(await page.locator("#channel-list input").count(), 0);
    assert.match(await page.locator("#channel-page").innerText(), /1 \/ 4/);
    const height = await rows
      .first()
      .evaluate((e) => e.getBoundingClientRect().height);
    assert.ok(height < 65, `Row too tall: ${height}`);
    assert.equal(await page.locator("#admin-token").inputValue(), "");
    await page.locator(".copy-address").first().click();
    assert.equal(
      await page.evaluate(() => navigator.clipboard.readText()),
      channels[0].base_url,
    );
    await page.locator("#state-filter").selectOption("修改待发布");
    assert.equal(await rows.count(), 0);
    await page.locator("#state-filter").selectOption("");
    await page.locator("#protocol-filter").selectOption("gemini");
    assert.equal(await rows.count(), 10);
    await page.locator("#protocol-filter").selectOption("");
    await page.locator("#notice").evaluate((e) => (e.hidden = true));
    await page.screenshot({
      path: path.join(output, "channels.png"),
      fullPage: true,
    });
    await page.getByRole("button", { name: "下一页", exact: true }).click();
    assert.equal(
      await rows.first().getAttribute("data-channel-id"),
      "test-011",
    );
    await page.locator("#channel-search").fill("test-035");
    assert.equal(await rows.count(), 1);
    await edit();
    assert.ok(await page.getByLabel("渠道标识", { exact: true }).isDisabled());
    await page.getByLabel("渠道名称", { exact: true }).fill("应该取消的修改");
    await page.getByRole("button", { name: "取消", exact: true }).click();
    assert.match(await rows.first().innerText(), /示例渠道 35/);
    assert.equal(posts.length, 0);
    await edit();
    await page.getByLabel("渠道名称", { exact: true }).fill("修改后的示例渠道");
    const gemini = page.locator("#editor-protocol-details details").filter({
      has: page.getByText("Gemini · 可选覆盖设置", { exact: true }),
    });
    await gemini.locator("summary").click();
    await gemini
      .getByLabel("该协议上游地址", { exact: true })
      .fill("https://override.example.invalid");
    await page.screenshot({
      path: path.join(output, "editor.png"),
      fullPage: true,
    });
    await save();
    assert.match(await rows.first().innerText(), /修改待发布/);
    await page.locator("#channel-search").fill("");
    await page.locator("#state-filter").selectOption("修改待发布");
    assert.equal(await rows.count(), 1);
    assert.equal(
      await rows.first().getAttribute("data-channel-id"),
      "test-035",
    );
    await page.locator("#state-filter").selectOption("");
    await page.locator("#channel-search").fill("test-035");
    assert.equal(posts.length, 0);
    // A background status refresh must not unlock a published route identifier.
    await page.getByRole("button", { name: "刷新状态", exact: true }).click();
    await edit();
    await page.waitForTimeout(5200);
    assert.ok(await page.getByLabel("渠道标识", { exact: true }).isDisabled());
    await page.getByRole("button", { name: "取消", exact: true }).click();
    await page
      .getByRole("button", { name: "发布完整配置", exact: true })
      .click();
    await page.locator("#baseline").filter({ hasText: /^V2$/ }).waitFor();
    assert.equal(posts.length, 1);
    assert.equal(posts[0].config.channels.length, 36);
    assert.equal(
      posts[0].config.channels[34].protocols.gemini.base_url,
      "https://override.example.invalid",
    );
    assert.deepEqual(posts[0].config.channels[0], channels[0]);
    await page.locator("#channel-search").fill("does-not-exist");
    await page.locator("#empty-channels").waitFor({ state: "visible" });
    assert.equal(await rows.count(), 0);
    await page.locator("#channel-search").fill("");
    await page.locator("#channel-page-size").selectOption("20");
    assert.equal(await rows.count(), 20);
    await page.getByRole("button", { name: "＋ 新增渠道" }).click();
    await page.getByLabel("渠道标识", { exact: true }).fill("test-001");
    await page.getByLabel("渠道名称", { exact: true }).fill("新增示例渠道");
    await page
      .getByLabel("默认上游地址", { exact: true })
      .fill("https://new.example.invalid");
    await save();
    assert.match(await page.locator("#editor-error").innerText(), /已存在/);
    await page.getByLabel("渠道标识", { exact: true }).fill("test-new");
    await page.getByLabel("OpenAI Chat", { exact: true }).uncheck();
    await save();
    assert.match(await page.locator("#editor-error").innerText(), /至少启用/);
    await page.getByLabel("OpenAI Chat", { exact: true }).check();
    await save();
    await page.locator("#protocol-filter").selectOption("gemini");
    assert.equal(
      await page
        .locator('#channel-list tr[data-channel-id="test-new"]')
        .count(),
      0,
    );
    await page.locator("#protocol-filter").selectOption("");
    await page.locator("#state-filter").selectOption("新增待发布");
    const added = page.locator('#channel-list tr[data-channel-id="test-new"]');
    await added.waitFor();
    assert.match(await added.innerText(), /新增待发布/);
    accept = false;
    await added.getByRole("button", { name: "删除", exact: true }).click();
    assert.equal(await added.count(), 1);
    accept = true;
    await added.getByRole("button", { name: "删除", exact: true }).click();
    assert.equal(await added.count(), 0);
    await page.locator("#state-filter").selectOption("");
    await page.locator("#channel-search").fill("test-001");
    await rows
      .first()
      .getByRole("button", { name: "删除", exact: true })
      .click();
    assert.match(await page.locator("#draft-state").innerText(), /删除 1/);
    assert.equal(posts.length, 1);
    await page.locator("#channel-search").fill("");
    await page.locator("#channel-page-size").selectOption("10");
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      ),
      false,
    );
    await page.screenshot({
      path: path.join(output, "mobile.png"),
      fullPage: true,
    });
    await edit();
    assert.equal(
      await page
        .locator("#channel-editor")
        .evaluate((e) => e.getBoundingClientRect().width),
      390,
    );
    assert.equal(
      await page
        .locator("#channel-editor .drawer-body")
        .evaluate((e) => e.scrollWidth > e.clientWidth),
      false,
    );
    await page.screenshot({
      path: path.join(output, "mobile-editor.png"),
      fullPage: false,
    });
    await page
      .getByLabel("渠道名称", { exact: true })
      .fill("Esc should cancel");
    await page.keyboard.press("Escape");
    await page.locator("#channel-editor").waitFor({ state: "hidden" });
    // The fixed publish controls must leave the last row and pagination reachable.
    await page.locator("#channel-pagination").scrollIntoViewIfNeeded();
    assert.ok(
      await page.evaluate(
        () =>
          document.getElementById("channel-pagination").getBoundingClientRect()
            .bottom <=
          document.getElementById("publish-dock").getBoundingClientRect().top,
      ),
    );
    await page.setViewportSize({ width: 1440, height: 1050 });
    await page.locator('[data-tab="nodes"]').click();
    await page.locator("#node-list tr").first().waitFor();
    assert.equal(await page.locator("#node-list tr").count(), 2);
    await page.screenshot({
      path: path.join(output, "nodes.png"),
      fullPage: true,
    });
    await page.getByRole("button", { name: "查看已生效内容" }).first().click();
    await page.locator("#details").waitFor();
    const tree = page.locator("#detail-content");
    const branches = tree.locator("details");
    const channelBranches = tree.locator("details[data-channel-id]");
    assert.equal(await channelBranches.count(), 36);
    assert.equal(await channelBranches.first().getAttribute("open"), null);
    assert.match(
      await channelBranches.first().locator(":scope > summary").textContent(),
      /示例渠道 01.*test-001/,
    );
    assert.equal(
      await channelBranches
        .first()
        .locator(".json-children")
        .first()
        .isVisible(),
      false,
    );
    await page.screenshot({
      path: path.join(output, "json-folded.png"),
      fullPage: false,
    });
    const summary = channelBranches.first().locator(":scope > summary");
    await summary.focus();
    await page.keyboard.press("Enter");
    assert.equal(
      await channelBranches
        .first()
        .locator(".json-children")
        .first()
        .isVisible(),
      true,
    );
    const protocols = channelBranches.first().locator("details").first();
    assert.equal(await protocols.getAttribute("open"), null);
    await protocols.locator(":scope > summary").click();
    assert.match(await protocols.innerText(), /openai_chat/);
    await page.locator("#expand-json").click();
    assert.equal(
      await branches.count(),
      await tree.locator("details[open]").count(),
    );
    await page.locator("#collapse-json").click();
    assert.equal(await tree.locator("details[open]").count(), 0);
    // Folding affects presentation only: the existing copy/export checks below
    // must still receive every channel and protocol in the original snapshot.
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator("#expand-json").click();
    assert.equal(
      await tree.evaluate((e) => e.scrollWidth > e.clientWidth),
      false,
    );
    await page.locator("#collapse-json").click();
    await page.setViewportSize({ width: 1440, height: 1050 });
    assert.equal(await page.locator("#restore-details").isVisible(), false);
    await page.locator("#copy-details").click();
    assert.deepEqual(
      JSON.parse(await page.evaluate(() => navigator.clipboard.readText())),
      snapshots.get(2),
    );
    const downloadPromise = page.waitForEvent("download");
    await page.locator("#export-details").click();
    const download = await downloadPromise;
    assert.equal(download.suggestedFilename(), "gateway-config-v2.json");
    assert.deepEqual(
      JSON.parse(fs.readFileSync(await download.path(), "utf8")),
      snapshots.get(2),
    );
    await page.locator("#close-details").click();
    failStatus = true;
    await page.locator("#refresh").click();
    await page.locator("#connection.stale").waitFor();
    assert.match(await page.locator("#node-summary").textContent(), /刷新失败/);
    failStatus = false;
    await page.locator("#refresh").click();
    await page.locator("#connection:not(.stale)").waitFor();
    await page.locator('[data-tab="history"]').click();
    await page.locator("#history-list tr").first().waitFor();
    assert.equal(await page.locator("#history-list tr").count(), 2);
    assert.match(
      await page.locator("#history-list tr").first().textContent(),
      /最新发布/,
    );
    await page.locator("#notice").evaluate((e) => (e.hidden = true));
    await page.screenshot({
      path: path.join(output, "history.png"),
      fullPage: true,
    });
    await page
      .locator('#history-list tr[data-version="1"]')
      .getByRole("button", { name: "查看内容" })
      .click();
    await page.locator("#details").waitFor();
    assert.equal(
      await page
        .locator("#detail-content details[data-channel-id][open]")
        .count(),
      0,
    );
    assert.equal(
      await page.locator("#detail-content > details[open]").count(),
      1,
    );
    await page.locator("#compare-details").click();
    assert.match(await page.locator("#diff-content").textContent(), /test-001/);
    assert.match(await page.locator("#diff-content").textContent(), /test-035/);
    await page.screenshot({
      path: path.join(output, "version-detail.png"),
      fullPage: true,
    });
    accept = false;
    await page.locator("#restore-details").click();
    assert.equal(rollbacks.length, 0);
    accept = true;
    await page.locator("#restore-details").click();
    await page.locator("#baseline").filter({ hasText: /^V3$/ }).waitFor();
    assert.equal(rollbacks.length, 1);
    assert.equal(rollbacks[0].source_version, 1);
    assert.deepEqual(snapshots.get(3), snapshots.get(1));
    await page.locator("#logout").click();
    await page.locator("#login").waitFor();
    assert.equal(await page.locator("#publish-dock").isVisible(), false);
    await page.getByRole("button", { name: "进入控制台" }).waitFor();
    assert.deepEqual(externalRequests, []);
    assert.deepEqual(failures, []);
    fs.writeFileSync(
      path.join(output, "result.json"),
      JSON.stringify(
        {
          passed: true,
          fixture_channels: 36,
          desktop_row_height: height,
          checks: [
            "compact rows",
            "pagination",
            "search",
            "cancel isolation",
            "draft-only save",
            "full publication",
            "protocol override",
            "identifier lock survives refresh",
            "duplicate validation",
            "protocol validation",
            "delete confirmation",
            "mobile drawer",
            "escape cancellation",
            "login errors and token visibility",
            "protocol and draft state filters",
            "copy and export complete snapshot while folded",
            "default channel summaries and keyboard folding",
            "nested JSON and expand/collapse all",
            "mobile JSON wrapping and fold reset",
            "node and history tables",
            "refresh failure status",
            "history diff and restore",
            "mobile publish dock clearance",
            "logout and no external requests",
          ],
          real_config_writes: 0,
        },
        null,
        2,
      ),
    );
    console.log(
      "UI layout acceptance passed; real configuration/database writes: 0. Desktop row height: " +
        height +
        "px.",
    );
  } finally {
    await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((e) => {
  console.error(e);
  server.close();
  process.exitCode = 1;
});
