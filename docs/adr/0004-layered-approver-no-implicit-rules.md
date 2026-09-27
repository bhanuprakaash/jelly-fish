# Layered approver; approval rules are never implicit

Inside the sandbox, actions are free, since the container limits the blast radius. Tools with external side effects are gated. The **Approver** runs in order: explicit **Approval Rules**, then **Jev** (a platform-provided decision model returning allow / ask / deny with a confidence; below the threshold, unavailable, or slow (>5s) goes to a human). Connector hints (`readOnlyHint`, `destructiveHint`) seed the defaults. The harness may *suggest* a rule ("you approved this 3× in 7 days, always allow?"), but only a human creates one. Silent auto-approval learned from behavior was rejected as unsafe and confusing.

**Amendment (2026-09-27):** dropped the small-LLM fallback below Jev — the chain is now just Approval Rules, then Jev, then a human. Full spec: [approver.md](../design/approver.md).
