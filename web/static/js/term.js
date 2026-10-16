// Container terminal: xterm.js over a WebSocket to /c/{id}/exec.
import { Terminal, FitAddon } from "/static/vendor/term.js";

const el = document.getElementById("term");
const css = getComputedStyle(document.documentElement);
const term = new Terminal({
  cursorBlink: true,
  fontFamily: css.getPropertyValue("--mono"),
  fontSize: 13,
  theme: { background: "#0b0d10", foreground: "#e7e9eb", cursor: "#5b8ff9", selectionBackground: "#263c66" },
});
const fit = new FitAddon();
term.loadAddon(fit);
term.open(el);
fit.fit();

const proto = location.protocol === "https:" ? "wss:" : "ws:";
const ws = new WebSocket(`${proto}//${location.host}/c/${el.dataset.id}/exec?cols=${term.cols}&rows=${term.rows}`);
ws.binaryType = "arraybuffer";
const enc = new TextEncoder();
ws.onmessage = (e) => term.write(typeof e.data === "string" ? e.data : new Uint8Array(e.data));
ws.onclose = (e) => term.write(`\r\n\x1b[90m${e.reason || "Session terminée"}\x1b[0m\r\n`);
term.onData((d) => { if (ws.readyState === 1) ws.send(enc.encode(d)); });
const resize = () => {
  fit.fit();
  if (ws.readyState === 1) ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
};
new ResizeObserver(resize).observe(el);
ws.onopen = () => { resize(); term.focus(); };
