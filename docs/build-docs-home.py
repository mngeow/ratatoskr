#!/usr/bin/env python3
"""Regenerates docs/index.html, the documentation home page.

Optional maintenance helper. Run it from anywhere after adding, removing or renaming a document:

    python3 docs/build-docs-home.py

What it does:
  * lists every file under docs/ plus a few repository files, by category, with a title and a one-line
    summary taken from the file itself (or from the status table in docs/design/README.md);
  * embeds each Markdown file as escaped plain text, so the page can show a preview without a server;
  * adds a small "Docs home" link to the existing HTML pages (once; it is skipped if already there).

It reads files and writes docs/index.html and that link. It runs nothing from the project.
"""
import html
import json
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
    ('design', 'Design guides', 'Ground rules, the design index and the two early proposals.'),
    ('testing', 'Testing guides', 'Step-by-step manual test guides. Written from the source; nothing here has been run by the team.'),
    ('contracts', 'Contracts and designs', 'The authoritative description of what each part is meant to do, and where it is described as built. A contract is not evidence of behaviour unless it says so.'),
    ('reports', 'Reports and open issues', 'Candidate reports, reviews and the list of outstanding issues.'),
    ('papers', 'Briefs, handoffs and working papers', 'Task briefs and handoffs between team members. Kept as history. Not authoritative, and often superseded by the contract for the same area.'),
    ('repo', 'Repository files', 'The README, the rig definition and team culture, and the first spike brief. These are outside docs/.'),
    ('tools', 'Tools', 'The helper that generates this page.'),
]

CONTRACTS = {
    'interfaces.md', 'cross-machine-contract.md', 'cross-machine-design.md', 'daemon-contract.md', 'daemon-design.md',
    'network-api-contract.md', 'host-cards-contract.md', 'router-design.md', 'server-agent-design.md',
    'backend-structure.md', 'frontend-structure.md', 'participant-api-contract.md',
}
REPORTS = {'backend-report.md', 'candidate-report.md', 'store-review.md', 'outstanding-issues.md', 'pane-risks.md', 'frontend-input-bugs.md'}

REPO_TITLES = {
    'README.md': 'Project README',
    'AGENTS.md': 'Instructions for coding agents',
    'CLAUDE.md': 'Project instructions and default team culture',
    'rigs/team-ratatoskr/README.md': 'Team rig README',
    'rigs/team-ratatoskr/CULTURE.md': 'Team culture',
    'rigs/team-ratatoskr/context.md': 'Team context',
    'spikes/1-panes/BRIEF.md': 'Spike 1 brief: panes',
}

REPO_FILES = [
    ('README.md', 'The project README: what Ratatoskr is, how to build and run it, and the ADR table'),
    ('AGENTS.md', 'Instructions for coding agents working in this repository'),
    ('CLAUDE.md', 'Project instructions and the default team culture'),
    ('rigs/team-ratatoskr/README.md', 'The team rig: seats, roles and how they work together'),
    ('rigs/team-ratatoskr/CULTURE.md', 'The team culture for this rig'),
    ('rigs/team-ratatoskr/context.md', 'Shared context given to the team'),
    ('spikes/1-panes/BRIEF.md', 'The first spike brief, kept as history'),
]

WORKING_NOTE = 'Working paper / history. Not evidence of implemented behaviour.'


def read(path):
    with open(path, encoding='utf-8') as f:
        return f.read()


def strip_md(text):
    text = re.sub(r'\[([^\]]+)\]\([^)]*\)', r'\1', text)
    text = re.sub(r'[`*_]', '', text)
    return re.sub(r'\s+', ' ', text).strip()


