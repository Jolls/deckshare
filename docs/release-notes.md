# Release notes

User-facing changes, newest first. Plain language: what a learner or instructor can now do.
Embedded in the binary and shown at `/release-notes` (#266). One `## [x.y.z] - date` section per
user-facing release, covering everything since the previous one; the patch releases in between
don't get sections of their own. Under it, `### New features`, `### Bug fixes` and
`### Security` (omit empty ones), each a list of `- Title: what changed` bullets (a bullet may
continue on indented lines). The section's version must also appear in `CHANGELOG.md`.

## [0.3.17] - 2026-10-07

### New features
- Colour scheme and accent: Light, Dark or Auto, plus eight accent colours, under Appearance in
  Settings. Cards follow the scheme too.
- Stats page: Your recall, pass rate, reviews and cards due, with charts.
- Release notes: This page, and a bar that appears after an update.
- Password reset links: An admin can generate a one-time link if you forget your password.
- Suspend, bury and flag: Set a card aside while studying or from the notes list.
- Closed instances: Sign-ups can be turned off.

### Bug fixes
- Importing a deck no longer misreads out-of-range values in a review history, which could turn
  an odd rating into an "Again".
- The Settings page no longer shows a "no value" placeholder for your retention target or version
  after saving.
- Note types, tags and deck names now have sensible size limits.

### Security
- Changing your password also cancels any reset link still outstanding.
