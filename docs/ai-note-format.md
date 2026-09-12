# Formatting notes for AI agents

How an AI agent (Claude or otherwise) should structure flashcard notes it generates, so they
render correctly once imported into DeckShare. This is a producer-side guide — for how
DeckShare itself renders a note into HTML, see
[architecture.md §8](architecture.md#8-note-type-rendering).

## Basic note type

Two fields, `Front` and `Back`. One note → one card.

```
Front: What is the capital of France?
Back: Paris
```

- Keep `Front` a single question, not a compound one — one card should test one fact.
- `Back` can include brief elaboration, but front-load the answer; don't bury it in a
  paragraph.
- Plain text or simple HTML (`<b>`, `<i>`, `<br>`, `<ul>/<li>`) is fine. Content is sanitised
  on render, so don't rely on `<script>`, inline event handlers, or arbitrary CSS — they'll be
  stripped.

## Cloze note type

One field (`Text`, conventionally), with `{{c1::hidden text}}` markers. One note → one card
**per distinct cloze number**, not per marker.

```
Text: The mitochondria is the {{c1::powerhouse}} of the {{c2::cell}}.
```

This produces two cards: c1 hides "powerhouse" and reveals "cell" as context; c2 hides "cell"
and reveals "powerhouse" as context. Every cloze number *other* than the active one on a given
card shows as plain revealed text on both sides — don't omit the other markers thinking only
the active one matters.

Optional hint syntax: `{{c1::hidden::hint shown instead of [...]}}`.

Rules of thumb for an agent generating cloze notes:

- **Reuse `c1` for facts you want tested together** on the same card (e.g. two blanks in one
  sentence that only make sense answered jointly); use a new number (`c2`, `c3`, …) for facts
  that should be separate cards.
- Don't cloze trivial words (articles, connectives) — cloze the fact, not the sentence
  structure.
- One idea per cloze span. Don't wrap a whole clause in one `{{c1::…}}` when it contains two
  separate facts — split them into `c1` and `c2`.
- Numbering must start at `c1` and need not be contiguous across notes, but *within* a note
  every number you use should appear at least twice if you intend shared context, or once if
  it's a single fact.

## What not to include

- No `stability`, `due`, `interval`, or any scheduling field — notes carry content only.
  Scheduling state is computed server-side per user (see architecture.md §6) and is never
  something a note-authoring step sets.
- No note `guid` — DeckShare assigns/dedupes on import; don't invent one.
- No deck-assignment logic embedded in note text — deck is a property of the import, not the
  note content.

## Output shape

When generating a batch of notes for import, use one object per note:

```json
{ "noteType": "Basic", "fields": { "Front": "...", "Back": "..." }, "tags": ["optional"] }
{ "noteType": "Cloze", "fields": { "Text": "..." }, "tags": ["optional"] }
```

Tags are freeform strings, space-free (use `-` or `_` for multi-word tags), matching Anki's
convention.
