# GitHub MCP: issue_triage (Copilot excluded)

Use when the user asks to **find, read, or triage issues** (status, labels, comments, sub-issues).

## Tools
- `mcp__github__search_issues` (preferred: targeted queries)
- `mcp__github__list_issues` (fallback: broad scans by state/labels/since)
- `mcp__github__issue_read` (`get`, `get_comments`, `get_sub_issues`, `get_labels`)
- `mcp__github__get_label` (inspect/validate label metadata)

## Recommended flow
1. Find candidates:
   - Prefer `mcp__github__search_issues(query: ...)` for precision.
   - Use `mcp__github__list_issues(owner, repo, state?, labels?, since?)` for a simple listing.
2. Read details: `mcp__github__issue_read(method:"get", owner, repo, issue_number)`
3. Pull context:
   - Comments: `mcp__github__issue_read(method:"get_comments", ...)`
   - Labels: `mcp__github__issue_read(method:"get_labels", ...)`
   - Sub-issues: `mcp__github__issue_read(method:"get_sub_issues", ...)`

## Query tips
- `search_issues` uses GitHub search syntax; include repo scoping when possible (e.g., `repo:OWNER/REPO is:open label:bug`).
