#!/usr/bin/env python3
"""Regenerates docs/index.html, the documentation home page.

Optional maintenance helper. Run it from anywhere after adding, removing or renaming a document:

    python3 docs/build-docs-home.py

What it does:
  * lists every HTML page under docs/, by category, with a title and a one-line summary taken from the page itself
    (or from the status table in docs/design/README.md), plus the participant API's schemas folder;
  * lists no Markdown and no other file a browser does not render: the owner wants the home page to hold only
    browser-readable documentation. Those files stay in the repository; the summary line counts what was left out;
  * adds a small "Docs home" link to the existing HTML pages (once; it is skipped if already there).

It reads files and writes docs/index.html and that link. It runs nothing from the project.
"""
import html
import os
import re
import sys

DOCS = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(DOCS)
OUT = os.path.join(DOCS, 'index.html')

# ---------------------------------------------------------------- categories (order is the nav order)
CATS = [
    ('adr', 'Decision records', 'ADR-0001 to ADR-0009. Each is a self-contained HTML page with its status near the top. ADR-0006 is deferred.'),
    ('deferred', 'Deferred ADRs', 'Decisions set aside on purpose, with the reason. Not accepted, not rejected, not active requirements.'),
    ('guides', 'User guides', 'How a feature works and how to set it up, for the person using it. Written from the source; nothing here has been run by the team.'),
    ('design', 'Design guides', 'The two early design proposals.'),
    ('testing', 'Testing guides', 'Step-by-step manual test guides. Written from the source; nothing here has been run by the team.'),
    ('specs', 'Specifications', 'Machine-readable contracts. A contract is not evidence of behaviour unless it says so.'),
    ('other', 'Other pages', 'HTML pages the generator does not classify yet. Listed so nothing new is hidden.'),
]

SCHEMAS = 'spec/participant-api/schemas/'


def read(path):
    with open(path, encoding='utf-8') as f:
        return f.read()


def strip_md(text):
    text = re.sub(r'\[([^\]]+)\]\([^)]*\)', r'\1', text)
    text = re.sub(r'[`*_]', '', text)
    return no_md_names(re.sub(r'\s+', ' ', text).strip())


# Summary text comes from docs/design/README.md and may name a Markdown file. The home page shows no Markdown,
# so a file name becomes words: an exact phrase first, then any other name.md as its words.
MD_PHRASES = {
    'implementation contract: participant-api-contract.md': 'participant API implementation contract',
}


def no_md_names(text):
    for old, new in MD_PHRASES.items():
        text = text.replace(old, new)
    return re.sub(r'(?:[\w.-]+/)*([\w-]+)\.md\b', lambda m: m.group(1).replace('-', ' '), text)


def html_title(text):
    m = re.search(r'<title>(.*?)</title>', text, re.S)
    return html.unescape(re.sub(r'\s+', ' ', m.group(1))).strip() if m else ''


def design_table():
    """Status and summary columns of the design index, keyed by the linked path (relative to docs/)."""
    rows = {}
    for line in read(os.path.join(DOCS, 'design', 'README.md')).split('\n'):
        if not line.startswith('| ['):
            continue
        cells = [c.strip() for c in line.strip().strip('|').split(' | ')]
        if len(cells) < 3:
            continue
        m = re.match(r'\[([^\]]+)\]\(([^)]+)\)', cells[0])
        if not m:
            continue
        target = os.path.normpath(os.path.join('design', m.group(2)))
        rows[target] = (strip_md(m.group(1)), strip_md(cells[1]), strip_md(cells[2]))
    return rows


def all_docs_files():
    found = []
    for base, _, names in os.walk(DOCS):
        for n in names:
            if n.startswith('.') or n == 'index.html' and base == DOCS:
                continue
            found.append(os.path.relpath(os.path.join(base, n), DOCS).replace(os.sep, '/'))
    return sorted(found)


