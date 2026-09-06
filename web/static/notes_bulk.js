// Deck notes list bulk-selection UI (#241): per-row checkboxes, select-all-on-page, and
// shift-click range select, driving the apply-to-many action bar. Plain DOM -- htmx/Alpine are
// already loaded globally (layout.html) but neither is needed for checkbox bookkeeping.
(function () {
  'use strict';

  // The container is a plain <div>, not a <form> -- checkboxes live in the table and submit
  // through the separate #bulk-notes-form via their own form="" attribute (deck.html), so a
  // single-row Delete form elsewhere in this container never carries the selection along.
  document.querySelectorAll('[data-bulk-notes]').forEach(function (container) {
    var bar = container.querySelector('[data-bulk-bar]');
    var count = container.querySelector('[data-bulk-count]');
    var rows = Array.prototype.slice.call(container.querySelectorAll('[data-select-row]'));
    var selectAll = container.querySelector('[data-select-all]');
    var tagsInput = container.querySelector('input[name="tags"]');
    var lastIndex = null;

    // The tags field's own form (#bulk-notes-form) has no other input before it in tree order,
    // so an implicit Enter-submit would run "Add tags" on whatever's currently typed -- require
    // an explicit button click instead.
    if (tagsInput) {
      tagsInput.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') e.preventDefault();
      });
    }

    function refresh() {
      var checked = rows.filter(function (cb) { return cb.checked; }).length;
      if (bar) bar.hidden = checked === 0;
      if (count) count.textContent = String(checked);
      if (selectAll) selectAll.checked = checked > 0 && checked === rows.length;
    }

    rows.forEach(function (cb, index) {
      cb.addEventListener('click', function (e) {
        if (e.shiftKey && lastIndex !== null) {
          var lo = Math.min(lastIndex, index);
          var hi = Math.max(lastIndex, index);
          for (var i = lo; i <= hi; i++) rows[i].checked = cb.checked;
        }
        lastIndex = index;
        refresh();
      });
    });

    if (selectAll) {
      selectAll.addEventListener('change', function () {
        rows.forEach(function (cb) { cb.checked = selectAll.checked; });
        // "Last clicked row" no longer has an obvious meaning after a bulk toggle -- clear it so
        // a later shift-click starts a fresh range instead of anchoring on a stale row.
        lastIndex = null;
        refresh();
      });
    }

    refresh();
  });
})();
