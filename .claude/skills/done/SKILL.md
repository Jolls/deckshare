---
name: done
description: Use when a PR was just merged and deleted on the remote, to return the local repo to a clean main and remove stale local branches.
---

# Post-Merge Cleanup

> Source: [Jolls/claude-skills](https://github.com/Jolls/claude-skills) (`skills/done`)
> — specialize downstream copies per-project; sync generic fixes both ways.

## Overview
After a PR merges and its remote branch is deleted, sync local `main` and remove the now-stale local feature branch(es). **Local only** — never runs `git push --delete` or any command that deletes a branch on the remote.

## When to Use
User says something like "merged and deleted", "clean up branches", "back to main", or any request to tidy up local git state after a PR merge.

## Steps

1. Switch to main and pull:
   ```
   git checkout main
   git pull
   ```
2. Update local refs to reflect branches already deleted on the remote (this does not delete anything remote — it only cleans up local bookkeeping), then list local branches:
   ```
   git fetch --prune
   git branch -vv
   ```
   Branches marked `[origin/<name>: gone]` had their remote already deleted (e.g. via GitHub's merge UI).
3. Delete each local branch that is `gone` **and already merged** (safe delete, local only):
   ```
   git branch -d <branch>
   ```
   `-d` also refuses branches merged via GitHub squash/rebase (the commits on main have different hashes). For a refused branch, verify it was merged with its tip intact before force-deleting:
   ```
   gh pr list --state merged --head <branch> --json number,headRefOid
   git rev-parse <branch>
   ```
   Use `git branch -D <branch>` only if a merged PR's `headRefOid` equals the local tip (nothing unpushed or committed after the merge). Otherwise stop and confirm with the user — `-D` can discard unmerged work.

## Common Mistakes
- Deleting a branch that isn't actually merged (`-d` will refuse — only escalate to `-D` after the merged-PR tip check in step 3).
- Skipping `git fetch --prune` — without it, deleted remote branches don't show as `gone` and get missed.
