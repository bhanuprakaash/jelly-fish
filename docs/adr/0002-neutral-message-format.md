# Neutral message format; providers are adapters

The Event Log stores messages, tool calls, and thinking blocks in our own provider-neutral format. Each **Provider** adapter (Anthropic, OpenAI, Gemini; others later) translates to and from that format at the edge. This lets a user switch models in the middle of a session, and keeps provider quirks out of the core. We don't use a multi-provider library: the translation layer is core infrastructure we want to own. The neutral format carries its own version (`msg_v`) inside each payload that embeds a message, with upcasters kept in one place.
