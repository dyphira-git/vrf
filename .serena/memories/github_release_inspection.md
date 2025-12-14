# GitHub MCP: release_inspection (Copilot excluded)

Use when the user asks about **releases/tags**, e.g. “latest release”, “what’s in tag vX”, “list tags”.

## Tools
- Latest release: `mcp__github__get_latest_release`
- List releases: `mcp__github__list_releases`
- Release by tag: `mcp__github__get_release_by_tag`
- List tags: `mcp__github__list_tags`
- Tag details: `mcp__github__get_tag`

## Recommended flow
1. Latest: `mcp__github__get_latest_release(owner, repo)`
2. Enumerate: `mcp__github__list_releases(owner, repo, perPage?, page?)`
3. Specific: `mcp__github__get_release_by_tag(owner, repo, tag)`
4. Tags: `mcp__github__list_tags(owner, repo, perPage?, page?)` then `mcp__github__get_tag(owner, repo, tag)`
