// Copies the generated prompt (import_ai.html's step-2 screen) to the clipboard.
(function () {
  'use strict';

  var button = document.getElementById('copy-prompt');
  var prompt = document.getElementById('prompt');
  if (!button || !prompt) {
    return;
  }

  var defaultLabel = button.textContent;
  var resetTimer = null;

  button.addEventListener('click', function () {
    navigator.clipboard.writeText(prompt.value).then(
      function () {
        setLabel('Copied!');
      },
      function () {
        setLabel('Copy failed -- select the text and copy manually');
      }
    );
  });

  function setLabel(text) {
    button.textContent = text;
    clearTimeout(resetTimer);
    resetTimer = setTimeout(function () {
      button.textContent = defaultLabel;
    }, 2000);
  }
})();
