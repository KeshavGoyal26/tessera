import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // Playwright specs live in e2e/ and must not be collected by vitest.
    include: ["test/**/*.test.ts"],
    environment: "node",
  },
});
