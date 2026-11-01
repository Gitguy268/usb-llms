// Browser test against a running `usb-llms web --no-open`.
//   PW=/path/to/node_modules/playwright URL=http://127.0.0.1:11436/ MODEL=smollm2 OUT=shots node test/e2e.mjs
import { createRequire } from "node:module";
import { mkdirSync } from "node:fs";
import assert from "node:assert/strict";

const { chromium } = createRequire(import.meta.url)(process.env.PW);
const url = process.env.URL, model = process.env.MODEL, out = process.env.OUT ?? "shots";
mkdirSync(out, { recursive: true });
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1180, height: 760 }, colorScheme: "light" });
const page = await ctx.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
page.on("console", (m) => m.type() === "error" && errors.push(m.text()));

await page.goto(url);
await page.waitForFunction((m) => [...document.querySelectorAll("#model option")].some((o) => o.value.startsWith(m)), model);
const value = await page.$$eval("#model option", (os, m) => os.find((o) => o.value.startsWith(m)).value, model);
await page.selectOption("#model", value);
assert.equal(await page.textContent("#mode-text"), "Offline");
assert.equal(await page.getAttribute("#mode", "aria-checked"), "false");
await page.screenshot({ path: `${out}/empty.png` });

await page.fill("#input", "Say hi in three words.");
await page.click("#send");
await page.waitForFunction(() => document.querySelectorAll(".chat-row").length > 0, null, { timeout: 120000 });
await page.waitForFunction(() => !document.querySelector("#send").disabled && document.querySelector("#send").textContent === "Send", null, { timeout: 120000 });
const reply = await page.$$eval("#messages > *", (ns) => ns.at(-1).textContent);
assert.ok(reply.trim().length > 0, "the model replied");
console.log("reply:", reply.trim().slice(0, 100));
await page.screenshot({ path: `${out}/chat.png` });

await page.reload();
await page.waitForSelector(".chat-row .open");
await page.click(".chat-row .open");
await page.waitForFunction(() => document.querySelector("#messages").textContent.includes("Say hi in three words."));

await page.click("#show-models");
await page.waitForSelector("#installed tr");
assert.ok((await page.textContent("#installed")).includes(model));
assert.ok((await page.textContent("#catalog")).includes("uncensored"));
await page.screenshot({ path: `${out}/models.png`, fullPage: true });

await page.fill("#spec", "smollm2-135m");
await page.click("#add button");
await page.waitForFunction(() => /offline/i.test(document.querySelector("#progress-text").textContent));

await page.click("#mode");
await page.waitForFunction(() => document.querySelector("#mode-text").textContent === "Online", null, { timeout: 60000 });
await page.waitForSelector("#mode[aria-checked=true]");
await page.click("#mode");
await page.waitForFunction(() => document.querySelector("#mode-text").textContent === "Offline", null, { timeout: 60000 });

const phone = await browser.newContext({ viewport: { width: 390, height: 780 }, colorScheme: "dark", hasTouch: true });
const p2 = await phone.newPage();
await p2.goto(url);
await p2.waitForSelector("#model option", { state: "attached" });
const overflow = await p2.evaluate(() => document.documentElement.scrollWidth - innerWidth);
assert.ok(overflow <= 0, `no sideways scroll on a phone (${overflow}px)`);
await p2.screenshot({ path: `${out}/phone-dark.png` });
await p2.click("#menu");
await p2.waitForSelector(".chat-row .open", { state: "visible" });
await p2.waitForTimeout(250);
await p2.screenshot({ path: `${out}/phone-menu.png` });
while (await p2.locator(".chat-row .del").count()) { await p2.locator(".chat-row .del").first().click({ force: true }); await p2.waitForTimeout(300); }
await p2.waitForFunction(() => !document.querySelector(".chat-row"));

await browser.close();
assert.deepEqual(errors.filter((e) => !/Failed to load resource/.test(e)), []);
console.log("browser test passed");
