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

  // "Add cloze deletion" wraps the field's current selection in {{cN::...}} (render.scanCloze's
  // format, internal/render/cloze.go), numbering it one past the highest cN already used
  // anywhere in the note so cloze numbers stay unique across fields.
  document.addEventListener('click', function (e) {
    var btn = e.target.closest('.cloze-btn');
    if (!btn) return;
    var el = btn.previousElementSibling;
    if (!(el instanceof HTMLTextAreaElement)) return;

    var form = btn.closest('form');
    var maxNum = 0;
    form.querySelectorAll('textarea[name="field[]"]').forEach(function (ta) {
      var re = /\{\{c(\d+)::/g;
      var m;
      while ((m = re.exec(ta.value)) !== null) {
        var n = parseInt(m[1], 10);
        if (n > maxNum) maxNum = n;
      }
    });

    var open = '{{c' + (maxNum + 1) + '::';
    var close = '}}';
    var start = el.selectionStart;
    var end = el.selectionEnd;
    var selected = el.value.slice(start, end);
    el.value = el.value.slice(0, start) + open + selected + close + el.value.slice(end);
    el.focus();
    el.selectionStart = start + open.length;
    el.selectionEnd = start + open.length + selected.length;
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
})();
