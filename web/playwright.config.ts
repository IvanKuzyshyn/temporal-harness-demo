import { defineConfig } from "@playwright/test";
const external = process.env.WEBCHAT_TEST_BROWSER_URL;
export default defineConfig({
  testDir: "./tests",
  timeout: 60000,
  expect: { timeout: 15000 },
  workers: 1,
  use: {
    baseURL: external ?? "http://127.0.0.1:13012",
    headless: true,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: external
    ? undefined
    : {
        command:
          "node node_modules/next/dist/bin/next start --hostname 127.0.0.1 --port 13012",
        url: "http://127.0.0.1:13012",
        reuseExistingServer: false,
        timeout: 30000,
      },
});
