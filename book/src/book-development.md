# Build and contribute to this book

Use mdBook **0.4.52** for this first implementation. Install the release for your operating system from the [official release assets](https://github.com/rust-lang/mdBook/releases/tag/v0.4.52), or install that exact version with Cargo:

```sh
cargo install mdbook --version 0.4.52 --locked
mdbook --version
mdbook build
```

Run these commands at the Pulse repository root. Output is written to `book/build/` and is ignored by Git. To read a local preview:

```sh
mdbook serve --hostname 127.0.0.1 --port 3001
```

The eventual production application lives in `homelab/apps/pulse_site/` and serves the book at `/book/`. This local build does not publish the site. Nested production path, Mermaid rendering, and hosting validation remain tracked in `plan.md`.

Add chapters to `book/src/SUMMARY.md` only when their content is useful. Keep original operations and development documents canonical while their book pages include them; relative links in those documents need migration validation before publication. Do not duplicate them into independently edited copies.

Before handing work off, update `plan.md` with the current branch and PR, completed changes, commands actually run, unresolved failures, and the next concrete task. Distinguish written instructions from instructions executed on a clean environment.
