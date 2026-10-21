# Repository agent instructions

## GitHub operations

- Run GitHub network operations such as `gh auth status`, `gh pr create`, `gh pr view`, and `git push` with network-enabled execution from the first attempt. In the Codex workspace sandbox, use `sandbox_permissions: require_escalated` for these commands.
- Do not interpret a restricted-network failure from `gh` or Git as an invalid token. Verify authentication with network access before reporting a credential problem or changing tools.
- Prefer the GitHub CLI for creating and updating pull requests. Use browser interaction only if the network-enabled CLI is unavailable or fails for a confirmed reason.
- Keep credential values out of command output, logs, and pull request descriptions.
