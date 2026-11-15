import { defineConfig } from "@playwright/test";

// Hermetic: the page is served from the file in the tree and every API it
// calls is answered from testdata (see parity.spec.js); nothing reaches a
// network or a drsh.
export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.js/,
  reporter: [["list"]],
  use: { browserName: "chromium", headless: true, viewport: { width: 1200, height: 900 } },
});
