# GitHub MCP: issue_workflow (Copilot excluded)

Use when the user asks to **fix an issue end-to-end** (understand → code change → PR → merge → close).

## Tools
- Understand scope: `mcp__github__issue_read`
- Status updates: `mcp__github__add_issue_comment`
- PR lifecycle: `mcp__github__create_pull_request`, `mcp__github__update_pull_request`, `mcp__github__pull_request_read`, `mcp__github__update_pull_request_branch`
- Close/duplicate handling (if needed): `mcp__github__issue_write`

## Required Flow (Test driven development, follow extremely closely)
1. Read issue: `mcp__github__issue_read(method:"get", owner, repo, issue_number)` + `get_comments`
2. Research task information and requirements very in-depth. Use "go doc" actively.
3. ALWAYS, and this is EXTREMELY IMPORTANT, run `git worktree add -b feat/feat-name worktrees/feat-name main`
4. Start by implementing a thorough suite of unit tests for your task FIRST.
5. Double-check implemented unit tests in great detail, be extremly strict here.
6. Create or update corresponding README.md carefully, keep it concise though.
7. Implement thorough and highly detailed code to fulfill unit test requirements.
8. Run respective unit tests and make ALL of them pass to 100% without manipulating test files or skipping tests.
9. Open PR: `mcp__github__create_pull_request(owner, repo, head:<branch>, base:<base>, title, body)`
   - Include “Fixes #<issue>” in PR body to auto-close on merge.
10. Link PR back to issue: `mcp__github__add_issue_comment(owner, repo, issue_number, body)`
11. Verify PR status/scope: `mcp__github__pull_request_read(method:"get_status"|"get_files"|"get_diff", ...)`
12. If base moved: `mcp__github__update_pull_request_branch(owner, repo, pullNumber, expectedHeadSha?)`

## Practical notes
- Use `pull_request_read(method:"get")` to grab head/base SHAs for traceability.
