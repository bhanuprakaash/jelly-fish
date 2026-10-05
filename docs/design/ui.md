# Web UI: Visual Design

Status: design, 2026-10-03. Terms follow [`CONTEXT.md`](../../CONTEXT.md). Covers the look of the React PWA in `web/`: theme, tokens, buttons, chat cards and the main screens. Behaviour stays in the existing specs ([streaming.md](streaming.md), [approver.md](approver.md), [agents-skills.md](agents-skills.md), [memory.md](memory.md), [mcp-client.md](mcp-client.md), [usage-metering.md](usage-metering.md), [notifications.md](notifications.md)); this doc only says how it looks.

Prototypes live in [`ui/`](ui/) as Design canvas sources (`*.dc.html` plus `canvas.json`). They render only inside the canvas editor; see [ui/README.md](ui/README.md).

## 1. Summary

- Theme: **ink and vermilion**, minimal and quiet. Paper background, ink type, one red. Inspired by sakana.ai.
- Every Session is a **jellyfish** drawn in one ink line with a translucent jelly-blue bell. Delegation shows as tentacles to smaller jellies (Child Sessions).
- Colour is spent only on state. **Jelly blue** means alive (running). **Vermilion** means the user is needed (approval, budget). Nothing else is loud.
- One font family: Manrope for UI and text, JetBrains Mono for tools, arguments, events and money.
- All themes share one set of semantic tokens (§3).
- The UI never says "Jev"; it says "auto mode".

## 2. Session status → glyph

From the status enum in [event-log.md](event-log.md). The glyph is the jellyfish icon; colour plus a text label, never colour alone.

| Status | Colour | Motion | Chat List badge ([streaming.md](streaming.md)) |
|---|---|---|---|
| `runnable`, `running` | jelly blue `#2F6E9E` | soft pulse ring | spinner |
| `awaiting_approval` | vermilion `#C8352A` | beacon pulse (the only fast motion) | dot, also on the parent |
| `awaiting_children` | indigo `#2F4F7F` | soft pulse | spinner |
| `sleeping` | grey `#9A9893` | slow fade | spinner |
| `awaiting_user` | pale grey `#8A8984` | none | none |
| `completed` | moss `#4F6B47` | none | none |
| `failed` | rust `#7A2A20` | none | "Retry?" |

`prefers-reduced-motion` replaces every animation with a static ring.

## 3. Tokens

The app has four themes: `system`, `jellyfish-light`, `jellyfish-dark` and `classic` (a neutral dark theme with a sky accent). `system` follows the OS. The user picks one per device in Settings → Appearance. Each theme sets the same tokens. The colours below are the jellyfish themes.

| Token | Jellyfish light | Jellyfish dark | Use |
|---|---|---|---|
| `bg` | `#FAFAF7` | `#0F1012` | page |
| `surface` | `#FFFFFF` | `#17191C` | cards, inputs |
| `sunk` | `#F4F3EF` | `#1E2125` | notices, hover, code chips |
| `line` | `#E8E6E0` | `#272A2F` | hairlines, card borders |
| `line2` | `#DDDAD3` | `#363A40` | secondary button border |
| `ink` | `#161616` | `#F1F0EB` | text, primary button |
| `ink2` | `#3A3A37` | `#CFCEC8` | secondary text |
| `muted` | `#6B6A65` | `#9B9A94` | captions, meta |
| `accent` | `#9CC9E8` | `#9CC9E8` | rings, bars, focus |
| `accent-ink` | `#2F6E9E` | `#A9D2EE` | running text, links |
| `accent-tint` | `#EEF5FB` | `rgba(156,201,232,.10)` | user bubble, selected row |
| `accent-line` | `#D6E7F4` | `rgba(156,201,232,.28)` | composer, accent borders |
| `attention` | `#C8352A` | `#E0503F` | approval fill |
| `attention-tint` | `#FFF7F4` | `rgba(224,80,63,.08)` | approval card |
| `attention-line` | `#EBC2BB` | `rgba(224,80,63,.38)` | approval border |
| `danger` | `#8E3326` | `#E08A7C` | danger text, failed |
| `success` | `#4F6B47` | `#93B88A` | done |
| `waiting` | `#2F4F7F` | `#A3B8DE` | waiting on children, files |

Type: Manrope 400/500 body (14–15px), 600 titles with `-0.025em` to `-0.035em` tracking; JetBrains Mono 12–13px. Radii: buttons 10px, cards 16px, panels 24px, chips and avatars fully round. Cards use a hairline, no shadow.

## 4. Buttons

