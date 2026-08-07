---
name: done
description: Use when a PR was just merged and deleted on the remote, to return the local repo to a clean main and remove stale local branches.
---

# Post-Merge Cleanup

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
   If `-d` refuses (unmerged), stop and confirm with the user before using `-D` — don't force-delete without asking, it can discard unmerged work.

## Common Mistakes

- Deleting a branch that isn't actually merged (`-d` will refuse — respect that refusal, don't reach for `-D` automatically).
- Skipping `git fetch --prune` — without it, deleted remote branches don't show as `gone` and get missed.
