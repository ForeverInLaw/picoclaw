param(
    [Parameter(Mandatory = $true)]
    [string]$SkillName,
    [switch]$DryRun,
    [string]$OutputDir = "skills"
)

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$workspaceRoot = (Resolve-Path (Join-Path $scriptDir "..\\..\\..")).Path

if ($SkillName -notmatch '^[a-z0-9]+(-[a-z0-9]+)*$') {
    throw "Skill name must use lowercase letters, numbers, and hyphens only."
}

if ([System.IO.Path]::IsPathRooted($OutputDir) -or $OutputDir -match '(^|[\\/])\.\.([\\/]|$)') {
    throw "OutputDir must be a relative path under the workspace root."
}

$targetDir = Join-Path $workspaceRoot $OutputDir
$targetDir = Join-Path $targetDir $SkillName
$title = ($SkillName -split '-') | ForEach-Object {
    if ($_.Length -eq 0) { return $_ }
    $_.Substring(0, 1).ToUpperInvariant() + $_.Substring(1).ToLowerInvariant()
}
$title = ($title -join ' ')

$content = @"
---
name: $SkillName
description: "Describe what this skill solves and when to use it."
---

# $title

State the problem and the reusable solution.

## Quick Reference

| Situation | Action |
|-----------|--------|
| Trigger | What to do |

## Workflow

1. First step
2. Second step
3. Verification step

## Gotchas

- Important caveat
- Common failure mode

## Source

- Learning ID: LRN-YYYYMMDD-XXX
- Extraction Date: YYYY-MM-DD
"@

if ($DryRun) {
    Write-Output "Would create:"
    Write-Output "  $targetDir"
    Write-Output "  $(Join-Path $targetDir 'SKILL.md')"
    Write-Output ""
    Write-Output $content
    exit 0
}

if (Test-Path $targetDir) {
    throw "Target already exists: $targetDir"
}

New-Item -ItemType Directory -Path $targetDir -Force | Out-Null
Set-Content -Path (Join-Path $targetDir "SKILL.md") -Value $content -NoNewline
Write-Output "Created $(Join-Path $targetDir 'SKILL.md')"
