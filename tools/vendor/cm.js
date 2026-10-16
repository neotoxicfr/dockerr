// CodeMirror bundle for the compose editor: YAML/.env editing, lint markers
// fed by the server's validation, and a merge view for diffs.
import { EditorView, basicSetup } from "codemirror";
import { EditorState, Compartment } from "@codemirror/state";
import { keymap } from "@codemirror/view";
import { indentWithTab } from "@codemirror/commands";
import { yaml } from "@codemirror/lang-yaml";
import { lintGutter, setDiagnostics } from "@codemirror/lint";
import { unifiedMergeView } from "@codemirror/merge";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags as t } from "@lezer/highlight";

// Colours come from CSS variables so light and dark themes both read well.
const highlight = HighlightStyle.define([
  { tag: [t.propertyName, t.definition(t.propertyName)], color: "var(--syn-key)" },
  { tag: [t.string, t.special(t.string)], color: "var(--syn-str)" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "var(--syn-num)" },
  { tag: [t.comment, t.lineComment], color: "var(--fg-3)", fontStyle: "italic" },
  { tag: [t.meta, t.labelName, t.typeName], color: "var(--syn-meta)" },
  { tag: [t.punctuation, t.separator, t.squareBracket, t.brace], color: "var(--fg-2)" },
]);

const theme = EditorView.theme({
  "&": { color: "var(--fg)", backgroundColor: "var(--bg-1)", fontSize: "13px", height: "100%" },
  ".cm-content": { fontFamily: "var(--mono)", caretColor: "var(--accent)" },
  ".cm-gutters": { backgroundColor: "var(--bg-1)", color: "var(--fg-3)", border: "none" },
  ".cm-activeLine, .cm-activeLineGutter": { backgroundColor: "var(--bg-2)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": { backgroundColor: "var(--sel) !important" },
  ".cm-cursor": { borderLeftColor: "var(--accent)" },
  ".cm-scroller": { overflow: "auto" },
});

export function mount(parent, doc, { lang = "yaml", onChange, readOnly = false, original } = {}) {
  const ext = [basicSetup, theme, syntaxHighlighting(highlight), keymap.of([indentWithTab]), lintGutter(), EditorView.lineWrapping];
  if (lang === "yaml") ext.push(yaml());
  if (readOnly) ext.push(EditorState.readOnly.of(true), EditorView.editable.of(false));
  if (original !== undefined) ext.push(unifiedMergeView({ original, mergeControls: false, gutter: true }));
  if (onChange) ext.push(EditorView.updateListener.of((u) => { if (u.docChanged) onChange(u.state.doc.toString()); }));
  const view = new EditorView({ parent, state: EditorState.create({ doc, extensions: ext }) });
  return {
    view,
    get value() { return view.state.doc.toString(); },
    set value(v) { view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } }); },
    // problems: [{line, message}] (1-based lines; 0 = whole file)
    mark(problems) {
      const doc = view.state.doc;
      const diags = problems.map((p) => {
        const line = doc.line(Math.min(Math.max(p.line || 1, 1), doc.lines));
        return { from: line.from, to: line.to, severity: "error", message: p.message };
      });
      view.dispatch(setDiagnostics(view.state, diags));
    },
    destroy() { view.destroy(); },
  };
}
