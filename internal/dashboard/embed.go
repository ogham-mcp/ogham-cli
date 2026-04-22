// Package dashboard implements the native Go HTTP dashboard served by
// `ogham dashboard`. All static assets (compiled Tailwind CSS, vendored
// HTMX, any JS sprinkles) are embedded at build-time so the shipping
// binary has no runtime filesystem dependencies.
package dashboard

import "embed"

// staticFS holds the compiled/vendored front-end assets: styles.css
// (Tailwind output), htmx.min.js (vendored HTMX 2.0.x), and app.js
// (prototype-specific JS; may be empty).
//
//go:embed static
var staticFS embed.FS
