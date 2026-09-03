(function () {
    var MAX_DIMENSION = 512;
    var JPEG_QUALITY = 0.85;

    var form = document.getElementById("avatar-form");
    var input = document.getElementById("avatar");
    if (!form || !input) return;

    form.addEventListener("submit", function (event) {
        var file = input.files[0];
        if (!file) return;

        event.preventDefault();
        // Wrapped in Promise.resolve().then(...) so a synchronous throw inside resize() (e.g.
        // createImageBitmap not being available) still reaches the .catch fallback below, instead
        // of escaping as an uncaught exception before any promise exists to catch it.
        Promise.resolve()
            .then(function () { return resize(file); })
            .then(function (blob) {
                var dt = new DataTransfer();
                dt.items.add(new File([blob], "avatar.jpg", { type: "image/jpeg" }));
                input.files = dt.files;
                form.submit();
            })
            .catch(function () {
                // Resizing failed (e.g. not a decodable image) -- submit the original file and
                // let the server's own validation produce the error message.
                form.submit();
            });
    });

    function resize(file) {
        // imageOrientation: "from-image" applies the file's EXIF orientation tag when decoding --
        // without it, a portrait phone photo is drawn using raw (unrotated) pixel order, and the
        // re-encoded JPEG below carries no EXIF of its own to correct it afterwards.
        return createImageBitmap(file, { imageOrientation: "from-image" }).then(function (bitmap) {
            var scale = Math.min(1, MAX_DIMENSION / Math.max(bitmap.width, bitmap.height));
            var width = Math.round(bitmap.width * scale);
            var height = Math.round(bitmap.height * scale);

            var canvas = document.createElement("canvas");
            canvas.width = width;
            canvas.height = height;
            var ctx = canvas.getContext("2d");
            ctx.drawImage(bitmap, 0, 0, width, height);

            return new Promise(function (resolve, reject) {
                canvas.toBlob(function (blob) {
                    if (blob) resolve(blob); else reject(new Error("toBlob failed"));
                }, "image/jpeg", JPEG_QUALITY);
            });
        });
    }
})();
