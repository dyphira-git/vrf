# GitHub MCP: issue_completion

## Tools
- Status updates: `mcp__github__add_issue_comment`
- PR lifecycle: `mcp__github__create_pull_request`, `mcp__github__update_pull_request`, `mcp__github__pull_request_read`, `mcp__github__update_pull_request_branch`
- Close/duplicate handling (if needed): `mcp__github__issue_write`

## Workflow
1. Run respective unit tests and make ALL of them pass to 100% without manipulating test files or skipping tests.
2. Open PR: `mcp__github__create_pull_request(owner, repo, head:<branch>, base:<base>, title, body)`
   - Include “Fixes #<issue>” in PR body to auto-close on merge.
3. Link PR back to issue: `mcp__github__add_issue_comment(owner, repo, issue_number, body)`
4. Verify PR status/scope: `mcp__github__pull_request_read(method:"get_status"|"get_files"|"get_diff", ...)`
5. If base moved: `mcp__github__update_pull_request_branch(owner, repo, pullNumber, expectedHeadSha?)`
6. Push committed files to created PR using `mcp__github__push_files` with the following syntax:

```json
{
    "owner": "dgtlkitchen",
    "repo": "vrf",
    "branch": "fix/<issue>",
    "message": "fix(general): fix description | Fixes #<issue>",
    "files": [
        {"path": "path/in/repo/file1.txt", "content": "file contents as a string"},
        {"path": "src/app.ts", "content": "..."}
    ]
}
```
