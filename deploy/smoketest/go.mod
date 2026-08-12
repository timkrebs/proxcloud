// deploy/smoketest is a standalone, stdlib-only module (ADR-0016): a static Go
// binary with no runtime deps that the CD wave runs against staging and prod.
// It is intentionally NOT part of the backend module so the smoke black-box
// never imports server internals — it speaks only the public HTTP API.
module github.com/timkrebs9/proxcloud/deploy/smoketest

go 1.23
