# Interface preferences

The user explicitly requires polished, attractive Home Assistant interfaces for every UI change. Treat visual quality as part of completion.

- Prefer compact cards, clear grouping, filled sliders, color swatches and direct controls over long generic forms. Refer to Home Assistant design guidance and community examples such as Mushroom and Bubble Card when choosing patterns.
- Preserve the active HA theme. Check both dark and light themes, desktop and phone widths, and inspect rendered screenshots before shipping. Allow theme transitions to settle before judging a screenshot.
- Keep close and save controls reachable during scrolling. Preserve drafts, keyboard access, Escape behavior and existing functionality.
- Label on-screen previews clearly. A preview must not change a physical device or persist settings unless the user explicitly invokes that action.

References: https://contributors.home-assistant.io/design/components/controls/ ; https://github.com/piitaya/lovelace-mushroom ; https://github.com/Clooos/Bubble-Card

# Release flow

Home Assistant sees an update only when `version` in `alice/config.yaml` changes on `main`.

- Every shipped change raises that version and adds an entry to `alice/CHANGELOG.md`. The changelog and the wiki live only in `alice/` (`alice/CHANGELOG.md`, `alice/wiki/`); there are no root copies.
- `alice/` is the only source of app, server and bridge code; nothing is imported from sibling projects.
- Run `python tools/sync_release.py` to copy the notices, build `alice/DOCS.md` and refresh `alice/source-manifest.json`; `python tools/check_release.py` must pass.
- `tools/check.sh` runs every check CI runs (Python, server, bridge, manifest). Two bridge tests check file permissions and fail only on Windows.
- The improvement plan is in `docs/plan/` (one file per part, status table at the end of each).
- Push a `claude/` branch first. The `Promote` workflow fast-forwards `main` by itself once `Check` and `Image` are green for that commit, so push only what is ready to ship: an update on `main` installs itself on the live HA, and HA downloads the image `Image` published for that version instead of building it.
- Bridges update themselves after an app update unless switched off in the panel: right after the app update, one speaker at a time, even during playback, with automatic rollback. A failure stops that version everywhere. Health cannot prove the microphone, so an update finishes only when the next voice request reaches the bridge.
