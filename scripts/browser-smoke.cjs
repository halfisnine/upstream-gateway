// Optional browser acceptance check. Use ONLY an isolated development database.
// GATEWAY_BROWSER_URL, GATEWAY_BROWSER_TOKEN_FILE and PLAYWRIGHT_MODULE are required.
const fs = require("node:fs");
const path = require("node:path");
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright");
const base = process.env.GATEWAY_BROWSER_URL;
const token = fs
  .readFileSync(process.env.GATEWAY_BROWSER_TOKEN_FILE, "utf8")
  .trim();
const output =
  process.env.GATEWAY_BROWSER_OUTPUT || path.resolve("build/browser");
if (!base || !new URL(base).hostname.match(/^(127\.0\.0\.1|localhost)$/))
  throw Error("Only loopback development consoles are accepted");
fs.mkdirSync(output, { recursive: true });
(async () => {
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
  const failures = [];
  page.on("pageerror", (e) => failures.push(e.message));
  page.on("dialog", (d) => d.accept());
  const api = async (p, body) => {
    const r = await fetch(base + p, {
      method: body ? "POST" : "GET",
      headers: {
        Authorization: "Bearer " + token,
        ...(body ? { "Content-Type": "application/json" } : {}),
      },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!r.ok) throw Error("API status " + r.status);
    return r.json();
  };
  let initial;
  try {
    initial = (await api("/api/status")).latest_version;
    if (
      initial !== 0 &&
      (await api("/api/versions/" + initial)).config.channels.length
    )
      throw Error(
        "Browser acceptance requires an empty development configuration",
      );
    const v1 = initial + 1,
      v2 = initial + 2,
      v3 = initial + 3,
      v4 = initial + 4;
    const waitVersion = async (v) =>
      page
        .locator("#baseline")
        .filter({ hasText: new RegExp("^V" + v + "$") })
        .waitFor({ state: "visible" });
    await page.goto(base);
    await page.locator("#admin-token").fill(token);
    await page.getByRole("button", { name: "进入控制台" }).click();
    await page.locator("#workspace").waitFor({ state: "visible" });
    await page.getByRole("button", { name: "＋ 新增渠道" }).click();
    await page.getByLabel("渠道标识", { exact: true }).fill("demo-a");
    await page
      .getByLabel("渠道名称", { exact: true })
      .fill("示例渠道 A · 多协议");
    await page
      .getByLabel("默认上游地址", { exact: true })
      .fill("https://example.invalid");
    if (await page.locator("#channel-editor input[type=password]").count())
      throw Error("Channel credentials are still editable");
    for (const p of ["OpenAI Responses", "Anthropic Messages", "Gemini"])
      await page.getByLabel(p, { exact: true }).check();
    await page.getByRole("button", { name: "保存到草稿", exact: true }).click();
    await page.locator("#pending-changes").click();
    await page
      .locator("#publish-note")
      .fill("浏览器验收：单渠道四协议，未连接真实上游");
    if (!(await page.locator("#publish-dialog").isVisible()))
      await page.locator("#pending-changes").click();
    await page
      .getByRole("button", { name: "发布全部修改", exact: true })
      .click();
    await waitVersion(v1);
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({
      path: path.join(output, "channels.png"),
      fullPage: true,
    });
    await page.locator('[data-tab="nodes"]').click();
    await page
      .locator("#node-list")
      .getByText("已同步", { exact: true })
      .nth(1)
      .waitFor({ state: "visible", timeout: 20000 });
    await page.screenshot({
      path: path.join(output, "nodes.png"),
      fullPage: true,
    });
    await page.getByRole("button", { name: "查看已生效内容" }).first().click();
    await page.locator("#details").waitFor({ state: "visible" });
    const details = await page.locator("#detail-content").innerText();
    if (/"(?:key|key_set|internal_token|token_set)"\s*:/.test(details))
      throw Error("Credential field appeared in history");
    await page.locator("#close-details").click();
    await page.locator('[data-tab="channels"]').click();
    await page
      .locator('#channel-list tr[data-channel-id="demo-a"]')
      .getByRole("button", { name: "编辑", exact: true })
      .click();
    await page
      .getByLabel("渠道名称", { exact: true })
      .fill("示例渠道 A · 第二版");
    await page.getByRole("button", { name: "保存到草稿", exact: true }).click();
    if (!(await page.locator("#publish-dialog").isVisible()))
      await page.locator("#pending-changes").click();
    await page
      .getByRole("button", { name: "发布全部修改", exact: true })
      .click();
    await waitVersion(v2);
    await page.locator('[data-tab="history"]').click();
    await page
      .locator("#history-list tr")
      .filter({ has: page.getByText("V" + v1, { exact: true }) })
      .getByRole("button", { name: "恢复此版本" })
      .click();
    await waitVersion(v3);
    const restored = await api("/api/versions/" + v3);
    if (restored.config.channels[0].name !== "示例渠道 A · 多协议")
      throw Error("Rollback did not restore full snapshot");
    await page.screenshot({
      path: path.join(output, "history.png"),
      fullPage: true,
    });
    await page.locator('[data-tab="channels"]').click();
    await page
      .locator('#channel-list tr[data-channel-id="demo-a"]')
      .getByRole("button", { name: "删除", exact: true })
      .click();
    if (!(await page.locator("#publish-dialog").isVisible()))
      await page.locator("#pending-changes").click();
    await page
      .getByRole("button", { name: "发布全部修改", exact: true })
      .click();
    await waitVersion(v4);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: path.join(output, "mobile.png"),
      fullPage: true,
    });
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    );
    if (overflow) throw Error("Mobile layout overflows");
    await page.locator("#logout").click();
    await page.locator("#login").waitFor({ state: "visible" });
    if (failures.length)
      throw Error("Browser exceptions: " + failures.join(";"));
    fs.writeFileSync(
      path.join(output, "result.json"),
      JSON.stringify(
        {
          passed: true,
          checks: [
            "login",
            "channel-create",
            "four-protocols",
            "publication",
            "two-node-sync",
            "no-credential-fields",
            "edit",
            "full-snapshot-rollback",
            "delete",
            "mobile",
            "logout",
          ],
        },
        null,
        2,
      ),
    );
    console.log(
      "Browser acceptance passed: login, CRUD, four protocols, full versions, rollback, two-node sync, credential-free config, mobile and logout.",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(
    String(e.stack || e)
      .split(token)
      .join("[REDACTED]"),
  );
  process.exit(1);
});
