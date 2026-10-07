# Release notes

User-facing changes, newest first. Plain language: what a learner or instructor can now do.
Embedded in the binary and shown at `/release-notes` (#266). Only versions with a user-visible
change get a section; internal-only releases are simply absent. Format: `## [x.y.z] - date`
followed by `- ` bullets (a bullet may continue on indented lines). Every version here must also
appear in `CHANGELOG.md`.

## [0.3.16] - 2026-10-06
- Pick an accent colour under Appearance in Settings: Azure (the default), Blue, Indigo, Purple,
  Pink, Red, Orange or Green. It works with Light, Dark and Auto, and is remembered on every device. Choices preview instantly before you save.

## [0.3.15] - 2026-10-06
- A new Release notes page lists what changed in each version. When DeckShare is updated, a bar
  under the top of the page links to it; dismiss the bar and it stays away until the next update.
- The current version is shown at the bottom of every page and links to the release notes.

## [0.3.14] - 2026-10-06
- Choose Light, Dark or Auto colour scheme under Appearance in Settings. Auto follows your
  device. Your cards keep their light background so card styling stays readable.

## [0.3.13] - 2026-10-06
- A new Stats page, linked from your deck list, shows your own recall, pass rate, reviews over the
  last 30 days and cards due, overall and per deck, with charts of reviews and pass rate per day.

## [0.3.12] - 2026-09-24
- If you forget your password, the person running your DeckShare can generate a one-time reset link
  for you. Opening it lets you choose a new password and signs you out everywhere.
- Changing your password also cancels any reset link still outstanding.
- Importing a deck no longer misreads out-of-range values in a review history, which could turn an
  odd rating into an "Again".
- Note types, tags and deck names now have sensible size limits.

## [0.3.11] - 2026-09-11
- Suspend, bury and flag a card while studying or from the notes list.
- A closed instance can turn off new sign-ups.
- The Settings page no longer shows a "no value" placeholder for your retention target or version
  after saving.
