# Vendored browser assets

Both files are checked in unmodified, exactly as published, so the dashboard loads nothing from a CDN at
runtime and works on a machine with no network route to the internet. There are no other vendored assets
and no other runtime dependencies; everything else the dashboard loads is served from this directory.

Recorded 2026-11-11. The pinned URLs are jsdelivr's npm package paths, which serve the published tarball
contents at an exact version.

## htmx

- Package: `htmx.org`, version `2.0.11`
- File: `dist/htmx.min.js`
- URL: https://cdn.jsdelivr.net/npm/htmx.org@2.0.11/dist/htmx.min.js
- Licence: **0BSD** (BSD Zero Clause). The published minified file carries no banner or copyright line, so
  none is reproduced here; the licence is the package's declared licence.
- SHA-256 of the vendored file:
  `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`
- Size: 52,182 bytes

## Cytoscape.js

- Package: `cytoscape`, version `3.35.0`
- File: `dist/cytoscape.min.js`
- URL: https://cdn.jsdelivr.net/npm/cytoscape@3.35.0/dist/cytoscape.min.js
- Licence: **MIT**. The file keeps its upstream notice, reproduced here exactly as it appears:

  ```
  Copyright (c) 2016-2026, The Cytoscape Consortium.

  Permission is hereby granted, free of charge, to any person obtaining a copy of
  this software and associated documentation files (the “Software”), to deal in
  the Software without restriction, including without limitation the rights to
  use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies
  of the Software, and to permit persons to whom the Software is furnished to do
  so, subject to the following conditions:

  The above copyright notice and this permission notice shall be included in all
  copies or substantial portions of the Software.

  THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
  IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
  FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
  AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
  LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
  OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
  SOFTWARE.
  ```

- SHA-256 of the vendored file:
  `71fa9ebec21a7ca2c40cff6aacc30d1f6464e363662ad4b2c197e1adc770cd94`
- Size: 442,752 bytes

## Verifying a copy

```sh
shasum -a 256 static/htmx.min.js static/cytoscape.min.js
```

The two hashes above must match. Nothing in this directory is generated, patched or minified by this
project, and updating either asset means updating this file with the new version, URL, licence and hash.
