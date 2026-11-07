# Issue tracker: itsaplan

Team: Redstone (`redstone`)
Project: Korobka (`KOROBKA`, ref `redstone.KOROBKA`)

Use the connected itsaplan MCP tools for ticket operations.

- Publish tickets and specs as issues in the KOROBKA project.
- Scope issue searches to this project.
- Fetch a referenced ticket by its identifier (`KOROBKA-42`) with `get_issue_by_number`.
- Read issue descriptions, labels, status, and comments before acting.
- Resolve columns, issue types, and labels from `get_project`; pick columns by `stateType`, never by name.
- Preserve existing status and assignment unless the task requires a change.
- Check for existing tickets before creating duplicates.
- Use native subtasks (`parentId`) and issue links for blocking relationships.

Tickets created before the move were imported from Linear. Branch names and older
commits still cite `IVO-*` identifiers; each imported description starts with
`Перенесено из Linear: IVO-N`, so search KOROBKA for that marker to find the ticket.
