# Public evidence fixture

`http-status-codes.csv` is the IANA HTTP Status Code Registry downloaded on
2026-11-14 from:

https://www.iana.org/assignments/http-status-codes/http-status-codes-1.csv

The main regression uses the complete CSV unchanged. A second controlled
layout builds a shorter client-error table from its rows and places it under
the registry heading and classification notes from:

https://www.iana.org/assignments/http-status-codes/http-status-codes.xhtml

This controlled layout tests excerpt budgeting; it is not a claim about how
any scraper formats the live registry. The frozen data keeps the test offline.
The requested row is public protocol data, not a personal research example.

`http-status-codes.md` is a frozen Markdown rendering of the public registry
HTML at https://www.iana.org/assignments/http-status-codes, captured on
2026-11-14 through the configured Firecrawl page extractor. The extractor
service version was not recorded. It preserves the rendering that exposed
the passage-selection defect; tests do not require the live service.

SHA-256:

- CSV: `6098f56a5f25ca050313838886090a5e58a40ca72c2b44eb19d5ac7c9f6b4896`
- Markdown: `52e162d926a6bb6bbdb17feec026a6ce7a4bc3d64b72224a012901551da43812`
