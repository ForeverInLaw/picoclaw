# Errors

Unexpected failures, command errors, provider errors, and integration breakages.

**Areas**: frontend | backend | infra | tests | docs | config | tools | models
**Statuses**: pending | in_progress | resolved | wont_fix | promoted

Use this template for new entries:

~~~~markdown
## [ERR-YYYYMMDD-XXX] command_or_component

**Logged**: ISO-8601 timestamp
**Priority**: high
**Status**: pending
**Area**: frontend | backend | infra | tests | docs | config | tools | models

### Summary
Brief description of what failed

### Error
```
Actual error message or output
```

### Context
- Command or operation attempted
- Input or parameters used
- Environment details if relevant

### Suggested Fix
Best known next action or prevention rule

### Metadata
- Reproducible: yes | no | unknown
- Related Files: path/to/file
- See Also: ERR-YYYYMMDD-XXX

---
~~~~
