# Frontend

The UI is `web/index.html` (markup and CSS), `web/app.js` (all logic) and `web/login.html`, embedded into the binary with `go:embed`. Plain JavaScript, no framework, no build step.

## Layout

Tree (source > schema > tables with estimated row counts) | tabs (table tabs and query consoles) | status bar with the breadcrumb of the active tab. The sidebar is resizable.

## Table tabs

WHERE / ORDER BY bars (Enter applies), click a header to cycle asc/desc/none, a pager with page size, Σ for an exact count, DDL, "to console" (opens the tab's query in a console) and copy as TSV.

Row selection: click a row (or its number) to select it; Shift+click selects a range, Ctrl/Cmd+click toggles one row, dragging across rows selects a range, and Ctrl+A selects the whole page. Ctrl+C (or ⧉ TSV) copies the selected rows as TSV with a header line; with nothing selected, ⧉ TSV copies the whole page. Tabs and newlines inside values become spaces. The grid is `user-select: none`, so the copy shortcut isn't fighting browser text selection.

Double-click a row to open the row view: every field as name/type/value, JSON pretty-printed, with a field filter, ▲/▼ (or the arrow keys) to step through rows, FK values that are clickable there too, and Copy giving `name<TAB>value` lines.

## Foreign keys

FK columns get a ↗ marker in the header, and their values render as links. Clicking one opens the referenced table in a new tab (next to the current one) with `WHERE ref_col = <value>` prefilled; for multi-column FKs every column is ANDed. Filtered tabs show ⧩ in the title. Clicking a table in the tree reuses its unfiltered tab, so FK tabs are never hijacked.

## Result views

Every tab has a ▦ Table / ▤ Cards switch in its toolbar, stored per tab as `t.view`. Cards view is a CSS grid (`repeat(auto-fill, minmax(290px, 1fr))`). Each card is headed by the row number and its primary-key value(s), falling back to the first column, and holds a name/value table of every field. Long values are ellipsised, with the full value in the tooltip. Cards use the same `data-ri` / `data-r` / `data-c` attributes as grid rows, so selection, drag-select, Ctrl+C, double-click for the row view and FK links work the same in both views.

## Query consoles

A textarea (Ctrl+Enter runs), the source picker and, when the server reports `ai: true` for the sources, the Ask AI bar. Generate sends the prompt with the current SQL and puts the returned statement in the editor, then runs it.

## Values

Cells are styled by JSON type (`cellClass`): `null` grey and italic, numbers, booleans and objects (JSON columns) each in their own color. Large integers and numerics that the server sends as strings render like text.

## Persistence

Open tabs, filters, console SQL, expanded tree nodes and the sidebar width are kept in `localStorage`. All reads and writes are wrapped so the UI works without it.