def main():
    table = design_table()
    items = []  # dicts
    left_out = {}  # extension -> count of files not listed (Markdown, scripts, ...)

    def add(cat, path, rel_from_docs, kind, title, summary, status='', anchor=''):
        items.append(dict(cat=cat, path=path, href=rel_from_docs + anchor, kind=kind, title=title, summary=summary, status=status))

    for rel in all_docs_files():
        path = 'docs/' + rel
        if rel.startswith(SCHEMAS) or rel.startswith('spec/participant-api/examples/'):
            if not any(i['path'] == 'docs/' + SCHEMAS for i in items):
                add('specs', 'docs/' + SCHEMAS, SCHEMAS, 'folder', 'Participant API schemas and conformance examples', 'The JSON Schemas (schemas/) and the conformance exchanges (examples/) of the participant API: a folder of files, not a page', 'Spec, never run')
            continue
        if not rel.endswith('.html'):
            ext = os.path.splitext(rel)[1] or '(none)'
            left_out[ext] = left_out.get(ext, 0) + 1
            continue
        text = read(os.path.join(DOCS, rel))
        if rel.startswith('adr/deferred/'):
            title = html_title(text)
            if rel == 'adr/deferred/index.html':
                add('deferred', path, rel, 'html', title, 'The list of deferred decision records and what "deferred" means here', 'Section index')
            elif rel.endswith('0006-a2a-proposal/index.html'):
                add('deferred', path, rel, 'html', title, 'The full A2A proposal, moved here unchanged from ADR-0006 and kept as history; not a requirement', 'Deferred 2026-11-08')
            else:
                add('deferred', path, rel, 'html', title, 'Why A2A is deferred (fit, not inability), what was built, the branch kept, and the first-run findings', 'Deferred 2026-11-08')
        elif rel.startswith('adr/'):
            m = table.get(os.path.normpath(rel)) or ('', '', '')
            add('adr', path, rel, 'html', m[0] or html_title(text), m[2], m[1])
        elif rel == 'testing/index.html':
            m = table.get('testing/index.html')
            add('testing', path, rel, 'html', 'Testing the daemon by hand: the full guide', (m[2] if m else 'Overview and scenarios 1 to 5, with diagrams, fill-in fields and copy buttons'), (m[1] if m else 'Written from the source; not run by the team'))
            for sid, kick, h2 in re.findall(r'<section class="scn" id="(s\d)"><header class="scn-h"><p class="kick">([^<]*)</p><h2>([^<]*)</h2>', text):
                add('testing', path + '#' + sid, rel, 'html', h2, 'Jump straight to this scenario in the full guide', 'Written, not verified', '#' + sid)
        elif rel.startswith('guides/'):
            add('guides', path, rel, 'html', html_title(text) or rel, 'How the pieces fit together, with diagrams and message-flow scenarios', 'Written from the source; not run by the team')
        elif rel.startswith('design/'):
            m = table.get(os.path.normpath(rel)) or ('', '', '')
            add('design', path, rel, 'html', html_title(text) or m[0], m[2], m[1])
        else:
            add('other', path, rel, 'html', html_title(text) or rel, 'Not classified by the generator. Listed so nothing is hidden.', 'Unclassified')

    # ordering inside categories
    cat_index = {c[0]: n for n, c in enumerate(CATS)}
    items.sort(key=lambda it: (cat_index[it['cat']], it['path'] if it['cat'] != 'testing' else (0 if it['path'].endswith('testing/index.html') else 1, it['path'])))

    def esc(s):
        return html.escape(s, quote=True)

    counts = {c[0]: sum(1 for it in items if it['cat'] == c[0]) for c in CATS}
    nav_html = ''.join(
        f'<li><a href="#{c[0]}"><span>{esc(c[1])}</span><b>{counts[c[0]]}</b></a></li>' for c in CATS if counts[c[0]])

    sections = []
    for key, name, blurb in CATS:
        rows = [it for it in items if it['cat'] == key]
        if not rows:
            continue
        li = []
        for it in rows:
            kind = it['kind']
            if kind == 'html':
                badge = '<span class="badge html" title="Opens directly in a browser">HTML</span>'
            else:
                badge = '<span class="badge folder" title="A folder of files">Folder</span>'
            status = f'<p class="status">{esc(it["status"])}</p>' if it['status'] else ''
            summary = f'<p class="sum">{esc(it["summary"])}</p>' if it['summary'] else ''
            hay = ' '.join([it['title'], it['path'], name, it['status'], it['summary']]).lower()
            li.append(
                f'<li class="item" data-kind="{kind}" data-hay="{esc(hay)}">'
                f'<div class="top"><a class="ttl" href="{esc(it["href"])}">{esc(it["title"])}</a>{badge}</div>'
                f'{summary}{status}<p class="path"><code>{esc(it["path"])}</code></p></li>')
        sections.append(
            f'<section class="cat" id="{key}" aria-labelledby="h-{key}">'
            f'<nav class="crumb" aria-label="Breadcrumb"><a href="#top">Docs home</a> <span aria-hidden="true">&rsaquo;</span> <span>{esc(name)}</span></nav>'
            f'<h2 id="h-{key}">{esc(name)} <span class="cnt" data-total="{len(rows)}">{len(rows)}</span></h2>'
            f'<p class="blurb">{esc(blurb)}</p><ul class="list">{"".join(li)}</ul></section>')

    total = len(items)
    page = PAGE
    page = page.replace('@@NAV@@', nav_html).replace('@@SECTIONS@@', '\n'.join(sections)).replace('@@TOTAL@@', str(total))
    with open(OUT, 'w', encoding='utf-8') as f:
        f.write(page)

    # link back from the existing HTML pages (once)
    linked = 0
    for it in items:
        if it['kind'] != 'html' or '#' in it['path']:
            continue
        full = os.path.join(ROOT, it['path'])
        rel = '../' * (it['path'].count('/') - 1) + 'index.html'
        text = read(full)
        if 'data-docs-home' in text:
            continue
        tag = ('<nav data-docs-home aria-label="Documentation home" style="font:14px/1.4 system-ui,sans-serif;padding:6px 14px;'
               'background:#272822;color:#f8f8f2;border-bottom:1px solid #49483e"><a href="' + rel + '" style="color:#66d9ef">&larr; Docs home</a></nav>')
        if '<body>' not in text:
            print('skipped (no plain <body>):', it['path'], file=sys.stderr)
            continue
        with open(full, 'w', encoding='utf-8') as f:
            f.write(text.replace('<body>', '<body>\n' + tag, 1))
        linked += 1

    print('items', total, 'categories', {k: v for k, v in counts.items() if v}, 'pages linked', linked,
          'files not listed (kept in the repository), by extension', dict(sorted(left_out.items())))


