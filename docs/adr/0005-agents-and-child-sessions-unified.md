# Custom agents and sub-agents are one mechanism

An **Agent** is only a saved configuration. A sub-agent is a **Child Session** (a normal session with `parent_id`) running some agent, started by the built-in `delegate(agent, task)` tool, with a depth cap. Child sessions get durability, budgets, approvals, and usage for free. The alternative, a separate lightweight in-process sub-agent mechanism, would duplicate all of that and be painful to retrofit.
