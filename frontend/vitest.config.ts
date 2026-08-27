import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "node:path";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    coverage: {
      provider: "v8",
      include: ["src/**"],
      exclude: [
        // tygo-generated types from backend/api/types — never hand-edited.
        "src/lib/api/generated/**",
        // test files and ambient type declarations carry no runnable logic.
        "**/*.test.{ts,tsx}",
        "**/*.d.ts",
        // config files under src, if any.
        "src/**/*.config.*",
        // non-code assets swept in by `src/**`; the v8 remapper cannot parse
        // them and would emit a spurious PARSE_ERROR for uncovered files.
        "**/*.css",
        "**/*.ico",
      ],
      reporter: ["text-summary", "html", "json-summary"],
      // MEASURED-CURRENT ratchet floors (measured: lines 26.8 / statements 26.8 /
      // functions 17.5 / branches 21.3). Set a small buffer below measured so CI
      // is deterministic-green while still blocking regression. Do NOT lower.
      thresholds: {
        lines: 25,
        statements: 25,
        functions: 16,
        branches: 20,
      },
    },
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
    },
  },
});
