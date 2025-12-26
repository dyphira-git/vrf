# GitHub MCP: issue_workflow

Use when the user asks to **fix an issue end-to-end** (understand → code change → PR → merge → close).

## Tools
- Understand scope: `mcp__github__issue_read`
- Status updates: `mcp__github__add_issue_comment`

## ALWAYS, and this is EXTREMELY IMPORTANT, run `git worktree add -b fix,feat,style/<issur-number> worktrees/fix,feat,docs-<issue-number> main`

### Important
Prefer using the multi_tool_use.parallel function in combination with either github tools or serena code search tools.

## Required Flow (Test driven development, follow extremely closely)
1. Read issue: `mcp__github__issue_read(method:"get", owner, repo, issue_number)` + `get_comments`
2. Research task information and requirements very in-depth. Use "go doc" actively.
3. Start by implementing a thorough suite of unit tests for your task FIRST.
4. Double-check implemented unit tests in great detail, be extremly strict here.
5. Create or update corresponding README.md carefully, keep it concise though.
6. Implement thorough and highly detailed code to fulfill unit test requirements.
