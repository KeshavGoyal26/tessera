import { spawnSync } from "node:child_process";
import { defineConfig, devices } from "@playwright/test";

// E2E runs the real server (`go run ./cmd/tessera --dev`) and the built web demo. Without a Go
// toolchain the specs skip with a message (see e2e/room.spec.ts).
const hasGo = spawnSync("go", ["version"]).status === 0;
const external = process.env.TESSERA_E2E_URL !== undefined;
const SERVER_PORT = 7880;
const DEMO_PORT = 4173;

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.spec.ts",
  fullyParallel: false,
  workers: 1,
  timeout: 60_000,
  reporter: "list",
  use: { baseURL: `http://127.0.0.1:${DEMO_PORT}` },
  webServer: [
    ...(hasGo && !external
      ? [
          {
            command: "cd ../.. && go run ./cmd/tessera --dev",
            url: `http://127.0.0.1:${SERVER_PORT}/rtc`,
            reuseExistingServer: true,
            timeout: 120_000,
          },
        ]
      : []),
    {
      command: `npm --prefix ../../examples/web-demo run build && npm --prefix ../../examples/web-demo run preview -- --host 127.0.0.1 --port ${DEMO_PORT} --strictPort`,
      url: `http://127.0.0.1:${DEMO_PORT}`,
      reuseExistingServer: true,
      timeout: 120_000,
    },
  ],
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        permissions: ["camera", "microphone"],
        launchOptions: {
          // PW_CHROMIUM_PATH points at a preinstalled Chromium when `playwright install` is unavailable.
          ...(process.env.PW_CHROMIUM_PATH ? { executablePath: process.env.PW_CHROMIUM_PATH } : {}),
          args: [
            "--use-fake-ui-for-media-stream",
            "--use-fake-device-for-media-stream",
            "--auto-select-desktop-capture-source=Entire screen",
            "--allow-loopback-in-peer-connection",
          ],
        },
      },
    },
    {
      name: "firefox",
      use: {
        ...devices["Desktop Firefox"],
        launchOptions: {
          firefoxUserPrefs: {
            "media.navigator.streams.fake": true,
            "media.navigator.permission.disabled": true,
          },
        },
      },
    },
  ],
});
