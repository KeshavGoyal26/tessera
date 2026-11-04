import js from "@eslint/js";
import { defineConfig, globalIgnores } from "eslint/config";
import tseslint from "typescript-eslint";

export default defineConfig(
  globalIgnores(["dist/", "src/gen/", "coverage/", "test-results/", "playwright-report/"]),
  js.configs.recommended,
  tseslint.configs.recommended,
  {
    files: ["e2e/**", "scripts/**", "playwright.config.ts"],
    languageOptions: { globals: { process: "readonly", console: "readonly" } },
  },
  {
    rules: {
      "@typescript-eslint/consistent-type-imports": "error",
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
    },
  },
);
