// Conventional-commit rules for the CI commitlint gate (.github/workflows/ci.yml).
// Warn-only until 2026-08-26, then it flips to a blocking check.
// Matches the commit conventions in CLAUDE.md (feat/fix/refactor/test/docs/...).
export default {
  extends: ["@commitlint/config-conventional"],
};
