// Browser test of the Create page and chat attachments, against a running `usb-llms web --no-open`
// on a drive with speech set up.
//   PW=/path/to/node_modules/playwright URL=http://127.0.0.1:11436/ IMAGE=pic.png OUT=shots node test/media-e2e.mjs
import { createRequire } from "node:module";
import { mkdirSync } from "node:fs";

const { chromium } = createRequire(import.meta.url)(process.env.PW);
const url = process.env.URL, out = process.env.OUT ?? "shots";
mkdirSync(out, { recursive: true });
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1180, height: 860 }, colorScheme: "light" });
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
let failed = false;
const check = (ok, what) => {
  console.log(ok ? "ok  " : "FAIL", what);
  if (!ok) failed = true;
};

await page.goto(url);
await page.click("#show-create");
await page.waitForSelector("#kinds button");
check((await page.locator("#kinds button").count()) === 6, "six kinds on the Create page");
await page.waitForFunction(() => document.querySelectorAll("#m-catalog tr").length > 5);
const items = await page.locator("#gallery .item").count();
check(items > 0, `gallery shows ${items} results`);
if (await page.locator("model-viewer").count()) {
  await page.waitForFunction(() => Boolean(customElements.get("model-viewer")), null, { timeout: 15000 });
  await page.waitForTimeout(2500);
  check(true, "3D viewer loaded from the vendored script");
}
await page.screenshot({ path: `${out}/create.png` });

await page.click("#kinds button:has-text('Speech')");
await page.fill("#m-prompt", "Testing the Create page from the drive.");
await page.click("#m-make");
await page.waitForSelector("#m-job audio", { timeout: 240000 });
check(true, "speech made through the page");
await page.screenshot({ path: `${out}/create-speech.png` });

await page.click("#new-chat");
if (process.env.IMAGE) {
  await page.setInputFiles("#attach-file", process.env.IMAGE);
  await page.waitForSelector("#chips .chip img");
  check(true, "attached picture shows as a chip");
}
check(await page.locator("#record").isVisible(), "voice-note button is there");
await page.screenshot({ path: `${out}/chat-attach.png` });

await page.setViewportSize({ width: 390, height: 800 });
await page.click("#menu");
await page.click("#show-create");
await page.waitForSelector("#kinds button");
check(!(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)), "no sideways scroll on a phone");
await page.screenshot({ path: `${out}/create-phone.png` });
check(errors.length === 0, `console errors: ${errors.length ? errors.join(" | ") : "none"}`);
await browser.close();
process.exit(failed ? 1 : 0);