def md_title_summary(text):
    title, summary = '', ''
    lines = text.split('\n')
    i = 0
    while i < len(lines):
        if lines[i].startswith('# '):
            title = strip_md(lines[i][2:])
            i += 1
            break
        i += 1
    para = []
    in_code = False
    for line in lines[i:]:
        if line.startswith('```'):
            in_code = not in_code
            continue
        if in_code:
            continue
        s = line.strip()
        if not s:
            if para:
                break
            continue
        if s.startswith(('#', '|', '>', '-', '*', '<', '!', '[!')) or re.match(r'^\d+\.', s):
            if para:
                break
            continue
        para.append(s)
    summary = strip_md(' '.join(para))
    if len(summary) > 230:
        summary = summary[:227].rsplit(' ', 1)[0] + '...'
    return title, summary


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

    def add(cat, path, rel_from_docs, kind, title, summary, status='', anchor='', extra=''):
        items.append(dict(cat=cat, path=path, href=rel_from_docs + anchor, kind=kind, title=title, summary=summary, status=status, extra=extra))

    previews = {}
    covered = set()

    for rel in all_docs_files():
        covered.add(rel)
        path = 'docs/' + rel
        text = read(os.path.join(DOCS, rel)) if rel.endswith(('.md', '.html', '.py')) else ''
        if rel.startswith('adr/deferred/'):
            ok = table.get(os.path.normpath(rel))
            title = html_title(text)
            if rel == 'adr/deferred/index.html':
                add('deferred', path, rel, 'html', title, 'The list of deferred decision records and what "deferred" means here', 'Section index')
            elif rel.endswith('0006-a2a-proposal/index.html'):
                add('deferred', path, rel, 'html', title, 'The full A2A proposal, moved here unchanged from ADR-0006 and kept as history; not a requirement', 'Deferred 2026-11-08')
            else:
                add('deferred', path, rel, 'html', title, 'Why A2A is deferred (fit, not inability), what was built, the branch kept, and the first-run findings', 'Deferred 2026-11-08')
        elif rel.startswith('adr/'):
            ok = table.get(os.path.normpath(rel))
            m = ok or ('', '', '')
            title = m[0] or html_title(text)
            add('adr', path, rel, 'html', title, m[2], m[1])
        elif rel == 'testing/index.html':
            m = table.get('testing/index.html') or table.get(os.path.normpath('testing/index.html'))
            add('testing', path, rel, 'html', 'Testing the daemon by hand: the full guide', (m[2] if m else 'Overview and scenarios 1 to 5, with diagrams, fill-in fields and copy buttons'), (m[1] if m else 'Written from the source; not run by the team'))
            for sid, kick, h2 in re.findall(r'<section class="scn" id="(s\d)"><header class="scn-h"><p class="kick">([^<]*)</p><h2>([^<]*)</h2>', text):
                add('testing', path + '#' + sid, rel, 'html', h2, 'Jump straight to this scenario in the full guide', 'Written, not verified', '#' + sid)
        elif rel == 'testing/README.md':
            t, s = md_title_summary(text)
            add('testing', path, rel, 'md', 'Testing the daemon by hand: short form', 'The same scenarios as the full guide in plain text, with the owner\'s report and the scenario table', 'Short form of the guide, for reading on a machine without a browser')
            previews[rel] = text
        elif rel.startswith('testing/scenario-') and rel.endswith('README.md'):
            t, s = md_title_summary(text)
            add('testing', path, rel, 'md', t, s, 'Short form of one scenario; the full HTML guide has diagrams and copy buttons')
            previews[rel] = text
        elif rel.startswith('design/'):
            if rel.endswith('.html'):
                m = table.get(os.path.normpath(rel)) or ('', '', '')
                add('design', path, rel, 'html', html_title(text) or m[0], m[2], m[1])
            else:
                t, s = md_title_summary(text)
                m = table.get(os.path.normpath(rel)) or ('', '', '')
                add('design', path, rel, 'md', t, m[2] or s, m[1])
                previews[rel] = text
        elif rel.startswith('implementation/'):
            name = rel.split('/', 1)[1]
            t, s = md_title_summary(text)
            m = table.get(os.path.normpath(rel)) or ('', '', '')
            if name in CONTRACTS:
                add('contracts', path, rel, 'md', t, m[2] or s, m[1])
            elif name in REPORTS:
                add('reports', path, rel, 'md', t, m[2] or s, m[1] or ('Maintained by the lead' if name == 'outstanding-issues.md' else ''))
            else:
                add('papers', path, rel, 'md', t, s, WORKING_NOTE)
            previews[rel] = text
        elif rel.startswith('spec/participant-api/'):
            name = rel.split('/', 2)[2]
            if name in ('README.md', 'CHANGELOG.md'):
                tt, ss = md_title_summary(text)
                add('contracts', path, rel, 'md', tt, 'The language-neutral participant API: normative prose for adapter authors (README) or its change log (CHANGELOG). Implemented on a branch, never run' if name == 'README.md' else ss, 'Spec, never run')
                previews[rel] = text
            elif not any(i['path'] == 'docs/spec/participant-api/schemas/' for i in items):
                add('contracts', 'docs/spec/participant-api/schemas/', 'spec/participant-api/schemas/', 'other', 'Participant API schemas and conformance examples', 'The JSON Schemas (schemas/) and the conformance exchanges (examples/) of the participant API: a folder of files, not a page', 'Spec, never run')
        elif rel == 'build-docs-home.py':
            add('tools', path, rel, 'tool', 'build-docs-home.py', 'Regenerates this page and adds the "Docs home" link to the HTML pages. Optional; a Python 3 script that only reads files and writes docs/index.html.', 'Helper')
        else:
            add('papers', path, rel, 'other', rel, 'Not classified by the generator. Listed so nothing is hidden.', 'Unclassified')

    for rel, blurb in REPO_FILES:
        full = os.path.join(ROOT, rel)
        if not os.path.exists(full):
            continue
        text = read(full)
        t, _ = md_title_summary(text)
        add('repo', rel, '../' + rel, 'md', REPO_TITLES.get(rel, t or rel), blurb, '')
        previews['../' + rel] = text

    # ordering inside categories
    def sort_key(it):
        return (it['cat'], it['path'])
    cat_index = {c[0]: n for n, c in enumerate(CATS)}
    items.sort(key=lambda it: (cat_index[it['cat']], it['path'] if it['cat'] != 'testing' else (0 if it['path'].endswith('testing/index.html') else 1 if '#' in it['path'] else 2, it['path'])))

    # embed previews (escaped inside JSON; the page only ever uses textContent)
    data = json.dumps(previews, ensure_ascii=False).replace('<', '\\u003c').replace('>', '\\u003e').replace('&', '\\u0026').replace('\u2028', '\\u2028').replace('\u2029', '\\u2029')

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
            elif kind == 'md':
                badge = '<span class="badge md" title="Markdown: a browser may show raw text or download it. Use Preview here.">Markdown</span>'
            elif kind == 'tool':
                badge = '<span class="badge tool">Script</span>'
            else:
                badge = '<span class="badge md">File</span>'
            status = f'<p class="status">{esc(it["status"])}</p>' if it['status'] else ''
            summary = f'<p class="sum">{esc(it["summary"])}</p>' if it['summary'] else ''
            pv = ''
            if kind == 'md':
                prev_key = it['href']
                pv = f'<button type="button" class="pv" data-preview="{esc(prev_key)}" data-title="{esc(it["title"])}">Preview</button>'
            hay = ' '.join([it['title'], it['path'], name, it['status'], it['summary'], kind]).lower()
            li.append(
                f'<li class="item" data-kind="{kind}" data-hay="{esc(hay)}">'
                f'<div class="top"><a class="ttl" href="{esc(it["href"])}">{esc(it["title"])}</a>{badge}{pv}</div>'
                f'{summary}{status}<p class="path"><code>{esc(it["path"])}</code></p></li>')
        sections.append(
            f'<section class="cat" id="{key}" aria-labelledby="h-{key}">'
            f'<nav class="crumb" aria-label="Breadcrumb"><a href="#top">Docs home</a> <span aria-hidden="true">&rsaquo;</span> <span>{esc(name)}</span></nav>'
            f'<h2 id="h-{key}">{esc(name)} <span class="cnt" data-total="{len(rows)}">{len(rows)}</span></h2>'
            f'<p class="blurb">{esc(blurb)}</p><ul class="list">{"".join(li)}</ul></section>')

    total = len(items)
    page = PAGE
    page = page.replace('@@NAV@@', nav_html).replace('@@SECTIONS@@', '\n'.join(sections)).replace('@@TOTAL@@', str(total)).replace('@@DATA@@', data)
    with open(OUT, 'w', encoding='utf-8') as f:
        f.write(page)

    # link back from the existing HTML pages (once)
    linked = 0
    for it in items:
        if it['kind'] != 'html' or '#' in it['path']:
            continue
        full = os.path.join(ROOT, it['path'])
        depth = it['path'].count('/') - 1  # docs/x/index.html -> 0 levels below docs/x
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

    listed = {it['path'][5:].split('#')[0] for it in items if it['path'].startswith('docs/')}
    folders = [p for p in listed if p.endswith('/')]
    # A file inside a folder entry (the participant API schemas and examples) is listed through that folder.
    missing = sorted(f for f in set(all_docs_files()) - listed if not any(f.startswith(d) for d in folders) and not f.startswith('spec/participant-api/examples/'))
    print('items', total, 'categories', {k: v for k, v in counts.items() if v}, 'pages linked', linked, 'unlisted docs files', missing)


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
a:focus-visible, button:focus-visible, input:focus-visible, summary:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
code { font: .86em ui-monospace, Menlo, Consolas, monospace; color: var(--yellow); }
.skip { position: absolute; left: -999px; top: 6px; background: var(--yellow); color: #000; padding: 6px 10px; border-radius: 6px; z-index: 30; }
.skip:focus { left: 8px; }
header.bar { position: sticky; top: 0; z-index: 20; background: var(--panel); border-bottom: 1px solid var(--line); }
.bar-in { display: flex; flex-wrap: wrap; gap: 10px 18px; align-items: center; padding: 10px 16px; max-width: 1280px; margin: 0 auto; }
.brand { font-weight: 700; letter-spacing: .02em; color: var(--green); text-decoration: none; }
.brand small { color: var(--muted); font-weight: 400; margin-left: 8px; }
.search { flex: 1 1 260px; display: flex; gap: 8px; align-items: center; }
.search input { flex: 1; min-width: 0; background: var(--bg); color: var(--ink); border: 1px solid var(--line); border-radius: 8px; padding: 8px 12px; font: inherit; }
.chips { display: flex; gap: 6px; }
.chips button { background: transparent; color: var(--ink); border: 1px solid var(--line); border-radius: 999px; padding: 4px 12px; font: inherit; font-size: .88rem; cursor: pointer; }
.chips button[aria-pressed="true"] { background: var(--raise); border-color: var(--link); color: var(--link); }
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
.badge.md { color: var(--orange); border-color: var(--orange); }
.badge.tool { color: var(--purple); border-color: var(--purple); }
.pv { margin-left: auto; background: var(--bg); color: var(--ink); border: 1px solid var(--line); border-radius: 6px; padding: 2px 10px; font: inherit; font-size: .85rem; cursor: pointer; }
.pv:hover { border-color: var(--link); }
.sum { margin: 6px 0 0; }
.status { margin: 4px 0 0; color: var(--yellow); font-size: .9rem; }
.path { margin: 4px 0 0; font-size: .82rem; color: var(--muted); }
.note { color: var(--muted); font-size: .9rem; }
#empty { display: none; padding: 20px; border: 1px dashed var(--line); border-radius: 8px; color: var(--muted); }
dialog { width: min(980px, 96vw); max-height: 90vh; background: var(--panel); color: var(--ink); border: 1px solid var(--line); border-radius: 12px; padding: 0; }
dialog::backdrop { background: rgba(0,0,0,.65); }
.dlg-h { display: flex; gap: 10px; align-items: center; padding: 10px 14px; border-bottom: 1px solid var(--line); position: sticky; top: 0; background: var(--panel); }
.dlg-h h3 { margin: 0; font-size: 1rem; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dlg-h button { background: var(--bg); color: var(--ink); border: 1px solid var(--line); border-radius: 6px; padding: 4px 12px; font: inherit; cursor: pointer; }
dialog pre { margin: 0; padding: 14px 16px; overflow: auto; max-height: calc(90vh - 56px); white-space: pre-wrap; overflow-wrap: anywhere; font: .84rem/1.5 ui-monospace, Menlo, Consolas, monospace; background: var(--bg); }
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
      <div class="chips" role="group" aria-label="Show">
        <button type="button" data-kind="all" aria-pressed="true">All</button>
        <button type="button" data-kind="html" aria-pressed="false">HTML</button>
        <button type="button" data-kind="md" aria-pressed="false">Markdown</button>
      </div>
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
      <p class="ok">One page that links to every document in the repository. Pick a decision record, a testing guide, a contract, or search for anything.</p>
      <p><b>HTML</b> documents open straight from this page. <b>Markdown</b> files are plain text: a browser may show them raw or offer to download them, so each has a <b>Preview</b> button that shows the text here, escaped, with no rendering and no server. Nothing on this page is fetched; it works from a local checkout.</p>
      <p>Status words come from the documents themselves. A document is not evidence that something works unless it says so, and the team has not run most of what is described. Working papers and older reports are kept so nothing is hidden, and are labelled as history.</p>
      <p class="open">Open this page: <code>open docs/index.html</code> (macOS, from the repository root). Bookmark it once.</p>
      <p class="note">@@TOTAL@@ entries. Not checked in a browser by the team that wrote it.</p>
    </section>
    <p id="empty" role="status">Nothing matches that filter.</p>
    @@SECTIONS@@
  </main>
</div>
<dialog id="dlg" aria-labelledby="dlg-t">
  <div class="dlg-h"><h3 id="dlg-t"></h3><button type="button" id="dlg-x">Close</button></div>
  <pre id="dlg-pre" tabindex="0"></pre>
</dialog>
<script type="application/json" id="previews">@@DATA@@</script>
<script>
(function () {
  'use strict';
  var items = Array.prototype.slice.call(document.querySelectorAll('.item'));
  var cats = Array.prototype.slice.call(document.querySelectorAll('.cat'));
  var q = document.getElementById('q'), countEl = document.getElementById('count'), empty = document.getElementById('empty');
  var chips = Array.prototype.slice.call(document.querySelectorAll('.chips button'));
  var kind = 'all';
  function apply() {
    var terms = q.value.toLowerCase().split(/\s+/).filter(Boolean), shown = 0;
    items.forEach(function (it) {
      var hay = it.getAttribute('data-hay'), k = it.getAttribute('data-kind');
      var ok = terms.every(function (t) { return hay.indexOf(t) !== -1; }) && (kind === 'all' || k === kind);
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
  chips.forEach(function (b) {
    b.addEventListener('click', function () {
      kind = b.getAttribute('data-kind');
      chips.forEach(function (x) { x.setAttribute('aria-pressed', x === b ? 'true' : 'false'); });
      apply();
    });
  });
  apply();

  var data = {};
  try { data = JSON.parse(document.getElementById('previews').textContent); } catch (e) { data = {}; }
  var dlg = document.getElementById('dlg'), pre = document.getElementById('dlg-pre'), tt = document.getElementById('dlg-t'), last = null;
  document.addEventListener('click', function (e) {
    var b = e.target.closest ? e.target.closest('.pv') : null;
    if (!b) return;
    var key = b.getAttribute('data-preview');
    last = b;
    tt.textContent = b.getAttribute('data-title') + '  (' + key.replace(/^\.\.\//, '') + ')';
    pre.textContent = Object.prototype.hasOwnProperty.call(data, key) ? data[key] : 'No preview text was embedded for this file.';
    pre.scrollTop = 0;
    if (typeof dlg.showModal === 'function') dlg.showModal(); else dlg.setAttribute('open', '');
    pre.focus();
  });
  function closeDlg() { if (typeof dlg.close === 'function') dlg.close(); else dlg.removeAttribute('open'); if (last) last.focus(); }
  document.getElementById('dlg-x').addEventListener('click', closeDlg);
  dlg.addEventListener('click', function (e) { if (e.target === dlg) closeDlg(); });

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
