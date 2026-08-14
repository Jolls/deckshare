// Package render renders note-type templates ({{Field}}, cloze, conditionals) to HTML and
// sanitises the result (architecture.md §8). It is pure: no internal/db import, no net/http
// import, no I/O. Callers build Note/Template structs from their own data and get back a Card
// whose Rendered.HTML is sanitised and safe to emit unescaped; sanitiseCardHTML itself stays
// unexported, since there is no legitimate caller of it outside this package.
//
// A {{type:Field}} answer-input widget is never part of sanitised HTML -- render leaves an
// unguessable placeholder in Rendered.HTML and the caller splices in the real widget afterwards
// with TypeAnswerInput / TypeAnswerExpected, entirely outside the sanitiser.
//
// Call shape for a future caller (e.g. the reviewer, per card in a batch):
//
//	card, err := render.RenderCard(tmpl, note, dbCard.Ordinal, noteType.IsCloze)
//	q := render.TypeAnswerInput(card.Question)        // question side
//	a := render.TypeAnswerExpected(card.Answer)        // answer side
//	css, dropped := render.SanitiseCSS(noteType.Css)   // once per note type per page, never per card
//	// page: <style>{{.CSS}}</style>
//	//       <article hidden data-card-id class="enshu-card card">{{.Q}}</article>
package render
