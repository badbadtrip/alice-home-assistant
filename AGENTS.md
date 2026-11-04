# Interface preferences

The user explicitly requires polished, attractive Home Assistant interfaces for every UI change. Treat visual quality as part of completion.

- Prefer compact cards, clear grouping, filled sliders, color swatches and direct controls over long generic forms. Refer to Home Assistant design guidance and community examples such as Mushroom and Bubble Card when choosing patterns.
- Preserve the active HA theme. Check both dark and light themes, desktop and phone widths, and inspect rendered screenshots before shipping. Allow theme transitions to settle before judging a screenshot.
- Keep close and save controls reachable during scrolling. Preserve drafts, keyboard access, Escape behavior and existing functionality.
- Label on-screen previews clearly. A preview must not change a physical device or persist settings unless the user explicitly invokes that action.

References: https://contributors.home-assistant.io/design/components/controls/ ; https://github.com/piitaya/lovelace-mushroom ; https://github.com/Clooos/Bubble-Card
