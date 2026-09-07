# Build and contribute to this book

Use mdBook **0.4.52** for this first implementation. Install the release for your operating system from the [official release assets](https://github.com/rust-lang/mdBook/releases/tag/v0.4.52), or install that exact version with Cargo:

```sh
cargo install mdbook --version 0.4.52 --locked
mdbook --version
npm ci --prefix book --ignore-scripts
npm run --prefix book assets
mdbook build
python3 book/check-links.py
```

Run these commands at the Pulse repository root with Node.js 22+ and npm available. The lockfile pins Mermaid and verifies npm package integrity; the asset step bundles its renderer and license locally, with no browser CDN dependency. Output is written to `book/build/` and is ignored by Git. To read a local preview:

```sh
mdbook serve --hostname 127.0.0.1 --port 3001
```

The eventual production application lives in `homelab/apps/pulse_site/` and serves the book at `/book/`. This local build does not publish the site. Mermaid is bundled locally; browser rendering, theme switching, nested production paths, and hosting validation remain tracked in `plan.md`.

Add chapters to `book/src/SUMMARY.md` only when their content is useful. Keep original operations and development documents canonical while their book pages include them. Their linked CRD and journey references are included too; run the link checker after adding references. Do not duplicate them into independently edited copies.

Before handing work off, update `plan.md` with the current branch and PR, completed changes, commands actually run, unresolved failures, and the next concrete task. Distinguish written instructions from instructions executed on a clean environment.
