# Console design

[Editable desktop concept](console-concept.svg). The SVG can be imported into Figma.
This is a design study with sample data, not a capture of a running console.

The implementation lives in `web/dist/index.html`, `style.css`, and `console-ui.js`.

- Graphite surfaces and sage accents give the workspace a quiet hierarchy. Light
  mode uses the same components and spacing, with a persisted theme switch.
- The sidebar prioritizes finding environments. Sandbox creation lives in a
  dialog, with optional resource limits behind a disclosure.
- Terminal, Files, and Run stay prominent. Backup policies, snapshots, and
  lifecycle actions live under Manage environment.
- The workspace header follows the active session. Keyboard navigation works
  across tabs; N opens creation and / searches when focus is outside an editor,
  terminal, or dialog.
- At narrow widths the resource panel moves above the workspace; file editing
  and run previews stack vertically. Reduced-motion preferences are respected.

Run `npm --prefix web test` for the console's interaction tests. Assets are
embedded in the daemon, so activation requires rebuilding and restarting it.
