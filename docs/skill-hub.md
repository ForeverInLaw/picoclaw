# Skill Hub

Open **Agent → Skills** to search the configured registry, load more results, view a skill's details and install its selected version. The installed list refreshes after an install. Existing Markdown import, viewing and workspace-skill deletion remain available on the same page.

Hub search and detail respect `tools.skills` and `tools.find_skills`; installation also requires `tools.install_skill`. The configured registry must be enabled. The Hub uses the server's registry settings and keeps credentials out of the browser. The current adapter is ClawHub, using `tools.skills.registries.clawhub` and the existing optional auth token. The Hub does not accept a registry URL or token from the browser.

Config GET represents configured registry/GitHub tokens as `****`. Saving that unchanged sentinel or omitting the token preserves the stored credential. An explicit token replaces it; an explicit empty token removes it. Changing the ClawHub base URL without supplying a replacement clears the previous saved token, so it is not moved to another endpoint. Registry requests also strip Authorization on redirects to another origin. Reflected credentials are removed from registry metadata and error bodies do not enter browser or model results.

Install uses the existing registry download and ZIP safety checks, including path traversal, symlink and file-size rejection. Archives are staged under the workspace and checked through the existing skills loader before activation. Malware-blocked skills never become installed; suspicious flags are shown. Registry provenance uses the same private atomic metadata file as agent installs. Existing directories produce a conflict and are not overwritten. Hub, import and delete writes are serialized.

Search pages contain up to 20 results in the UI. The existing search interface returns ranked prefixes rather than a remote cursor; the Hub provides bounded offsets over at most 200 results. Refine the query to find more specific skills.

Search and detail requests use TanStack Query cancellation signals. Canceling an install stops its active request; the installed list refreshes even after cancellation, because cancellation after activation cannot undo a completed install. A canceled or failed download cannot activate a partial archive. The UI reports errors and keeps server state in TanStack Query; search text and selection use granular Zustand selectors.

Focused verification uses local fake registries and ZIP fixtures:

```sh
go test -tags stdjson ./pkg/skills ./pkg/tools ./web/backend/api ./web/backend/middleware
go test -race -tags stdjson ./web/backend/api ./pkg/skills -run 'TestAuthenticatedSkillHub|TestSkillHubKeeps|TestSkillHubCanceled|TestRegistryCredentialsStay|TestRegistryErrors|TestRegistryRedirect'
cd web/frontend
pnpm test:hub
pnpm lint
pnpm build:backend
```
