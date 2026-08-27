import { dirname } from "path";
import { fileURLToPath } from "url";
import { FlatCompat } from "@eslint/eslintrc";
import prettier from "eslint-config-prettier";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

const compat = new FlatCompat({
  baseDirectory: __dirname,
});

const eslintConfig = [
  ...compat.extends("next/core-web-vitals", "next/typescript"),
  {
    ignores: [
      "node_modules/**",
      ".next/**",
      "out/**",
      "build/**",
      "next-env.d.ts",
      // tygo-generated from backend/api/types — never hand-edited, so linting
      // its unavoidable `any` (json.RawMessage) here is noise, not signal.
      "src/lib/api/generated/**",
    ],
  },
  // Must stay LAST: turns off ESLint rules that conflict with Prettier so the
  // formatter and linter don't fight over the same code.
  prettier,
];

export default eslintConfig;
