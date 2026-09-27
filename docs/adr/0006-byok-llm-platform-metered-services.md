# BYOK for LLMs, platform-metered for everything else

Users pay providers directly with their own **Provider Keys**. Sandbox compute, **Jev**, and web search are **Platform Services**: the platform holds those keys, meters **Usage** per session, and shows it on the dashboard as the basis for future billing (users may optionally bring a search key). This keeps the product usable by non-technical users, who won't have TypeSafe or Tavily keys. The cost is that the platform carries real spend, so **Quotas** exist from day one.

**Amendment 2026-09-27 (web search deferred):** in the base version web search is not a Platform Service. The model uses its Provider's built-in search (Anthropic, OpenAI, Gemini), billed to the user's own key like any LLM usage. The platform search (`SearchProvider`, Brave/Tavily, Quota) comes after the base version. Jev stays the only Platform Service in the base version. Details: [provider-gateway.md](../design/provider-gateway.md) Decision 6a.
