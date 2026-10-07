// Live preview (#267): previews the picked theme and accent on <html> before the form is
// submitted, using the same attributes the server renders. Nothing is saved until Update.
(function () {
    var form = document.getElementById("appearance-form");
    if (!form) return;

    // value that means "no attribute" -- the server omits it too (layout.html)
    var DEFAULTS = { accent: "azure", color_scheme: "auto" };
    var ATTRS = { accent: "data-accent", color_scheme: "data-theme" };

    form.addEventListener("change", function (event) {
        var name = event.target.name;
        if (!ATTRS[name]) return;
        var root = document.documentElement;
        if (event.target.value === DEFAULTS[name]) {
            root.removeAttribute(ATTRS[name]);
        } else {
            root.setAttribute(ATTRS[name], event.target.value);
        }
    });
})();
