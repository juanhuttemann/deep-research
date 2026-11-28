// The serve page's live frame, driven through a run's events, checked for
// each feature it shares with the terminal UI. docs/surfaces.md lists the
// features; every test names the one it covers in brackets, and
// TestSurfacesParity (internal/ui) fails when a listed feature has no test
// here. The page is the file in the tree, and every request it makes is
// answered below: nothing reaches a network or a drsh.
import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";

const pageHTML = readFileSync(new URL("../index.html", import.meta.url), "utf8");
const run = readFileSync(new URL("./testdata/run.jsonl", import.meta.url), "utf8")
  .trim().split("\n").map(l => JSON.parse(l));
const question = run[0].question;
const plan = run.find(e => e.type === "plan").plan;

// before is the index of the first event matching f: the events served
// stop there, leaving the run live at that point.
const before = f => run.findIndex(f);

// serve opens the page with the run's events up to (not including) upTo.
// Their times are moved to just after the page loaded: a run that ended
// before the page opened is history, and the page shows the start screen.
// planSettings is what /api/plan answers for the run's plan: the review
// settings the terminal's brief row shows, as the server draws them.
const planSettings = { per_topic: 3, settings: [
  { key: "mode", label: "depth", value: "quick", choices: [{ value: "quick", label: "quick" }] },
  { key: "follow_ups", label: "follow-ups", value: "off", choices: [{ value: "off", label: "off" }] },
] };

async function serve(page, upTo = run.length, extra = []) {
  await page.route("**/*", async route => {
    const u = new URL(route.request().url());
    if (u.host !== "drsh.test") return route.abort();
    switch (u.pathname) {
    case "/": return route.fulfill({ contentType: "text/html", body: pageHTML });
    case "/api/info": return route.fulfill({ json: {} });
    case "/api/runs": return route.fulfill({ json: [] });
    case "/api/plan": return route.fulfill({ json: planSettings });
    case "/api/events": {
      const base = Date.now() + 1000, t0 = Date.parse(run[0].time);
      const body = "retry: 86400000\n\n" + run.slice(0, upTo).concat(extra).map((e, i) =>
        `id: ${i}\ndata: ${JSON.stringify({ ...e, time: new Date(base + Date.parse(e.time) - t0).toISOString() })}\n\n`).join("");
      return route.fulfill({ headers: { "Content-Type": "text/event-stream" }, body });
    }
    }
    return route.fulfill({ status: 404, body: "" });
  });
  await page.goto("http://drsh.test/");
  await expect(page.locator("#run")).toBeVisible();
}
async function openActivity(page) {
  await page.locator("#activity > summary").click();
  await expect(page.locator("#log")).toBeVisible();
}
const head = (page, n) => page.locator("#log .head").nth(n);

test("[topic-groups] each topic's rows sit under one header, opened by a click or its digit", async ({ page }) => {
  await serve(page);
  await openActivity(page);
  await expect(page.locator("#log .head")).toHaveCount(3);
  await expect(head(page, 0)).toContainText("Wind and solar share");
  await expect(head(page, 0).locator(".num")).toHaveText("#1");
  await expect(head(page, 0).locator(".mark")).toHaveText("▹");
  // Shut, a header shows its latest row; no topic row stands on its own.
  await expect(head(page, 0).locator(".latest")).toBeVisible();
  await expect(page.locator("#log .act.sub")).toHaveCount(0);
  await head(page, 0).click();
  await expect(head(page, 0).locator(".mark")).toHaveText("▿");
  await expect(page.locator("#log .act.sub")).toHaveCount(5);
  await page.keyboard.press("2");
  await expect(head(page, 1).locator(".mark")).toHaveText("▿");
  await page.keyboard.press("1");
  await expect(head(page, 0).locator(".mark")).toHaveText("▹");
  // The run's own rows (a phase starting) stay rows of their own.
  await expect(page.locator("#log > .act:not(.head):not(.sub)").first()).toContainText("Planning research scope");
});

test("[topic-colours] a topic keeps one colour in the tree and the tail, and topics differ", async ({ page }) => {
  await serve(page);
  await openActivity(page);
  const colour = l => l.evaluate(n => getComputedStyle(n).color);
  const tree = page.locator("#agents .agent .nm");
  expect(await colour(tree.nth(0))).toBe(await colour(head(page, 0).locator(".nm")));
  expect(await colour(tree.nth(0))).not.toBe(await colour(tree.nth(1)));
  expect(await colour(tree.nth(1))).not.toBe(await colour(tree.nth(2)));
});

