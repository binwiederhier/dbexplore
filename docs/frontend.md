# Frontend

The UI is `web/index.html` (markup and CSS), `web/app.js` (all logic) and `web/login.html`, embedded into the binary with `go:embed`. Plain JavaScript, no framework, no build step.

## Layout

Tree (source > schema > tables with estimated row counts) | tabs (table tabs and query consoles) | status bar with the breadcrumb of the active tab. The sidebar is resizable.

## Table tabs

WHERE / ORDER BY bars (Enter applies), click a header to cycle asc/desc/none, a pager with page size, Σ for an exact count, DDL, "to console" (opens the tab's query in a console) and copy as TSV. Identifiers that the UI writes into SQL (header sorting, "to console", FK filters) go through `qid`, which quotes anything that is not a plain lowercase name or is a reserved word, so a table called `user` becomes `"user"` rather than `current_user`. Ctrl+Enter anywhere reruns the active tab.

Row selection: click a row (or its number) to select it; Shift+click selects a range, Ctrl/Cmd+click toggles one row, dragging across rows selects a range, and Ctrl+A selects the whole page. Ctrl+C (or ⧉ TSV) copies the selected rows as TSV with a header line; with nothing selected, ⧉ TSV copies the whole page. Tabs and newlines inside values become spaces. The grid is `user-select: none`, so the copy shortcut isn't fighting browser text selection.

Double-click a row to open the row view: every field as name/type/value, JSON pretty-printed, with a field filter, ▲/▼ (or the arrow keys) to step through rows, FK values that are clickable there too, and Copy giving `name<TAB>value` lines.

## Foreign keys

FK columns get a ↗ marker in the header, and their values render as links. Clicking one opens the referenced table in a new tab (next to the current one) with `WHERE ref_col = <value>` prefilled; for multi-column FKs every column is ANDed. Filtered tabs show ⧩ in the title. Clicking a table in the tree reuses its unfiltered tab, so FK tabs are never hijacked.

Hovering an FK link for 350 ms (in the grid, the cards view or the row view) shows a peek card with the referenced row. It is loaded lazily with the same `/api/table` call and WHERE clause the click would use (page size 1), and cached per source/table/WHERE until the tree is refreshed. Sweeping the pointer across links loads nothing, since the timer resets; a response that arrives after the pointer moved on is dropped. The card is `pointer-events: none` and disappears on mouse-out, scroll, click or Escape.

## Result views

Every tab has a ▦ Table / ▤ Cards switch in its toolbar, stored per tab as `t.view`. Cards view is a CSS grid (`repeat(auto-fill, minmax(290px, 1fr))`). Each card is headed by the row number and its primary-key value(s), falling back to the first column, and holds a name/value table of every field. Long values are ellipsised, with the full value in the tooltip. Cards use the same `data-ri` / `data-r` / `data-c` attributes as grid rows, so selection, drag-select, Ctrl+C, double-click for the row view and FK links work the same in both views.

## Query consoles

A textarea (Ctrl+Enter runs), the source picker and, when the server reports `ai: true` for the sources, the Ask AI bar. Generate sends the prompt with the current SQL and puts the returned statement in the editor, then runs it (on read-write sources it only fills the editor).

On a read-write source, `runTab` asks for confirmation (`riskyWrite`) before `DROP`, `TRUNCATE`, and `UPDATE`/`DELETE` without `WHERE`. A result with a `command` is a committed write: it shows the command tag, and changing the page size does not rerun it. After DDL (`afterWrite`) the tree and the cached columns and FKs of that source's tabs are reloaded; any write clears the peek cache.

## Row editor

On read-write sources (`isRW`), tables get a + Row button, and the row view of a table with a primary key gets Edit and Delete (`modalButtons` picks the header buttons for the modal's mode). `editRow(t, ri)` turns the modal into one textarea per column plus a NULL toggle, `ri == null` for a new row. `fieldValue` decides what is sent: changed fields for an update, filled-in or NULL fields for an insert (the rest get their default). Saving an update replaces the row in place from the returned row; an insert reloads the tab; a delete removes the row from the page. Database errors show above the fields. Ctrl+Enter saves; a click on the backdrop does not close the editor.

## Values

Cells are styled by JSON type (`cellClass`): `null` grey and italic, numbers, booleans and objects (JSON columns) each in their own color. Large integers and numerics that the server sends as strings render like text.

## Persistence

Open tabs, filters, console SQL, expanded tree nodes and the sidebar width are kept in `localStorage`. All reads and writes are wrapped so the UI works without it.
