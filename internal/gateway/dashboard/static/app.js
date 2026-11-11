/*
 * The dashboard's only behaviour. ADR-0009 sections 9 to 12: the Network page draws the gateway as a hub
 * with one spoke per host, refreshes that picture and the host table from the same JSON every five
 * seconds, and opens one Agent Card in the panel on a click or a table selection.
 *
 * No inline script and no inline style: the Content-Security-Policy is default-src 'self'. Everything the
 * page shows comes from the gateway, and a value it does not report stays "unknown" - this file never
 * invents one. Nothing here changes gateway state: the only requests are GETs of the routes the handler
 * already serves.
 */
(function () {
  'use strict';

  var REFRESH_MS = 5000;
  var CARD_TARGET = 'card-panel';

  /* htmx does not swap a non-2xx answer by default. An unknown host's card is a 404 that carries the
     "card" partial with Error set, so it is turned into a normal swap instead of being discarded. */
  document.addEventListener('htmx:beforeSwap', function (event) {
    if (event.detail && event.detail.xhr && event.detail.xhr.status === 404) {
      event.detail.shouldSwap = true;
      event.detail.isError = false;
    }
  });

  var container = document.getElementById('network-graph');
  if (!container || typeof window.cytoscape !== 'function') {
    return;
  }

  var graphURL = container.getAttribute('data-graph-url');
  var cardPrefix = container.getAttribute('data-card-prefix') || '';
  var panel = document.getElementById(CARD_TARGET);
  var table = document.getElementById('host-table');
  var seenToggle = document.getElementById('messages-seen');

  var state = {
    document: null,
    loading: false,
    selected: '',
    seenOn: false
  };

  /* ---- graph ------------------------------------------------------------------------------- */

  var cy = window.cytoscape({
    container: container,
    elements: [],
    style: [
      {
        selector: 'node',
        style: {
          label: 'data(label)',
          'font-size': 12,
          color: '#2a2420',
          'text-valign': 'bottom',
          'text-margin-y': 6,
          'background-color': '#e1ede8',
          'border-width': 2,
          'border-color': '#3f6a5e',
          width: 30,
          height: 30
        }
      },
      {
        selector: 'node.gateway',
        style: {
          shape: 'round-rectangle',
          width: 74,
          height: 34,
          label: 'Gateway',
          'font-weight': 'bold',
          'text-valign': 'center',
          'text-margin-y': 0,
          'background-color': '#f7e3d8',
          'border-color': '#b4552f',
          'border-width': 3
        }
      },
      /* Connection is the ring style and the word in the table; the card state is the fill tint. */
      { selector: 'node.host.notlive', style: { 'border-style': 'dashed', 'border-color': '#8a6a2f' } },
      { selector: 'node.host.neverseen', style: { 'border-style': 'dotted', 'border-color': '#9a8c80' } },
      { selector: 'node.host.card-fresh', style: { 'background-color': '#e1ede8' } },
      { selector: 'node.host.card-stale', style: { 'background-color': '#f4ead4' } },
      { selector: 'node.host.card-offline', style: { 'background-color': '#f5f0e6' } },
      { selector: 'node.host.card-none', style: { 'background-color': '#faf6ee' } },
      { selector: 'node.host.card-rejected', style: { 'background-color': '#f6e2d8', 'border-color': '#b24a3d' } },
      { selector: 'node.host.card-unknown', style: { 'background-color': '#f5f0e6', 'border-style': 'dotted' } },
      { selector: 'node.host.selected', style: { 'border-width': 4, 'border-color': '#5b7083' } },

      { selector: 'edge', style: { width: 2, 'line-color': '#8a8a8a', 'curve-style': 'bezier', 'target-arrow-shape': 'none' } },
      { selector: 'edge.spoke.live', style: { 'line-color': '#3f6a5e', 'line-style': 'solid' } },
      { selector: 'edge.spoke.notlive', style: { 'line-color': '#8a6a2f', 'line-style': 'dashed' } },
      { selector: 'edge.spoke.neverseen', style: { 'line-color': '#9a8c80', 'line-style': 'dotted' } },

      /* The messages_seen overlay: sends, never deliveries. Hidden unless the owner turns it on. */
      {
        selector: 'edge.seen',
        style: {
          display: 'none',
          label: 'data(label)',
          'font-size': 10,
          color: '#5b7083',
          'line-color': '#5b7083',
          'line-style': 'dashed',
          width: 1.5,
          'curve-style': 'bezier',
          opacity: 0.8
        }
      },
    ],
    layout: { name: 'preset' },
    wheelSensitivity: 0.15,
    minZoom: 0.2,
    maxZoom: 3
  });

  /* Positions: the gateway at the centre, one ring of hosts around it. Re-run by "Reset layout". */
  function positionNodes(fit) {
    var width = container.clientWidth || 640;
    var height = container.clientHeight || 440;
    var cx = width / 2;
    var cyPoint = height / 2;
    var hosts = cy.nodes('.host');
    var count = hosts.length;
    var radius = Math.max(90, Math.min(cx, cyPoint) - 60);

    cy.getElementById('gateway').position({ x: cx, y: cyPoint });
    hosts.forEach(function (node, index) {
      var angle = (Math.PI * 2 * index) / Math.max(count, 1) - Math.PI / 2;
      node.position({ x: cx + radius * Math.cos(angle), y: cyPoint + radius * Math.sin(angle) });
    });
    if (fit !== false && cy.elements().length) {
      cy.fit(cy.elements(), 40);
    }
  }

  /* ---- data -------------------------------------------------------------------------------- */

  function hostNameFromNodeID(id) {
    return id.indexOf('host:') === 0 ? id.slice(5) : '';
  }

  function setSelected(name) {
    state.selected = name;
    cy.nodes('.host').forEach(function (node) {
      node.toggleClass('selected', !!name && hostNameFromNodeID(node.id()) === name);
    });
    if (table) {
      Array.prototype.forEach.call(table.querySelectorAll('tbody tr'), function (row) {
        var isSelected = row.getAttribute('data-host') === name;
        if (isSelected) {
          row.setAttribute('aria-selected', 'true');
        } else {
          row.removeAttribute('aria-selected');
        }
      });
    }
  }

  function openCard(name) {
    if (!name || !panel || !window.htmx) {
      return;
    }
    setSelected(name);
    panel.setAttribute('data-open', 'true');
    window.htmx.ajax('GET', cardPrefix + encodeURIComponent(name) + '/card', {
      target: '#' + CARD_TARGET,
      swap: 'innerHTML'
    });
  }

  /* Rebuild the accessible table from the same document the graph uses, so the two cannot disagree. */
  function rebuildTable(elements) {
    if (!table) {
      return;
    }
    var body = table.querySelector('tbody');
    if (!body) {
      return;
    }
    var focusedHost = '';
    var focusedControl = document.activeElement;
    if (focusedControl && table.contains(focusedControl)) {
      var focusedRow = focusedControl.closest ? focusedControl.closest('tr[data-host]') : null;
      if (focusedRow && focusedControl.classList.contains('host-link')) {
        focusedHost = focusedRow.getAttribute('data-host') || '';
      }
    }
    while (body.firstChild) {
      body.removeChild(body.firstChild);
    }

    var hostCount = 0;
    elements.forEach(function (element) {
      if ((element.classes || '').indexOf('host ') !== 0) {
        return;
      }
      hostCount += 1;
      var data = element.data || {};
      var row = document.createElement('tr');
      row.setAttribute('data-host', data.label || '');

      var head = document.createElement('th');
      head.setAttribute('scope', 'row');
      head.textContent = data.label || 'unknown';
      row.appendChild(head);

      var connection = document.createElement('td');
      var connectionLabel = data.connectionLabel || 'unknown';
      var connectionClass = connectionLabel === 'live' ? 'live' : (connectionLabel === 'never seen' ? 'neverseen' : 'notlive');
      var connectionChip = document.createElement('span');
      connectionChip.className = 'conn conn-' + connectionClass;
      var connectionMark = document.createElement('span');
      connectionMark.className = 'conn-mark';
      connectionMark.setAttribute('aria-hidden', 'true');
      connectionChip.appendChild(connectionMark);
      connectionChip.appendChild(document.createTextNode(connectionLabel));
      connection.appendChild(connectionChip);
      row.appendChild(connection);

      var card = document.createElement('td');
      var chip = document.createElement('span');
      chip.className = 'chip chip-' + (data.cardState || 'unknown');
      var mark = document.createElement('span');
      mark.className = 'chip-mark';
      mark.setAttribute('aria-hidden', 'true');
      chip.appendChild(mark);
      chip.appendChild(document.createTextNode(data.cardState || 'unknown'));
      card.appendChild(chip);
      if (data.cardAge) {
        var age = document.createElement('span');
        age.className = 'age';
        age.textContent = ' ' + data.cardAge;
        card.appendChild(age);
      }
      if (data.cardSource) {
        var source = document.createElement('span');
        source.className = 'source';
        source.textContent = ' ' + data.cardSource;
        card.appendChild(source);
      }
      row.appendChild(card);

      var sessions = document.createElement('td');
      sessions.textContent = typeof data.sessionCount === 'number' ? String(data.sessionCount) : 'unknown';
      row.appendChild(sessions);

      var action = document.createElement('td');
      var link = document.createElement('a');
      link.className = 'host-link';
      link.href = cardPrefix + encodeURIComponent(data.label || '') + '/card';
      /* htmx attributes are set here, after load, so htmx processes them through its own API. */
      link.setAttribute('hx-get', link.href);
      link.setAttribute('hx-target', '#' + CARD_TARGET);
      link.setAttribute('hx-swap', 'innerHTML');
      link.textContent = 'Read the Agent Card';
      action.appendChild(link);
      row.appendChild(action);

      row.addEventListener('click', function (event) {
        if (event.target && event.target.tagName === 'A') {
          return; /* the anchor's own htmx request handles it */
        }
        openCard(data.label || '');
      });

      body.appendChild(row);
      if (data.label && data.label === state.selected) {
        row.setAttribute('aria-selected', 'true');
      }
    });

    if (hostCount === 0) {
      var emptyRow = document.createElement('tr');
      emptyRow.className = 'empty-row';
      var emptyCell = document.createElement('td');
      emptyCell.colSpan = 5;
      emptyCell.textContent = 'No paired hosts are known to this gateway.';
      emptyRow.appendChild(emptyCell);
      body.appendChild(emptyRow);
    }

    if (focusedHost) {
      Array.prototype.some.call(body.querySelectorAll('tr[data-host]'), function (row) {
        if (row.getAttribute('data-host') !== focusedHost) {
          return false;
        }
        var link = row.querySelector('.host-link');
        if (link && typeof link.focus === 'function') {
          link.focus({ preventScroll: true });
        }
        return true;
      });
    }

    if (window.htmx && typeof window.htmx.process === 'function') {
      window.htmx.process(table);
    }
  }

  function applyDocument(doc) {
    if (!doc || !Array.isArray(doc.elements)) {
      return;
    }
    var hadDocument = !!state.document;
    var previousZoom = cy.zoom();
    var previousPan = cy.pan();
    state.document = doc;

    cy.elements().remove();
    var elements = doc.elements.map(function (element) {
      if ((element.classes || '').split(/\s+/).indexOf('seen') === -1) {
        return element;
      }
      var data = Object.assign({}, element.data || {});
      data.label = String(data.count || 0) + ' sends; ' + String(data.hostStored || 0) + ' host-stored';
      return Object.assign({}, element, { data: data });
    });
    cy.add(elements);
    positionNodes(!hadDocument);
    if (hadDocument) {
      cy.zoom(previousZoom);
      cy.pan(previousPan);
    }
    applySeenOverlay();
    if (state.selected && cy.getElementById('host:' + state.selected).length) {
      setSelected(state.selected);
    } else if (state.selected) {
      setSelected('');
      if (panel) {
        panel.removeAttribute('data-open');
        var empty = document.createElement('p');
        empty.className = 'empty';
        empty.textContent = 'Choose a host in the graph, or a row in the table below, to read its Agent Card.';
        panel.replaceChildren(empty);
      }
    }
    rebuildTable(elements);

    var graphStatus = document.getElementById('graph-refresh-status');
    if (graphStatus) {
      graphStatus.textContent = '';
    }
    var graphUpdatedAt = document.getElementById('graph-updated-at');
    if (graphUpdatedAt && doc.generated_at) {
      graphUpdatedAt.textContent = doc.generated_at;
      graphUpdatedAt.setAttribute('datetime', doc.generated_at);
    }
    var graphNotes = document.getElementById('graph-notes');
    if (graphNotes) {
      graphNotes.replaceChildren();
      if (doc.truncated) {
        var truncated = document.createElement('p');
        truncated.className = 'note';
        truncated.textContent = 'The graph was truncated to fit its size bound; the table below is complete.';
        graphNotes.appendChild(truncated);
      }
      if (doc.edges_partial) {
        var partial = document.createElement('p');
        partial.className = 'note';
        partial.textContent = 'The messages-seen edges are partial for this snapshot.';
        graphNotes.appendChild(partial);
      }
    }
  }

  function refresh() {
    if (state.loading || !graphURL) {
      return;
    }
    state.loading = true;
    window.fetch(graphURL, { headers: { Accept: 'application/json' }, cache: 'no-store' })
      .then(function (response) {
        if (!response.ok) {
          throw new Error('graph request failed');
        }
        return response.json();
      })
      .then(applyDocument)
      .catch(function () {
        /* A failed refresh leaves the last good picture and the table in place; the status strip shows
           the gateway's own view on its own request. Nothing is invented to fill the gap. */
        var graphStatus = document.getElementById('graph-refresh-status');
        if (graphStatus) {
          graphStatus.textContent = 'Could not refresh the graph; showing the last received snapshot.';
        }
      })
      .then(function () {
        state.loading = false;
      });
  }

  /* ---- messages seen overlay (OFF by default) ----------------------------------------------- */

  function applySeenOverlay() {
    cy.edges('.seen').style('display', state.seenOn ? 'element' : 'none');
  }

  if (seenToggle) {
    seenToggle.checked = false;
    seenToggle.addEventListener('change', function () {
      state.seenOn = !!seenToggle.checked;
      applySeenOverlay();
    });
  }

  /* ---- toolbar ----------------------------------------------------------------------------- */

  document.addEventListener('click', function (event) {
    var button = event.target && event.target.closest ? event.target.closest('[data-graph-action]') : null;
    if (!button) {
      return;
    }
    var action = button.getAttribute('data-graph-action');
    if (action === 'zoom-in') {
      cy.zoom({ level: Math.min(cy.zoom() * 1.2, cy.maxZoom()), renderedPosition: { x: cy.width() / 2, y: cy.height() / 2 } });
    } else if (action === 'zoom-out') {
      cy.zoom({ level: Math.max(cy.zoom() / 1.2, cy.minZoom()), renderedPosition: { x: cy.width() / 2, y: cy.height() / 2 } });
    } else if (action === 'fit') {
      cy.fit(cy.elements(), 40);
    } else if (action === 'reset') {
      positionNodes(true);
    }
  });

  /* ---- selection ---------------------------------------------------------------------------- */

  cy.on('tap', 'node.host', function (event) {
    openCard(hostNameFromNodeID(event.target.id()));
  });

  cy.on('tap', function (event) {
    if (event.target === cy) {
      setSelected('');
      if (panel) {
        panel.removeAttribute('data-open');
        var empty = document.createElement('p');
        empty.className = 'empty';
        empty.textContent = 'Choose a host in the graph, or a row in the table below, to read its Agent Card.';
        panel.replaceChildren(empty);
      }
    }
  });

  /* Enter on a focused table link is the anchor's own activation; a click on the row does the rest. */

  /* ---- start -------------------------------------------------------------------------------- */

  refresh();
  window.setInterval(refresh, REFRESH_MS);
  window.addEventListener('resize', function () {
    if (cy.elements().length) {
      var previousZoom = cy.zoom();
      var previousPan = cy.pan();
      positionNodes(false);
      cy.zoom(previousZoom);
      cy.pan(previousPan);
    }
  });
}());