PAGE = r'''<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Ratatoskr Documentation</title>
<style>
:root { color-scheme: dark; --bg:#272822; --panel:#2f302a; --raise:#3a3b33; --line:#49483e; --ink:#f8f8f2; --muted:#b6b29b; --link:#66d9ef; --green:#a6e22e; --orange:#fd971f; --pink:#f92672; --purple:#ae81ff; --yellow:#e6db74; --focus:#e6db74; }
* { box-sizing: border-box; }
html { scroll-behavior: smooth; scroll-padding-top: 76px; }
@media (prefers-reduced-motion: reduce) { html { scroll-behavior: auto; } }
body { margin: 0; background: var(--bg); color: var(--ink); font: 16px/1.55 system-ui, -apple-system, "Segoe UI", sans-serif; }
a { color: var(--link); }
a:focus-visible, input:focus-visible, summary:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
code { font: .86em ui-monospace, Menlo, Consolas, monospace; color: var(--yellow); }
.skip { position: absolute; left: -999px; top: 6px; background: var(--yellow); color: #000; padding: 6px 10px; border-radius: 6px; z-index: 30; }
.skip:focus { left: 8px; }
header.bar { position: sticky; top: 0; z-index: 20; background: var(--panel); border-bottom: 1px solid var(--line); }
.bar-in { display: flex; flex-wrap: wrap; gap: 10px 18px; align-items: center; padding: 10px 16px; max-width: 1280px; margin: 0 auto; }
.brand { font-weight: 700; letter-spacing: .02em; color: var(--green); text-decoration: none; }
.brand small { color: var(--muted); font-weight: 400; margin-left: 8px; }
.search { flex: 1 1 260px; display: flex; gap: 8px; align-items: center; }
.search input { flex: 1; min-width: 0; background: var(--bg); color: var(--ink); border: 1px solid var(--line); border-radius: 8px; padding: 8px 12px; font: inherit; }
.layout { display: grid; grid-template-columns: 250px minmax(0, 1fr); gap: 28px; max-width: 1280px; margin: 0 auto; padding: 0 16px 60px; }
aside.side { position: sticky; top: 64px; align-self: start; max-height: calc(100vh - 72px); overflow: auto; padding: 18px 0; }
aside.side h2 { font-size: .78rem; text-transform: uppercase; letter-spacing: .08em; color: var(--muted); margin: 0 0 8px; }
aside.side ul { list-style: none; margin: 0; padding: 0; }
aside.side a { display: flex; justify-content: space-between; gap: 8px; padding: 6px 10px; border-radius: 6px; color: var(--ink); text-decoration: none; }
aside.side a:hover { background: var(--panel); }
aside.side a[aria-current="true"] { background: var(--raise); color: var(--green); }
aside.side b { color: var(--muted); font-weight: 400; }
main { padding-top: 18px; min-width: 0; }
.hero { border: 1px solid var(--line); background: var(--panel); border-radius: 12px; padding: 18px 20px; margin-bottom: 18px; }
.hero h1 { margin: 0 0 6px; font-size: 1.6rem; }
.hero p { margin: 6px 0; color: var(--muted); max-width: 68ch; }
.hero .ok { color: var(--ink); }
.open { display: inline-block; background: var(--bg); border: 1px solid var(--line); border-radius: 8px; padding: 6px 10px; margin-top: 4px; }
.cat { margin: 26px 0; }
.crumb { font-size: .85rem; color: var(--muted); }
.crumb a { color: var(--muted); }
.cat h2 { margin: 4px 0 2px; font-size: 1.3rem; color: var(--ink); }
.cnt { font-size: .8rem; color: var(--bg); background: var(--purple); border-radius: 999px; padding: 1px 8px; margin-left: 8px; vertical-align: middle; }
.blurb { margin: 4px 0 12px; color: var(--muted); max-width: 72ch; }
.list { list-style: none; margin: 0; padding: 0; display: grid; gap: 10px; }
.item { background: var(--panel); border: 1px solid var(--line); border-left: 3px solid var(--link); border-radius: 8px; padding: 10px 14px; }
.item[hidden] { display: none; }
.item .top { display: flex; flex-wrap: wrap; gap: 8px 10px; align-items: center; }
.ttl { font-weight: 650; text-decoration: none; }
.ttl:hover { text-decoration: underline; }
.badge { font-size: .72rem; border-radius: 4px; padding: 1px 7px; border: 1px solid; }
.badge.html { color: var(--green); border-color: var(--green); }
.badge.folder { color: var(--purple); border-color: var(--purple); }
.sum { margin: 6px 0 0; }
.status { margin: 4px 0 0; color: var(--yellow); font-size: .9rem; }
.path { margin: 4px 0 0; font-size: .82rem; color: var(--muted); }
.note { color: var(--muted); font-size: .9rem; }
#empty { display: none; padding: 20px; border: 1px dashed var(--line); border-radius: 8px; color: var(--muted); }
.mnav { display: none; }
@media (max-width: 860px) {
  .layout { grid-template-columns: 1fr; }
  aside.side { display: none; }
  .mnav { display: block; margin: 10px 0 0; }
  .mnav summary { cursor: pointer; padding: 8px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--panel); }
  .mnav ul { list-style: none; margin: 6px 0 0; padding: 0; display: grid; gap: 4px; }
  .mnav a { display: flex; justify-content: space-between; padding: 8px 12px; background: var(--panel); border-radius: 6px; text-decoration: none; color: var(--ink); }
}
</style>
</head>
<body id="top">
<a class="skip" href="#main">Skip to the documents</a>
<header class="bar">
  <div class="bar-in">
    <a class="brand" href="#top">Ratatoskr<small>documentation</small></a>
    <div class="search" role="search">
      <input id="q" type="search" placeholder="Filter by title, path, status or category" aria-label="Filter documents" autocomplete="off">
    </div>
    <span class="note" id="count" aria-live="polite"></span>
  </div>
</header>
<div class="layout">
  <aside class="side" aria-label="Sections"><h2>Sections</h2><ul id="sidenav">@@NAV@@</ul></aside>
  <main id="main">
    <details class="mnav"><summary>Sections</summary><ul>@@NAV@@</ul></details>
    <section class="hero">
      <h1>Ratatoskr documentation</h1>
      <p class="ok">One page for the documentation that opens in a browser: the decision records, the guides, the testing instructions and the specifications. Search for anything.</p>
      <p>Every entry is an HTML page, or the folder of participant API schemas, and opens straight from this page. Nothing on this page is fetched; it works from a local checkout.</p>
      <p>Status words come from the documents themselves. A document is not evidence that something works unless it says so, and the team has not run most of what is described.</p>
      <p class="open">Open this page: <code>open docs/index.html</code> (macOS, from the repository root). Bookmark it once.</p>
      <p class="note">@@TOTAL@@ entries. Not checked in a browser by the team that wrote it.</p>
    </section>
    <p id="empty" role="status">Nothing matches that filter.</p>
    @@SECTIONS@@
  </main>
</div>
<script>
(function () {
  'use strict';
  var items = Array.prototype.slice.call(document.querySelectorAll('.item'));
  var cats = Array.prototype.slice.call(document.querySelectorAll('.cat'));
  var q = document.getElementById('q'), countEl = document.getElementById('count'), empty = document.getElementById('empty');
  function apply() {
    var terms = q.value.toLowerCase().split(/\s+/).filter(Boolean), shown = 0;
    items.forEach(function (it) {
      var hay = it.getAttribute('data-hay');
      var ok = terms.every(function (t) { return hay.indexOf(t) !== -1; });
      it.hidden = !ok;
      if (ok) shown++;
    });
    cats.forEach(function (c) {
      var visible = c.querySelectorAll('.item:not([hidden])').length;
      c.hidden = visible === 0;
      var cnt = c.querySelector('.cnt');
      cnt.textContent = visible === +cnt.getAttribute('data-total') ? visible : visible + ' of ' + cnt.getAttribute('data-total');
    });
    empty.style.display = shown === 0 ? 'block' : 'none';
    countEl.textContent = 'Showing ' + shown + ' of ' + items.length;
  }
  q.addEventListener('input', apply);
  apply();

  var links = Array.prototype.slice.call(document.querySelectorAll('#sidenav a'));
  function mark() {
    var cur = null, y = window.scrollY + 90;
    cats.forEach(function (c) { if (!c.hidden && c.offsetTop <= y) cur = c.id; });
    links.forEach(function (a) { a.setAttribute('aria-current', a.getAttribute('href') === '#' + cur ? 'true' : 'false'); });
  }
  window.addEventListener('scroll', mark, { passive: true });
  mark();
}());
</script>
</body>
</html>
'''

if __name__ == '__main__':
    main()