Six variants, each with default, hover, pressed, focus, disabled and loading states. Focus is a 2px `bg` gap then a 2px `accent-ink` ring. Disabled is 38% opacity.

| Variant | Look | Use |
|---|---|---|
| Primary | `ink` fill, inverted text | the one main action per card: Send, Save, Keep |
| Secondary | `surface`, `line2` border | alternatives: Open, For this project, Retry now |
| Ghost | text only, `sunk` on hover | Not now, Collapse, Approve all |
| Jelly | `accent-tint` fill, `accent-ink` text | live things: Open swarm, Start a session |
| Attention | `attention` fill, `bg` text | only Approve and Allow more |
| Danger | transparent, `danger` text and border | Stop, Deny, Delete |

Sizes: 32px (dense desktop rows), 40px (default), 48px; mobile primary 52px. Icon buttons are 40px round and always carry `aria-label`. The permission mode control is a segmented group (Ask, Auto, Full-auto). Status chips look like pills but are never buttons.

## 5. Chat cards

Every card: header (icon, title, meta on the right), body, then actions with the primary first. Prototype: `Components.dc.html` (light and dark).

- **Messages**: the user's message is an `accent-tint` bubble on the right; the assistant's text is plain prose, no card. Thinking is a collapsed pill ("Thought for 6s").
- **Tool calls**: one card per turn, one row per call with a state icon, mono name, note and an `approved_by` chip (rule, auto mode, you, sandbox). States: done, running, denied ("denied by user: …"), timed out, outcome unknown (interrupted), skipped. A row expands to show arguments and result in a code block.
- **Web search**: query line plus numbered sources with their domain.
- **Child Session**: jelly avatar, Agent name, task, status chip, the latest step, then Open (secondary) and Stop (danger), with tokens and dollars on the right.
- **Approval**: the only vermilion card. "‹Agent› wants approval", "1 of N", tool and arguments, then Approve once (attention), For this project, Everywhere (secondary), Deny (danger), Approve all (ghost). A rule suggestion sits under a hairline.
- **Auto mode lines**: inline text with a shield icon, no card and no actions.
- **Elicitation**: "‹Connector› asks", a form, Send and Decline.
- **Notices**: provider busy (sunk, Retry now and Stop retrying), provider error (rust tint, Update key and Switch model), budget reached (vermilion border, Allow more and Stop here).
- **Artifact**: file tile, name, version pills, size, download and Save to project.
- **Memory**: a "Remembered …" chip with Undo; a write from a web page asks Keep, Edit or Delete.
- **Compaction**: a hairline divider, "Earlier messages summarized · View summary".
- **/cost**: tokens, dollars, turns, auto mode checks, one line per child and the Budget left.

## 6. Screens

| Prototype | Screen |
|---|---|
| `Main.dc.html` | Desktop chat: Chat List with a swarm summary, two-row session header, transcript, composer with the slash menu inside it, Artifacts panel and live events |
| `Swarm.dc.html` | One session as a live tree of jellies joined by tentacles, with an inspector for the selected child |
| `MobileList`, `MobileChat`, `MobileChatDark`, `MobileApproval` | Phone: chat list, chat in light and dark, approval opened from a push |
| `Agents.dc.html` | Agents as a school of jellies (size by role, dotted lines for who delegates to whom) beside a spec sheet |
| `Skills.dc.html` | Numbered index of skills by slash command, import and review |
| `Connectors.dc.html` | Connected list, catalog, add flow with per-tool switches |
| `Permissions.dc.html` | Mode cards, the "what runs without asking" matrix, always-allow rules |
| `Memory.dc.html` | About me and This project columns, pending review, Tidy suggestions |
| `Usage.dc.html` | Daily tokens by provider, model table, quota gauges, /cost |
| `Inventory.dc.html` | Every user-facing element the design docs imply, mapped to a screen |

The slash menu opens inside the composer and pushes the transcript up; it never floats over messages.

## 7. Open

- `Main`, `Swarm`, `MobileList` and `MobileApproval` still use their own button styles; move them to §4.
- Login, Settings → Account, Provider Keys and Admin screens are restyled in code with the same tokens, but not drawn.
- Buttons have no loading state.
- The mobile primary button is not 52px high.
- The round icon button (RowMenu ⋯) is not 40px.
- The §3 type scale and title tracking are not applied.
- The provider-error notice has no danger tint.
- In `jellyfish-dark`, the Pending review chip text has 4.2:1 contrast. The target is 4.5:1.
- The PWA manifest and launch splash stay dark for the light themes.
- The service worker precaches every font subset. Latin only would save about 100 KB.
