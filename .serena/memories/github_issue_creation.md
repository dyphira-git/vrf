# GitHub MCP: issue_creation (Copilot excluded)

Use when the user asks to **create a new issue** (or a batch of issues) and optionally label/assign/milestone it.

ALWAYS refer to .github/ISSUE_TEMPLATE for available issue templates.

## Tools
- `mcp__github__get_me` who you are
- `mcp__github__list_issue_types` (discover valid issue `type` values for an org)
- `mcp__github__get_label` (validate a label exists before applying)
- `mcp__github__issue_write` (create the issue)
- `mcp__github__add_issue_comment` (add follow-up context/checklists)
- `mcp__github__sub_issue_write` (attach existing sub-issues to a parent, use heavily for detailed issues)

## Recommended flow
1. If you need the repo’s supported issue types: `mcp__github__list_issue_types(owner)`
2. If you’re applying labels and want to avoid typos: `mcp__github__get_label(owner, repo, name)`
3. Create: `mcp__github__issue_write(method:"create", owner, repo, title, body, labels, assignees(yourself), milestone?, type?)`
4. Add extra details: `mcp__github__add_issue_comment(owner, repo, issue_number, body)`
5. Structure work (if parent/child): `mcp__github__sub_issue_write(method:"add", owner, repo, issue_number:<parent>, sub_issue_id:<child_id>)`

## Practical notes
- Prefer putting acceptance criteria + repro steps in the initial `body`.
- You can create multiple issues efficiently by batching `issue_write` calls.
