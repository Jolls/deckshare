// The reviewer's in-session queue (architecture.md §6). Owns everything that doesn't touch the
// network: which card is current, the local learning-steps requeue heuristic, unseen-count
// tracking, and revealing/advancing. It dispatches a refill-needed DOM event that htmx listens
// for on #review-refill; the grade batch POST is sent directly with fetch() (not through htmx --
// see flush()'s comment for why). Grading never awaits anything and never computes an FSRS
// value -- it looks up a precomputed branch already present on the hidden card node, or, for a
// card this session requeued, the fresh one the grade response brought back (CLAUDE.md §2.6, §2.7).
(function () {
  'use strict';

  var scriptTag = document.currentScript;

  var BACKOFF_SECONDS = [1, 2, 4, 8, 16, 30];
  var FLUSH_DEBOUNCE_MS = 2000;
  var REQUEUE_WINDOW_MS = 20 * 60 * 1000;
  var REFILL_THRESHOLD = 10;
  var RATING_GOOD = 3; // fsrs.Good (internal/fsrs/schedule.go) -- wire-identical rating ordinal

  var state = {
    deckId: null,
    userId: null, // acting account at page render; sent as ?u= so a switched-away tab is refused (#178)
    queue: [], // {cardId, el, branches, done, repeat}
    current: null, // index into state.queue
    cursor: '',
    exhausted: false,
    extraRounds: 0,
    pending: [], // ReviewEvents not yet sent
    inFlight: null, // ReviewEvents currently in an outstanding request, or null
    refillInFlight: false,
    revealStart: null,
    lastFlushAt: 0,
    backoffIndex: 0,
    flushTimer: null,
  };

  function init() {
    state.deckId = scriptTag.dataset.deckId;
    state.userId = scriptTag.dataset.userId || '';
    indexBatch(document.getElementById('review-queue'));
    showNext();

    document.addEventListener('keydown', onKeydown);
    document.body.addEventListener('htmx:afterSwap', onAfterSwap);
    document.body.addEventListener('htmx:beforeRequest', onFlagBeforeRequest);
    document.body.addEventListener('htmx:beforeSwap', onFlagBeforeSwap);
    var stage = document.getElementById('review-stage');
    if (stage) stage.addEventListener('click', onStageClick);
    var studyMore = document.getElementById('study-more');
    if (studyMore) studyMore.addEventListener('click', onStudyMoreClick);
    window.addEventListener('pagehide', flushOnUnload);
    document.addEventListener('visibilitychange', function () {
      if (document.visibilityState === 'hidden') flush();
    });
  }

  // -- Queue ownership -------------------------------------------------------

  function onAfterSwap(evt) {
    if (evt.target && evt.target.id === 'review-queue') {
      state.refillInFlight = false;
      indexBatch(evt.target);
      if (state.current === null) showNext();
    }
  }

  function indexBatch(container) {
    if (!container) return;
    var batches = container.querySelectorAll('.review-batch');
    var latest = batches[batches.length - 1];
    if (latest) {
      state.exhausted = latest.dataset.exhausted === 'true';
      state.cursor = latest.dataset.cursor || '';
    }

    var known = {};
    for (var i = 0; i < state.queue.length; i++) known[state.queue[i].cardId] = true;

    var nodes = container.querySelectorAll('article[data-card-id]');
    for (var j = 0; j < nodes.length; j++) {
      var el = nodes[j];
      var cardId = el.dataset.cardId;
      // A card already graded this session, or already queued, must not appear twice -- the
      // server's due-window refill can re-offer a card the client graded moments ago, before
      // the write has landed (architecture.md §6).
      if (known[cardId]) continue;
      known[cardId] = true;
      state.queue.push(cardToSlot(el));
    }
    maybeShowDone();
  }

  function cardToSlot(el) {
    return {
      cardId: el.dataset.cardId,
      el: el,
      branches: {
        1: branch(el, 'again'),
        2: branch(el, 'hard'),
        3: branch(el, 'good'),
        4: branch(el, 'easy'),
      },
      done: false,
      repeat: false,
    };
  }

  function branch(el, name) {
    return {
      due: el.dataset[name + 'Due'],
      state: parseInt(el.dataset[name + 'State'], 10),
      scheduledDays: parseInt(el.dataset[name + 'ScheduledDays'], 10),
    };
  }

  // -- Advance -----------------------------------------------------------

  // Index of the next card to show: the first slot not yet graded this session. -1 when every
  // slot is done.
  function firstPendingIndex() {
    for (var i = 0; i < state.queue.length; i++) {
      if (!state.queue[i].done) return i;
    }
    return -1;
  }

  function showNext() {
    var idx = firstPendingIndex();
    var stage = document.getElementById('review-stage');
    if (idx === -1) {
      state.current = null;
      if (stage) stage.hidden = true;
      maybeShowDone();
      return;
    }

    state.current = idx;
    var card = state.queue[idx];
    state.revealStart = null;
    if (!stage) return;
    stage.hidden = false;
    stage.dataset.revealed = 'false';
    stage.dataset.cardId = card.cardId;
    resetFlagControl(stage, card.cardId);
    var revealBtn = stage.querySelector('[data-reveal]');
    if (revealBtn) revealBtn.textContent = 'Show Answer';
    var ratingButtons = stage.querySelector('.rating-buttons');
    if (ratingButtons) ratingButtons.hidden = true;

    var q = card.el.querySelector('.card-question');
    var a = card.el.querySelector('.card-answer');
    var stageQ = stage.querySelector('.card-question');
    var stageA = stage.querySelector('.card-answer');
    if (stageQ && q) stageQ.innerHTML = q.innerHTML;
    if (stageA && a) { stageA.innerHTML = a.innerHTML; stageA.hidden = true; }

    updateIntervalLabels(card);
    maybeRequestRefill();
  }

  // Collapses the flag control (#207) back to its unsubmitted state and points its hidden cardId
  // input at the card now on screen -- otherwise the previous card's in-progress comment or
  // "Flagged" confirmation would bleed into the next card's view. Fully decoupled from grading:
  // nothing here touches state.queue or the flush/backoff machinery.
  function resetFlagControl(stage, cardId) {
    var cardIdInput = stage.querySelector('[data-flag-card-id]');
    if (cardIdInput) cardIdInput.value = cardId;
    var form = stage.querySelector('[data-flag-form]');
    if (form) form.hidden = true;
    var comment = stage.querySelector('[data-flag-comment]');
    if (comment) comment.value = '';
    var status = stage.querySelector('[data-flag-status]');
    if (status) status.textContent = '';
  }

  // Guards against a stale flag confirmation (#207): the flag POST is fully decoupled from
  // grading, so a student can reveal+rate (advancing showNext() to a new card, which
  // resetFlagControl() re-points at) before the flag response for the *previous* card lands.
  // Without this, htmx would swap "Flagged ✓" into the shared #review-stage's status span
  // against whatever card is now on screen. flagRequestCardId snapshots which card the request
  // was actually for; the swap is skipped if the stage has since moved on.
  var flagRequestCardId = null;

  function isFlagStatusTarget(evt) {
    return !!(evt.detail && evt.detail.target && evt.detail.target.matches &&
      evt.detail.target.matches('[data-flag-status]'));
  }

  function onFlagBeforeRequest(evt) {
    if (!isFlagStatusTarget(evt)) return;
    var stage = document.getElementById('review-stage');
    flagRequestCardId = stage ? stage.dataset.cardId : null;
  }

  function onFlagBeforeSwap(evt) {
    if (!isFlagStatusTarget(evt)) return;
    var stage = document.getElementById('review-stage');
    if (!stage || stage.dataset.cardId !== flagRequestCardId) {
      evt.detail.shouldSwap = false;
      return;
    }
    // Submitting collapses the form again, same as toggling it closed -- the confirmation text
    // swapped into [data-flag-status] is the only thing left visible.
    var form = stage.querySelector('[data-flag-form]');
    if (form) form.hidden = true;
  }

  function updateIntervalLabels(card) {
    var now = Date.now();
    for (var r = 1; r <= 4; r++) {
      var el = document.querySelector('[data-interval-for="' + r + '"]');
      if (el) el.textContent = formatInterval(card.branches[r], now);
    }
  }

  // scheduledDays is 0 for every Learning/Relearning-step branch (their steps are minute-scale --
  // see the go-fsrs default {1,10}-minute learning steps noted below), which used to collapse
  // Again/Hard/Good/Easy to the same "<1d" label right when a lapsed card's branches differ most.
  // Falls back to due (already on every branch) for minute/hour granularity in that case.
  function formatInterval(branch, now) {
    var days = branch.scheduledDays;
    if (days > 0) {
      if (days === 1) return '1d';
      if (days < 30) return days + 'd';
      if (days < 365) return Math.round(days / 30) + 'mo';
      return (days / 365).toFixed(1) + 'y';
    }
    var dueMs = Date.parse(branch.due);
    var minutes = isNaN(dueMs) ? 0 : Math.round((dueMs - now) / 60000);
    if (minutes < 1) return '<1m';
    if (minutes < 60) return minutes + 'm';
    return Math.round(minutes / 60) + 'h';
  }

  function reveal() {
    var stage = document.getElementById('review-stage');
    if (!stage || stage.dataset.revealed === 'true') return;
    var a = stage.querySelector('.card-answer');
    if (a) a.hidden = false;
    stage.dataset.revealed = 'true';
    var revealBtn = stage.querySelector('[data-reveal]');
    if (revealBtn) revealBtn.textContent = 'Hide Answer';
    var ratingButtons = stage.querySelector('.rating-buttons');
    if (ratingButtons) ratingButtons.hidden = false;
    if (state.revealStart === null) state.revealStart = Date.now();
  }

  // Re-hiding is cosmetic only: revealStart (and thus the graded durationMs) is left untouched,
  // since it measures time-to-first-reveal, not visibility.
  function hide() {
    var stage = document.getElementById('review-stage');
    if (!stage || stage.dataset.revealed !== 'true') return;
    var a = stage.querySelector('.card-answer');
    if (a) a.hidden = true;
    stage.dataset.revealed = 'false';
    var revealBtn = stage.querySelector('[data-reveal]');
    if (revealBtn) revealBtn.textContent = 'Show Answer';
    var ratingButtons = stage.querySelector('.rating-buttons');
    if (ratingButtons) ratingButtons.hidden = true;
  }

  function onStageClick(evt) {
    var ratingBtn = evt.target.closest('button[data-rating]');
    if (ratingBtn) {
      grade(parseInt(ratingBtn.dataset.rating, 10));
      return;
    }
    if (evt.target.closest('[data-reveal]')) {
      var stage = document.getElementById('review-stage');
      if (stage && stage.dataset.revealed === 'true') hide();
      else reveal();
      return;
    }
    if (evt.target.closest('[data-flag-toggle]')) {
      var form = evt.currentTarget.querySelector('[data-flag-form]');
      if (form) form.hidden = !form.hidden;
      return;
    }
    if (evt.target.closest('[data-flag-cancel]')) {
      var cancelForm = evt.currentTarget.querySelector('[data-flag-form]');
      if (cancelForm) cancelForm.hidden = true;
    }
  }

  function onKeydown(evt) {
    var tag = evt.target && evt.target.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA') return;
    if (state.current === null) return;
    var stage = document.getElementById('review-stage');
    if (!stage) return;

    if (evt.code === 'Space') {
      evt.preventDefault();
      if (stage.dataset.revealed === 'true') hide();
      else reveal();
      return;
    }
    var digit = { Digit1: 1, Digit2: 2, Digit3: 3, Digit4: 4 }[evt.code];
    if (digit && stage.dataset.revealed === 'true') {
      evt.preventDefault();
      grade(digit);
    }
  }

  // -- Grading, synchronously [§2.6] --------------------------------------

  function grade(rating) {
    var stage = document.getElementById('review-stage');
    if (state.current === null || !stage || stage.dataset.revealed !== 'true') return;
    var card = state.queue[state.current];
    var branch = card.branches[rating];
    if (!branch) return;

    var durationMs = state.revealStart ? Math.max(1, Date.now() - state.revealStart) : null;
    card.done = true;

    var ev = {
      id: uuidv7(),
      cardId: card.cardId,
      rating: rating,
      reviewedAt: new Date().toISOString(),
      durationMs: durationMs,
    };
    state.pending.push(ev);

    maybeRequeue(card, branch, rating);

    document.body.dispatchEvent(new CustomEvent('card-graded', { detail: ev }));
    scheduleFlush();
    showNext();
  }

  // Cosmetic, never written anywhere (architecture.md §6): a card requeues in-session iff its
  // graded branch lands it back in Learning/Relearning within 20 minutes AND the rating that
  // produced it was Again or Hard. Good never requeues in-session (#136) -- go-fsrs's default
  // {1,10}-minute learning steps mean a New card's first Good otherwise satisfies the
  // Learning-state/20-minute test too, forcing users toward Easy (an oversized ~8-day interval)
  // just to avoid an immediate repeat. Easy already never requeues (its branch state is always
  // Review), so this only changes Good's behaviour. This is a deliberate divergence from Anki's
  // own same-session learning queue for Good specifically -- docs/architecture.md §20.
  function shouldRequeue(rating, branchState, dueMs, nowMs) {
    if (branchState !== 1 && branchState !== 3) return false;
    if (rating === RATING_GOOD) return false;
    if (isNaN(dueMs) || dueMs - nowMs > REQUEUE_WINDOW_MS) return false;
    return true;
  }

  function maybeRequeue(card, branch, rating) {
    var dueMs = Date.parse(branch.due);
    if (!shouldRequeue(rating, branch.state, dueMs, Date.now())) return;

    var cardsDueBefore = 0;
    for (var i = 0; i < state.queue.length; i++) {
      var c = state.queue[i];
      if (c.done) continue;
      var d = Date.parse(c.branches[3].due); // Good branch, representative of queue order
      if (!isNaN(d) && d <= dueMs) cardsDueBefore++;
    }
    var ahead = Math.max(3, cardsDueBefore);

    var insertAt = state.queue.length;
    var remaining = ahead;
    for (var j = state.current + 1; j < state.queue.length; j++) {
      if (!state.queue[j].done) {
        remaining--;
        if (remaining <= 0) { insertAt = j; break; }
      }
    }
    // card.branches is the pre-grade preview -- the best value available at this instant.
    // onBatchSettled replaces it with the server's post-grade preview when this grade's POST
    // comes back, which is normally several cards before this slot is shown (#142).
    state.queue.splice(insertAt, 0, {
      cardId: card.cardId, el: card.el, branches: card.branches, done: false, repeat: true,
    });
  }

  // -- Unseen tracking + refill --------------------------------------------

  function maybeRequestRefill() {
    if (state.exhausted || state.refillInFlight) return;
    var unseen = 0;
    for (var i = 0; i < state.queue.length; i++) {
      var c = state.queue[i];
      if (!c.done && !c.repeat) unseen++;
    }
    if (unseen < REFILL_THRESHOLD) {
      state.refillInFlight = true;
      document.body.dispatchEvent(new CustomEvent('refill-needed'));
    }
  }

  function maybeShowDone() {
    if (firstPendingIndex() === -1 && state.exhausted) {
      var done = document.getElementById('review-done');
      if (done) done.hidden = false;
    }
  }

  // "Keep studying" (#172): re-grants one more full preset round for the rest of this page
  // session. The cursor resets to '' -- the cards the cap excluded sort before the position the
  // limited fetch's cursor holds, so carrying that cursor forward would skip exactly the cards
  // this button exists to reach. Restarting is safe: indexBatch's known[cardId] map already drops
  // any card already queued or graded this session, and the server still excludes anything with
  // last_review >= study_day_start.
  function onStudyMoreClick() {
    state.extraRounds++;
    state.exhausted = false;
    state.cursor = '';
    var done = document.getElementById('review-done');
    if (done) done.hidden = true;
    state.refillInFlight = true;
    document.body.dispatchEvent(new CustomEvent('refill-needed'));
  }

  // -- Sending, batched with backoff retry --------------------------------

  function scheduleFlush() {
    if (state.flushTimer) return;
    var sinceLast = Date.now() - state.lastFlushAt;
    var delay = sinceLast >= FLUSH_DEBOUNCE_MS ? 0 : FLUSH_DEBOUNCE_MS - sinceLast;
    state.flushTimer = setTimeout(function () {
      state.flushTimer = null;
      flush();
    }, delay);
  }

  // The acting account is a property of the browser-wide session cookie, not of this tab, so a
  // switch in another tab would otherwise re-attribute these grades to whoever the session now
  // names (#178). ?u= is what the server compares against the session user before writing anything;
  // it is a query parameter and not a header because the pagehide path below uses sendBeacon, which
  // cannot set headers, and not a body field because the batch body is fixed by CLAUDE.md §10.1.
  function batchURL() {
    return state.userId
      ? '/api/reviews/batch?u=' + encodeURIComponent(state.userId)
      : '/api/reviews/batch';
  }

  // Sent with a direct fetch(), not through htmx: htmx's json-enc extension re-evaluates
  // hx-vals a second time to recover typed values (its own base parameter pass flattens an
  // array of objects to FormData "[object Object]" entries first), and when that array has 2+
  // events, its same-key merge logic pushes the array into itself, throwing inside
  // encodeParameters -- htmx catches that silently and falls back to its default
  // (non-JSON) encoder, so the batch is sent malformed. See docs/plans/57-csp-reviewer.md
  // §Open question 1 (predicted this) and docs/plans/99-grading-persistence.md (confirmed it).
  function flush() {
    if (state.pending.length === 0 || state.inFlight) return;
    state.lastFlushAt = Date.now();
    var events = takePending();

    fetch(batchURL(), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ events: events }),
    }).then(function (res) {
      return res.text().then(function (text) {
        onBatchSettled(events, res.ok, res.status, text);
      });
    }, function () {
      onBatchSettled(events, false, 0, null); // fetch itself rejected: network error
    });
  }

  // pagehide's fetch is not reliable: browsers routinely abort an in-flight or newly-started
  // async request once the page starts unloading, which would silently drop the session's last
  // batch of grades (architecture.md §6 accepts losing unsent events to a hard crash, but not to
  // an ordinary tab close/navigation that this can catch). navigator.sendBeacon is fire-and-forget
  // and guaranteed by the browser to complete after the page is gone; there is no response to
  // react to, so it goes straight to takePending() instead of through flush().
  function flushOnUnload() {
    if (state.pending.length === 0 || !navigator.sendBeacon) {
      flush();
      return;
    }
    var events = takePending();
    if (events.length === 0) return;
    var blob = new Blob([JSON.stringify({ events: events })], { type: 'application/json' });
    if (!navigator.sendBeacon(batchURL(), blob)) {
      state.pending = events.concat(state.pending); // queuing failed; nothing more we can do here
    }
  }

  // Drains and returns state.pending, stashing the result in state.inFlight so a failure can put
  // it back. flush() will not call this while a request is outstanding; flushOnUnload deliberately
  // can -- a pagehide beacon has to go out even mid-flight -- and its overwrite of state.inFlight
  // is harmless, because onBatchSettled puts back the array its own closure captured, not
  // state.inFlight.
  function takePending() {
    if (state.pending.length === 0) return [];
    state.inFlight = state.pending;
    state.pending = [];
    return state.inFlight;
  }

  function onBatchSettled(sent, successful, status, text) {
    state.inFlight = null;

    if (successful) {
      state.backoffIndex = 0;
      clearDeliveryError();
      var results = [];
      try { results = JSON.parse(text).results || []; } catch (e) { /* malformed body: nothing to reconcile */ }
      for (var i = 0; i < results.length; i++) {
        var r = results[i];
        applyFreshPreview(r.cardId, r.preview);
        if (r.status === 'rejected' || r.status === 'forbidden') {
          console.error('deckshare: event ' + r.id + ' ' + r.status + ', dropped permanently');
          showDeliveryError('A grade could not be saved (' + r.status + '). Check your device clock or deck access.');
        }
      }
      if (state.pending.length > 0) scheduleFlush();
      return;
    }

    if (status === 409) {
      // The session now belongs to another account (#178). The events cannot be retried -- retrying
      // would either be refused again or, worse, land under the wrong user if the tab reloaded.
      console.error('deckshare: batch refused (409, session changed), dropping ' + sent.length + ' event(s)');
      showDeliveryError('This tab is signed in as a different account now, so these grades were not saved. Reload the page to keep studying.');
      return;
    }

    if (status >= 400 && status < 500 && status !== 401) {
      // A malformed batch (400) can never succeed by retrying it unchanged -- drop.
      // 401 is deliberately excluded and handled just below.
      console.error('deckshare: batch rejected (' + status + '), dropping ' + sent.length + ' event(s)');
      showDeliveryError('Some grades failed to save (status ' + status + '). Reload the page and try again.');
      return;
    }

    if (status === 401) {
      // The session ended under us -- most likely a password change in another tab, which
      // invalidates every session for the account. The events are still perfectly valid, so
      // they are held and retried rather than dropped: losing them would lose review_log
      // training data that cannot be reconstructed (CLAUDE.md §2.5).
      console.error('deckshare: batch unauthorised (401), holding ' + sent.length + ' event(s)');
      showDeliveryError('Your session ended. Sign in again in another tab to save your grades -- don\'t close this page.');
    }

    // 401, network error, or >=500: put the events back and retry with backoff.
    state.pending = sent.concat(state.pending);
    var delaySec = BACKOFF_SECONDS[Math.min(state.backoffIndex, BACKOFF_SECONDS.length - 1)];
    state.backoffIndex++;
    setTimeout(flush, delaySec * 1000);
  }

  // The branches on a hidden card node were precomputed for its state *before* this session's
  // first grade (§2.6), so a card the learning-steps heuristic requeued would show its pre-grade
  // intervals on its second appearance (#142). The grade response carries the four branches the
  // server recomputed from the state it actually stored; swap them into every slot still holding
  // that card, and repaint the labels if it is the one on screen. Cosmetic, exactly like the
  // branches it replaces -- nothing here is ever sent back.
  function applyFreshPreview(cardId, preview) {
    var fresh = previewBranches(preview);
    if (!fresh) return;
    for (var i = 0; i < state.queue.length; i++) {
      var slot = state.queue[i];
      if (slot.cardId !== cardId || slot.done) continue;
      slot.branches = fresh;
      if (i === state.current) updateIntervalLabels(slot);
    }
  }

  // All four branches or none: a partial swap would leave grade() with a missing branch, which it
  // answers by silently ignoring the keypress.
  function previewBranches(p) {
    if (!p || !p.again || !p.hard || !p.good || !p.easy) return null;
    return { 1: wireBranch(p.again), 2: wireBranch(p.hard), 3: wireBranch(p.good), 4: wireBranch(p.easy) };
  }

  // The JSON counterpart of branch(): same three fields, already typed, no parseInt.
  function wireBranch(b) {
    return { due: b.due, state: b.state, scheduledDays: b.scheduledDays };
  }

  function showDeliveryError(msg) {
    var el = document.getElementById('review-error');
    if (el) { el.textContent = msg; el.hidden = false; }
  }

  function clearDeliveryError() {
    var el = document.getElementById('review-error');
    if (el) el.hidden = true;
  }

  // -- uuidv7 --------------------------------------------------------------

  // crypto.randomUUID() is v4 and is not a substitute (schema.md specifies UUIDv7 ids).
  // Monotonicity within a millisecond is not required.
  function uuidv7() {
    var bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    var ts = Date.now();
    for (var i = 5; i >= 0; i--) {
      bytes[i] = ts % 256;
      ts = Math.floor(ts / 256);
    }
    bytes[6] = (bytes[6] & 0x0f) | 0x70; // version 7
    bytes[8] = (bytes[8] & 0x3f) | 0x80; // variant 10
    var hex = '';
    for (var j = 0; j < 16; j++) hex += bytes[j].toString(16).padStart(2, '0');
    return hex.slice(0, 8) + '-' + hex.slice(8, 12) + '-' + hex.slice(12, 16) + '-' + hex.slice(16, 20) + '-' + hex.slice(20);
  }

  window.deckshareReview = {
    deckId: function () { return state.deckId; },
    cursor: function () { return state.cursor; },
    extraRounds: function () { return String(state.extraRounds); },
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