test("[topic-labels] tree rows and headers carry the topic's number, follow-ups after the divider", async ({ page }) => {
  await serve(page);
  await expect(page.locator("#agents .agent .num")).toHaveText(["#1", "#2", "#3"]);
  await expect(page.locator("#agents > *").nth(2)).toHaveClass(/divider/);
  await expect(page.locator("#agents .agent").nth(2)).toContainText("Gas share 2024");
});

test("[read-rows] a page's read and its verify are one row: spinner, then a dot, linked when read", async ({ page }) => {
  // Live, before any verify: the reads spin.
  await serve(page, before(e => e.type === "verify"));
  await openActivity(page);
  await head(page, 0).click();
  await expect(page.locator("#log .act.sub .spin")).toHaveCount(3);
  await expect(page.locator("#log .act.sub", { has: page.locator(".spin") }).first()).toContainText("ember-energy.org/latest-insights/european-electricity-review-2025");
});

test("[read-rows] settled reads show their outcome in place; reads never verified are dropped", async ({ page }) => {
  await serve(page);
  await openActivity(page);
  await head(page, 0).click();
  const rows = page.locator("#log .act.sub");
  await expect(rows.locator(".dot.ok")).toHaveCount(1);
  await expect(rows.locator(".dot.warn")).toHaveCount(1);
  await expect(rows.locator(".dot.bad")).toHaveCount(1);
  await expect(rows.locator("a[href^='https://ember-energy.org/']")).toHaveCount(1);
  await expect(rows.locator("a[href='https://blocked.example.com/x']")).toHaveCount(0);
  // No second row for how a read went.
  await expect(page.locator("#log")).not.toContainText("ok ember-energy.org");
  // Topic 2's read was in flight when it finished: it will never be verified.
  await page.keyboard.press("1");
  await page.keyboard.press("2");
  await expect(page.locator("#log")).not.toContainText("example.net");
});

test("[read-rows] a page fetched past the budget says how its fetch went, as text, not as a source", async ({ page }) => {
  const at = before(e => e.type === "verify"), time = run[at - 1].time;
  const url = "https://spare.example.com/paper";
  await serve(page, at, [
    { type: "read", time, phase: "Research", url, domain: "spare.example.com", sub_id: "1", line: "read spare.example.com" },
    { type: "verify", time, phase: "Research", url, status: "unused", code: "403", sub_id: "1",
      line: "~ spare.example.com unverified (403) · not used: budget full" },
  ]);
  await openActivity(page);
  await head(page, 0).click();
  const row = page.locator("#log .act.sub", { hasText: "spare.example.com unverified (403) · not used: budget full" });
  await expect(row).toHaveCount(1);
  await expect(row.locator(".dot, .spin, a")).toHaveCount(0);
});

test("[row-styles] rows are told apart by kind: phase, error, search, the run's notes", async ({ page }) => {
  await serve(page);
  await openActivity(page);
  await expect(page.locator("#log .x .cy", { hasText: "▸" }).first()).toBeVisible();
  await expect(page.locator("#log b", { hasText: "Planning research scope" })).toBeVisible();
  await head(page, 0).click();
  await head(page, 1).click();
  await expect(page.locator("#log .x .rd", { hasText: "!" })).toHaveCount(1);
  await expect(page.locator("#log .x .dim", { hasText: "search" }).first()).toBeVisible();
  await expect(page.locator("#log .x .dim", { hasText: "searxng: 8 results" })).toBeVisible();
  // A citation has no row of its own.
  await expect(page.locator("#log")).not.toContainText("European Electricity Review 2025");
});

test("[tree-lines] a tree row follows its topic's searches and reads, and its errors", async ({ page }) => {
  await serve(page, before(e => e.type === "read"));
  await expect(page.locator("#agents .agent").nth(0).locator(".ln")).toHaveText("Searching: Ember 2024 EU wind solar share");
});

test("[tree-lines] an error on a topic puts its detail on the topic's row", async ({ page }) => {
  await serve(page, before(e => e.type === "token"));
  await expect(page.locator("#agents .agent").nth(1).locator(".ln")).toHaveText("search failed: rate-limited");
});

test("[phase-nav] reached phases on the strip open their view; Esc and the open phase go back", async ({ page }) => {
  await serve(page, before(e => e.type === "phase" && e.phase === "Analyze"));
  await expect(page.locator("#stepper button.step")).toHaveText(["✓ Plan", "▸ Research"]);
  await expect(page.locator("#stepper span.step", { hasText: "Analyze" })).toBeVisible();
  await page.locator("#stepper button.step", { hasText: "Plan" }).click();
  await expect(page.locator("#phase-view")).toBeVisible();
  await expect(page.locator("#agents")).toBeHidden();
  await expect(page.locator("#stepper button.step.open")).toHaveText("✓ Plan");
  await page.keyboard.press("Escape");
  await expect(page.locator("#phase-view")).toBeHidden();
  await expect(page.locator("#agents")).toBeVisible();
  await page.locator("#stepper button.step", { hasText: "Research" }).click();
  await page.locator("#stepper button.step", { hasText: "Research" }).click();
  await expect(page.locator("#phase-view")).toBeHidden();
});

