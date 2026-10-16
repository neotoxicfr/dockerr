#!/bin/sh
# Rebuilds the vendored editor and terminal bundles into web/static/vendor.
set -eu
cd "$(dirname "$0")"
npm ci --no-audit --no-fund
out=../../web/static/vendor
npx esbuild cm.js --bundle --format=esm --minify --legal-comments=none --outfile=$out/cm.js
npx esbuild term.js --bundle --format=esm --minify --legal-comments=none --outfile=$out/term.js
cp node_modules/@xterm/xterm/css/xterm.css $out/xterm.css
