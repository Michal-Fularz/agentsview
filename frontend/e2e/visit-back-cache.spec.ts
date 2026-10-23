import { test, expect } from "@playwright/test";
import { SessionsPage } from "./pages/sessions-page";
import { visitServer } from "./helpers/visit-server";

test.skip(
  ({ browserName }) => browserName !== "chromium",
  "This regression exercises Chromium's back/forward cache.",
);
test.use({
  launchOptions: {
    channel: "chromium",
    ignoreDefaultArgs: ["--disable-back-forward-cache"],
  },
});

test("Back restoration starts a fresh visit", async ({ page, baseURL }) => {
  const server = await visitServer(baseURL!, false, true);
  try {
    await page.addInitScript(() => {
      Object.assign(window, {
        restored: false,
      });
      window.addEventListener("pageshow", (event) =>
        Object.assign(window, { restored: event.persisted }),
      );
    });
    const enabled = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === "/api/v1/telemetry/events" &&
        response.request().postDataJSON()?.event === "app_opened",
    );
    await page.goto(server.url);
    await expect(new SessionsPage(page).sessionItems.first()).toBeVisible();
    await enabled;
    await page.goto(`${server.url}/away`);
    await expect.poll(() => server.visits.length).toBe(1);
    await page.goBack({ waitUntil: "commit" });
    await expect
      .poll(() => page.evaluate(() => (window as unknown as { restored: boolean }).restored))
      .toBe(true);
    await page.close();
    await expect.poll(() => server.visits.length).toBe(2);
  } finally {
    await server.close();
  }
});
