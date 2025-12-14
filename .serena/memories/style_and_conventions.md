# Style and conventions

## Decide required skills
Pick one of the following that works best with the task at hand, NEVER read both skill sets:
- ONLY EVER if given a github issue to work with read memories "github_issue_triage" and "github_issue_workflow".
- ONLY EVER if given the task to create github issues read memory "github_issue_creation"

## General conventions

- Follow standard Go/Cosmos SDK conventions: small packages, explicit error handling, deterministic logic in consensus-critical paths.
- NEVER edit generated protobuf files (`*.pb.go`, `*.pulsar.go`, `*.pb.gw.go`).
- Use Cosmos SDK Collections for on chain state management
