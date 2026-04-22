# Dashboard static asset build

This directory holds the build-only Tailwind pipeline for
`internal/dashboard/static/styles.css`. The Go binary does NOT depend
on anything here at runtime -- the compiled CSS is checked in as an
artefact (like a generated protobuf) and embedded via `//go:embed`.

## Why committed artefact?

`go install github.com/ogham-mcp/ogham-cli` must work without Node or
Bun on the user's path. Shipping `styles.css` in the repo is the
simplest way to guarantee that.

## Rebuild commands

```bash
# From this directory (internal/dashboard/_build):
bun install            # first time only
bun run build          # one-shot rebuild
bun run watch          # dev loop (leaves bun watching, plays with `templ generate --watch`)
```

Output goes to `../static/styles.css`. Commit the result when you touch
Tailwind classes in any `.templ` file.

## Fallback without Bun

```bash
npx @tailwindcss/cli -i input.css -o ../static/styles.css --minify
```

If neither Bun nor npm is available the prototype still works against
the hand-rolled subset CSS shipped in `../static/styles.css` as of the
v0.1 prototype -- you just won't pick up any new utility classes you
add to the templates.