test("[phase-views] the plan view lists each sub-topic, its search and terms, and when the phase ran", async ({ page }) => {
  await serve(page);
  await page.locator("#stepper button.step", { hasText: "Plan" }).click();
  await expect(page.locator("#pv-title")).toContainText("Plan");
  await expect(page.locator("#pv-title")).toContainText("started 0:01 · took 0:04");
  const body = page.locator("#pv-body");
  await expect(body).toContainText("2 sub-topics · multi-part question");
  for (const [i, t] of plan.sub_topics.entries()) {
    await expect(body.locator(".hn", { hasText: "#" + (i + 1) })).toBeVisible();
    await expect(body).toContainText(t.name);
    await expect(body).toContainText(t.query);
    await expect(body).toContainText(t.terms.join(" · "));
  }
  await expect(body).toContainText("reasoning");
});

test("[phase-views] the research view lists every topic with its sources and rows; later phases their reasoning", async ({ page }) => {
  await serve(page);
  await page.locator("#stepper button.step", { hasText: "Research" }).click();
  const body = page.locator("#pv-body");
  await expect(body).toContainText("Wind and solar share");
  await expect(body).toContainText("2 sources");
  await expect(body).toContainText("Gas share 2024");
  await page.locator("#stepper button.step", { hasText: "Fact-Check" }).click();
  await expect(page.locator("#pv-title")).toContainText("took 0:10");
  await expect(body).toContainText("claims 1-2");
  await expect(body).toContainText("Claim three needs the gas page.");
});

test("[activity-toggle] the tail starts hidden and a toggles it", async ({ page }) => {
  await serve(page);
  await expect(page.locator("#log")).toBeHidden();
  await page.keyboard.press("a");
  await expect(page.locator("#log")).toBeVisible();
  await page.keyboard.press("a");
  await expect(page.locator("#log")).toBeHidden();
});

test("[source-split] the total names the follow-up round's share", async ({ page }) => {
  await serve(page);
  await expect(page.locator("#n-sources")).toHaveText("3");
  await expect(page.locator("#n-split")).toHaveText(" (2 planned + 1 follow-up)");
});

test("[status-row] the status row stays out of the research", async ({ page }) => {
  await serve(page, before(e => e.type === "token"));
  await expect(page.locator("#status")).toBeHidden();
});

test("[counts] counts and times read as the terminal's", async ({ page }) => {
  await serve(page);
  await expect(page.locator("#n-tokens")).toHaveText("1.2M");
  expect(await page.evaluate(() => [clock(3723000), clock(65000), fmtNum(12345), fmtNum(999)])).toEqual(["1:02:03", "1:05", "12.3k", "999"]);
});

test("[report-title] the run is headed by its title, the question under it", async ({ page }) => {
  await serve(page);
  await expect(page.locator("#question")).toHaveText(plan.title);
  await expect(page.locator("#question-sub")).toHaveText(question);
});

test("[report-title] a run with no title is headed by its question alone", async ({ page }) => {
  await serve(page, before(e => e.type === "plan"));
  await expect(page.locator("#question")).toHaveText(question);
  await expect(page.locator("#question-sub")).toBeHidden();
});

test("[phase-views] the plan view gives the plan's settings as the server reads them", async ({ page }) => {
  await serve(page);
  await page.locator("#stepper button.step", { hasText: "Plan" }).click();
  await expect(page.locator("#pv-body")).toContainText("follow-ups off");
  await expect(page.locator("#pv-body")).toContainText("per sub-agent 3");
});

test("[phase-views] a cancelled run's last phase is not still running", async ({ page }) => {
  const at = before(e => e.type === "phase" && e.phase === "Analyze");
  await serve(page, at + 1, [{ type: "done", time: "2026-01-01T00:00:23Z", status: "cancelled" }]);
  await page.locator("#stepper button.step", { hasText: "Analyze" }).click();
  await expect(page.locator("#pv-title")).toContainText("took 0:03");
});

test("[topic-labels] on a narrow screen the number keeps the name readable", async ({ page }) => {
  await page.setViewportSize({ width: 380, height: 800 });
  await serve(page);
  const box = await page.locator("#agents .agent .nm").first().boundingBox();
  expect(box.width).toBeGreaterThan(120);
});
