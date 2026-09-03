// Field content is stored as raw HTML (architecture.md §8), and a bare "\n" collapses to a
// space when rendered. Anki's own editor inserts an actual <br> into the field on Enter; this
// does the same for our plain <textarea> fields so line breaks entered here render as line
// breaks on the card (issue #185).
(function () {
  'use strict';

  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Enter') return;
    var el = e.target;
    if (!(el instanceof HTMLTextAreaElement) || el.name !== 'field[]') return;

    e.preventDefault();
    var start = el.selectionStart;
    var end = el.selectionEnd;
    el.value = el.value.slice(0, start) + '<br>' + el.value.slice(end);
    el.selectionStart = el.selectionEnd = start + 4;
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
})();
