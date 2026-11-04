# Interface preferences

The user explicitly requires polished, attractive Home Assistant interfaces for every UI change. Treat visual quality as part of completion.

- Prefer compact cards, clear grouping, filled sliders, color swatches and direct controls over long generic forms. Refer to Home Assistant design guidance and community examples such as Mushroom and Bubble Card when choosing patterns.
- Preserve the active HA theme. Check both dark and light themes, desktop and phone widths, and inspect rendered screenshots before shipping. Allow theme transitions to settle before judging a screenshot.
- Keep close and save controls reachable during scrolling. Preserve drafts, keyboard access, Escape behavior and existing functionality.
- Label on-screen previews clearly. A preview must not change a physical device or persist settings unless the user explicitly invokes that action.

References: https://contributors.home-assistant.io/design/components/controls/ ; https://github.com/piitaya/lovelace-mushroom ; https://github.com/Clooos/Bubble-Card

# Release flow

Home Assistant sees an update only when `version` in `alice/config.yaml` changes on `main`.

- Every shipped change raises that version and adds an entry to `CHANGELOG.md` and `alice/CHANGELOG.md`.
- Refresh SHA-256 values in `alice/source-manifest.json` for changed files under `alice/`; `python tools/check_release.py` must pass.
- Push a branch first. Merge to `main` only after the GitHub `Check` workflow is green: an update on `main` installs itself on the live HA.
- App updates never update bridges on speakers. Those stay manual, with a voice check.
