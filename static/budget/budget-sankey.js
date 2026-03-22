// Budget Sankey diagram with drill-down into service details.
// Click-to-lock: click a node/link to pin its detail; click background or Escape to unlock.

(function () {
  'use strict';
  const tc = ThemeColors();

  const container = document.getElementById('sankey-chart');
  const detailContainer = document.getElementById('sankey-detail-chart');
  const slider = document.getElementById('sankey-slider');
  const dataEl = document.getElementById('sankey-data');
  const serviceEl = document.getElementById('service-details');
  if (!container || !dataEl) return;

  let data, serviceDetails, drillable;
  const detailPanel = document.getElementById('sankey-detail');
  const subtitleEl = document.querySelector('[data-section="subtitle"]');
  const defaultSubtitle = subtitleEl ? subtitleEl.textContent : '';
  const headingEl = container.closest('article') && container.closest('article').querySelector('h2');
  const defaultHeading = headingEl ? headingEl.textContent : '';
  let currentView, locked, lockedName, activeSankeyData, lastDrilledService;

  // Persistent banner elements — created once, updated in place for smooth transitions
  let bannerEl, bannerSwatch, bannerTitle, bodyEl, backEl;
  let unhoverTimer = null;
  const UNHOVER_DELAY = 300; // ms before reverting to default on mouseleave

  // URL hash ↔ service name mapping
  function toSlug(name) { return name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, ''); }
  let slugToName = {}; // built after serviceDetails is set

  function init(sankeyData, svcDetails) {
    data = sankeyData || {};
    serviceDetails = svcDetails || {};
    drillable = {};
    Object.keys(serviceDetails).forEach(function (name) { drillable[name] = true; });
    currentView = 'overview';
    locked = false;
    lockedName = null;
    activeSankeyData = data;

    container.innerHTML = '';
    if (detailContainer) detailContainer.innerHTML = '';
    if (slider) slider.classList.remove('slid');

    // Build INCOME / EXPENSE column header above the flex row (totals from server)
    var flowView = container.closest('.budget-flow-view');
    if (flowView) {
      var oldHeader = flowView.querySelector('.sankey-column-header');
      if (oldHeader) oldHeader.remove();
      var header = document.createElement('div');
      header.className = 'sankey-column-header';
      header.innerHTML = '<span>INCOME  ' + (data.incomeTotal || '') + '</span><span>EXPENSE  ' + (data.expenseTotal || '') + '</span>';
      flowView.insertBefore(header, flowView.firstChild);
    }

    renderSankey(container, data, {
      ariaLabel: 'Budget flow diagram showing how property taxes are allocated to city services',
      isDrillable: function (name) { return drillable[name]; },
      onNodeClick: function (name) {
        if (drillable[name]) {
          slideTo(name);
        } else {
          lockDetail(data, name);
        }
      }
    });

    // Build slug lookup
    slugToName = {};
    Object.keys(serviceDetails).forEach(function (name) { slugToName[toSlug(name)] = name; });

    if (detailPanel) {
      // Build persistent panel structure once
      detailPanel.innerHTML = '';
      detailPanel.hidden = false;

      backEl = document.createElement('button');
      backEl.className = 'sankey-back';
      backEl.textContent = '← City Overview';
      backEl.addEventListener('click', slideBack);
      backEl.hidden = true;
      detailPanel.appendChild(backEl);

      bannerEl = document.createElement('div');
      bannerEl.className = 'sankey-detail-banner sankey-detail-banner-hero';
      bannerSwatch = document.createElement('span');
      bannerSwatch.className = 'sankey-banner-swatch';
      bannerTitle = document.createElement('strong');
      bannerEl.appendChild(bannerSwatch);
      bannerEl.appendChild(bannerTitle);
      detailPanel.appendChild(bannerEl);

      bodyEl = document.createElement('div');
      bodyEl.className = 'sankey-detail-body';
      detailPanel.appendChild(bodyEl);

      showDefault(data);
    }

    // Auto-drill from URL hash
    const hash = location.hash.replace('#', '');
    if (hash && slugToName[hash]) {
      slideTo(slugToName[hash]);
    }
  }

  // ── Render a Sankey into a container ──
  function renderSankey(target, sankeyData, opts) {
    target.innerHTML = '';
    const w = target.clientWidth || 700;
    const h = Math.max(400, sankeyData.nodes.length * 52);

    const svg = d3.select(target)
      .append('svg')
      .attr('viewBox', [0, 0, w, h])
      .attr('role', 'img')
      .attr('aria-label', opts.ariaLabel || 'Budget flow diagram');

    const layout = d3.sankey()
      .nodeWidth(18)
      .nodePadding(24)
      .nodeAlign(d3.sankeyJustify)
      .iterations(20)
      .extent([[1, 10], [w - 1, h - 10]]);

    const graph = layout({
      nodes: sankeyData.nodes.map(function (d) { return Object.assign({}, d); }),
      links: sankeyData.links.map(function (d) { return Object.assign({}, d); })
    });

    // Link gradients: source color → target color
    const defs = svg.append('defs');
    graph.links.forEach(function (d, i) {
      const srcDet = sankeyData.details[d.source.name];
      const tgtDet = sankeyData.details[d.target.name];
      const srcColor = (srcDet && srcDet.color) || tc.statusMuted;
      const tgtColor = (tgtDet && tgtDet.color) || tc.statusMuted;
      const grad = defs.append('linearGradient')
        .attr('id', 'link-grad-' + i)
        .attr('gradientUnits', 'userSpaceOnUse')
        .attr('x1', d.source.x1).attr('x2', d.target.x0);
      grad.append('stop').attr('offset', '0%').attr('stop-color', srcColor);
      grad.append('stop').attr('offset', '100%').attr('stop-color', tgtColor);
    });

    // Links
    svg.append('g').attr('fill', 'none')
      .selectAll('path')
      .data(graph.links)
      .join('path')
      .attr('class', 'sankey-link')
      .attr('d', d3.sankeyLinkHorizontal())
      .attr('stroke', function (d, i) { return 'url(#link-grad-' + i + ')'; })
      .attr('stroke-opacity', 0.35)
      .attr('stroke-width', function (d) { return Math.max(1, d.width); });

    // Store visible link paths for hit-area hover highlighting
    const visibleLinks = svg.selectAll('.sankey-link');

    // Invisible wider hit area for thin links
    svg.append('g').attr('fill', 'none')
      .selectAll('path')
      .data(graph.links)
      .join('path')
      .attr('d', d3.sankeyLinkHorizontal())
      .attr('stroke', 'transparent')
      .attr('stroke-width', function (d) { return Math.max(12, d.width); })
      .style('cursor', 'pointer')
      .on('mouseenter', function (event, d) {
        if (locked) return;
        clearTimeout(unhoverTimer);
        visibleLinks.filter(function (vd) { return vd === d; }).attr('stroke-opacity', 0.6);
        showLinkDetail(sankeyData, d);
      })
      .on('mouseleave', function (event, d) {
        if (locked) return;
        visibleLinks.filter(function (vd) { return vd === d; }).attr('stroke-opacity', 0.35);
        unhoverTimer = setTimeout(function () { showDefault(sankeyData); }, UNHOVER_DELAY);
      })
      .on('click', function (event, d) {
        event.stopPropagation();
        lockLinkDetail(sankeyData, d);
      })
      .append('title')
      .text(function (d) { return d.source.name + ' → ' + d.target.name + ': $' + d.value.toFixed(1) + 'M'; });

    // Nodes
    const node = svg.append('g')
      .selectAll('g')
      .data(graph.nodes)
      .join('g')
      .attr('class', function (d) {
        let cls = 'sankey-node';
        if (opts.isDrillable && opts.isDrillable(d.name)) cls += ' drillable';
        if (opts.isBackNode && opts.isBackNode(d.name)) cls += ' back-node';
        return cls;
      })
      .attr('data-name', function (d) { return d.name; });

    node.append('rect')
      .attr('x', function (d) { return d.x0; })
      .attr('y', function (d) { return d.y0; })
      .attr('height', function (d) { return d.y1 - d.y0; })
      .attr('width', function (d) { return d.x1 - d.x0; })
      .attr('fill', function (d) {
        var det = sankeyData.details[d.name];
        if (det && det.color) return det.color;
        // Fall back to overview data for funding source nodes in drill-down
        if (data && data.details && data.details[d.name] && data.details[d.name].color) {
          return data.details[d.name].color;
        }
        return tc.statusMuted;
      })
      .attr('rx', 3)
      .on('mouseenter', function (event, d) {
        if (locked) return;
        clearTimeout(unhoverTimer);
        showDetail(sankeyData, d.name);
      })
      .on('mouseleave', function () {
        if (locked) return;
        unhoverTimer = setTimeout(function () { showDefault(sankeyData); }, UNHOVER_DELAY);
      })
      .on('click', function (event, d) {
        event.stopPropagation();
        if (opts.onNodeClick) opts.onNodeClick(d.name);
      })
      .append('title')
      .text(function (d) { return d.name + ': $' + d.value.toFixed(1) + 'M'; });

    // Labels
    node.append('text')
      .attr('class', 'sankey-label')
      .attr('x', function (d) { return d.x0 < w / 2 ? d.x1 + 6 : d.x0 - 6; })
      .attr('y', function (d) { return (d.y1 + d.y0) / 2 - 5; })
      .attr('text-anchor', function (d) { return d.x0 < w / 2 ? 'start' : 'end'; })
      .attr('font-size', '11px').attr('font-weight', '500').attr('fill', tc.textColor)
      .text(function (d) { return d.name; });

    node.append('text')
      .attr('class', 'sankey-value')
      .attr('x', function (d) { return d.x0 < w / 2 ? d.x1 + 6 : d.x0 - 6; })
      .attr('y', function (d) { return (d.y1 + d.y0) / 2 + 9; })
      .attr('text-anchor', function (d) { return d.x0 < w / 2 ? 'start' : 'end'; })
      .attr('font-size', '10px').attr('fill', tc.statusMuted)
      .text(function (d) { return '$' + d.value.toFixed(1) + 'M'; });

    // Click background to unlock
    svg.on('click', function () { unlock(); });
  }

  // ── Lock / unlock ──
  function lockDetail(sankeyData, name) {
    // If already locked on this name, unlock
    if (locked && lockedName === name) {
      unlock();
      return;
    }
    locked = true;
    lockedName = name;
    showDetail(sankeyData, name);
  }

  function lockLinkDetail(sankeyData, link) {
    const key = link.source.name + '|' + link.target.name;
    if (locked && lockedName === key) {
      unlock();
      return;
    }
    locked = true;
    lockedName = key;
    showLinkDetail(sankeyData, link);
  }

  function showLinkDetail(sankeyData, link) {
    if (!detailPanel) return;
    const src = link.source.name;
    const tgt = link.target.name;
    const det = sankeyData.details[tgt];
    const color = det ? det.color : tc.statusMuted;
    const key = src + '|' + tgt;
    const desc = (sankeyData.linkDetails || {})[key] || '';

    updateBanner(src + ' → ' + tgt, color);

    let html = '';
    if (locked) html += '<div class="sankey-detail-locked">Selected</div>';
    html += '<div class="sankey-detail-amount">$' + link.value.toFixed(1) + 'M</div>';
    if (det && det.total) {
      const pct = Math.round(link.value / det.total * 100);
      html += '<div class="sankey-detail-pct">' + pct + '% of ' + tgt + '\'s $' + det.total.toFixed(1) + 'M budget</div>';
    }
    if (desc) html += '<p>' + desc + '</p>';
    html += sourceLink(sankeyData);
    updateBody(html);
  }

  function unlock() {
    locked = false;
    lockedName = null;
    showDefault(activeSankeyData);
  }

  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && locked) unlock();
  });

  // ── Detail panel ──
  function updateBody(html, cb) {
    if (!bodyEl) return;
    bodyEl.style.opacity = '0';
    setTimeout(function () {
      bodyEl.innerHTML = html;
      bodyEl.style.opacity = '1';
      if (cb) cb();
    }, 120);
  }

  function updateBanner(title, color) {
    if (!bannerEl) return;
    bannerTitle.textContent = title;
    if (color) {
      bannerSwatch.style.setProperty('--swatch-color', color);
      bannerSwatch.style.display = '';
    } else {
      bannerSwatch.style.setProperty('--swatch-color', 'transparent');
      bannerSwatch.style.display = 'none';
    }
    backEl.hidden = currentView === 'overview';
  }

  function sourceLink(sankeyData) {
    const label = sankeyData.sourceNote;
    if (!label) return '';
    const url = sankeyData.source || sankeyData.sourceURL;
    if (url) {
      return '<div class="sankey-detail-source"><a href="' + url + '" target="_blank" rel="noopener">' + label + '</a></div>';
    }
    return '<div class="sankey-detail-source">' + label + '</div>';
  }

  function showDefault(sankeyData) {
    if (!detailPanel) return;
    const label = sankeyData.taxLevy || ('$' + (sankeyData.total || 0).toFixed(1) + 'M');
    const title = sankeyData.title || ('Revenue · ' + label);
    updateBanner(title, null);

    let html = '<ul class="sankey-detail-list">';
    const seen = {};
    const targets = [];
    sankeyData.links.forEach(function (link) {
      const name = sankeyData.nodes[link.target].name;
      if (!seen[name] && sankeyData.details[name]) {
        seen[name] = true;
        targets.push(name);
      }
    });
    targets.sort(function (a, b) { return sankeyData.details[a].total - sankeyData.details[b].total; });
    targets.forEach(function (name) {
      const d = sankeyData.details[name];
      html += '<li><span class="sankey-detail-swatch" style="background:' + d.color + '"></span>' +
        name +
        ' <span class="sankey-detail-li-val">$' + d.total.toFixed(1) + 'M</span></li>';
    });
    html += '</ul>';
    html += sourceLink(sankeyData);
    updateBody(html);
  }

  function showDetail(sankeyData, name) {
    const d = sankeyData.details[name];
    if (!d || !detailPanel) return;
    updateBanner(name, d.color);

    let html = '';
    if (locked) {
      html += '<div class="sankey-detail-locked">Selected</div>';
    }
    if (d.total) {
      html += '<div class="sankey-detail-amount">$' + d.total.toFixed(1) + 'M</div>';
      if (d.percent) {
        html += '<div class="sankey-detail-pct">' + d.percent + '% of levy';
        if (d.change) html += ' · ' + d.change + ' YoY';
        html += '</div>';
      }
    }
    if (d.description) html += '<p>' + d.description + '</p>';
    if (currentView === 'overview' && drillable[name]) {
      html += '<p class="sankey-detail-drill-hint" data-drill="' + name + '">' + (isTouch() ? 'Tap' : 'Click') + ' to see detailed breakdown →</p>';
    }
    html += sourceLink(sankeyData);
    updateBody(html, function () {
      const drillHint = bodyEl.querySelector('[data-drill]');
      if (drillHint) {
        drillHint.style.cursor = 'pointer';
        drillHint.addEventListener('click', function () { slideTo(drillHint.dataset.drill); });
      }
    });
  }

  // ── Slide to service detail ──
  // Measure and cache the overview SVG height for smooth transitions
  let overviewHeight = 0;
  function cacheOverviewHeight() {
    const svg = container.querySelector('svg');
    if (svg) overviewHeight = svg.getBoundingClientRect().height;
  }

  function transitionViewportHeight(targetContainer) {
    const viewport = document.querySelector('.sankey-viewport');
    if (!viewport) return;
    const svg = targetContainer.querySelector('svg');
    if (!svg) return;
    // Let the SVG render at natural size, then transition
    requestAnimationFrame(function () {
      const h = svg.getBoundingClientRect().height;
      viewport.style.height = viewport.offsetHeight + 'px'; // pin current
      requestAnimationFrame(function () {
        viewport.style.transition = 'height 0.5s ease';
        viewport.style.height = h + 'px';
      });
    });
  }

  function clearViewportHeight() {
    const viewport = document.querySelector('.sankey-viewport');
    if (!viewport) return;
    if (overviewHeight) {
      viewport.style.height = viewport.offsetHeight + 'px';
      requestAnimationFrame(function () {
        viewport.style.transition = 'height 0.5s ease';
        viewport.style.height = overviewHeight + 'px';
        // After transition, remove fixed height so it's responsive again
        setTimeout(function () { viewport.style.height = ''; viewport.style.transition = ''; }, 550);
      });
    } else {
      viewport.style.height = '';
      viewport.style.transition = '';
    }
  }

  function updateColumnHeader(sankeyData) {
    var flowView = container.closest('.budget-flow-view');
    if (!flowView) return;
    var hdr = flowView.querySelector('.sankey-column-header');
    if (hdr) {
      var spans = hdr.querySelectorAll('span');
      spans[0].textContent = 'INCOME  ' + (sankeyData.incomeTotal || '');
      spans[1].textContent = 'EXPENSE  ' + (sankeyData.expenseTotal || '');
    }
  }

  function slideTo(serviceName) {
    const svc = serviceDetails[serviceName];
    if (!svc) return;
    locked = false;
    lockedName = null;
    currentView = serviceName;
    lastDrilledService = serviceName;
    activeSankeyData = svc;
    cacheOverviewHeight();
    updateColumnHeader(svc);

    const targetSet = {};
    svc.links.forEach(function (l) { targetSet[svc.nodes[l.target].name] = true; });

    renderSankey(detailContainer, svc, {
      ariaLabel: svc.title + ' budget breakdown',
      leftLabel: 'FUNDING',
      rightLabel: serviceName.toUpperCase(),
      isBackNode: function (name) { return !targetSet[name]; },
      onNodeClick: function (name) {
        if (!targetSet[name]) {
          slideBack();
        } else {
          lockDetail(svc, name);
        }
      }
    });

    slider.classList.add('slid');
    transitionViewportHeight(detailContainer);
    var scrollTarget = container.closest('article') || document.querySelector('.sankey-viewport');
    if (scrollTarget) scrollTarget.scrollIntoView({ behavior: 'smooth', block: 'start' });
    if (headingEl) {
      headingEl.innerHTML = '<a class="sankey-breadcrumb-back" href="#">' + defaultHeading + '</a> / ' + serviceName;
      headingEl.querySelector('.sankey-breadcrumb-back').addEventListener('click', function (e) { e.preventDefault(); slideBack(); });
    }
    if (subtitleEl) {
      const det = data.details[serviceName];
      subtitleEl.textContent = (det && det.description) || serviceName;
    }
    history.replaceState(null, '', '#' + toSlug(serviceName));
    setTimeout(function () { showDefault(svc); }, 500);
  }

  function slideBack() {
    locked = false;
    lockedName = null;
    currentView = 'overview';
    activeSankeyData = data;
    updateColumnHeader(data);
    // Remember where the viewport is on screen before the transition
    var viewport = document.querySelector('.sankey-viewport');
    var vpTop = viewport ? viewport.getBoundingClientRect().top : 0;

    slider.classList.remove('slid');
    if (headingEl) headingEl.textContent = defaultHeading;
    if (subtitleEl) subtitleEl.textContent = defaultSubtitle;
    history.replaceState(null, '', location.pathname + location.search);

    // Read the node position from the overview SVG (still in DOM, just off-screen left).
    // Calculate where it will be once the slide completes and viewport expands.
    var serviceName = lastDrilledService;
    var node = container ? container.querySelector('.sankey-node[data-name="' + serviceName + '"]') : null;
    if (node && viewport) {
      // Node's Y within the overview SVG (relative to container top)
      var svg = container.querySelector('svg');
      var nodeRect = node.getBoundingClientRect();
      var svgRect = svg ? svg.getBoundingClientRect() : nodeRect;
      var nodeYInSVG = nodeRect.top - svgRect.top + nodeRect.height / 2;
      // Where the viewport top will be after scroll
      vpTop = viewport.getBoundingClientRect().top + window.scrollY;
      var target = vpTop + nodeYInSVG - window.innerHeight / 2;
      // Clamp so the viewport stays well on screen — don't scroll past its top or bottom
      var minScroll = vpTop - window.innerHeight * 0.2;
      var maxScroll = vpTop + (overviewHeight || 600) - window.innerHeight * 0.8;
      target = Math.max(minScroll, Math.min(maxScroll, target));
      window.scrollTo({ top: Math.max(0, target), behavior: 'smooth' });
    }

    // Fire slide + height transition simultaneously with the scroll
    clearViewportHeight();
    setTimeout(function () { showDefault(data); }, 500);
  }

  // ── Initial render ──
  init(JSON.parse(dataEl.textContent), serviceEl ? JSON.parse(serviceEl.textContent) : {});

  // Style drillable and back nodes
  const style = document.createElement('style');
  style.textContent =
    '.sankey-node.drillable rect { cursor: zoom-in; } ' +
    '.sankey-node.drillable rect:hover { filter: brightness(1.1); stroke: var(--thunder-900); stroke-width: 1.5; } ' +
    '.sankey-node.back-node rect { cursor: zoom-out; } ' +
    '.sankey-node.back-node rect:hover { filter: brightness(1.1); stroke: var(--thunder-900); stroke-width: 1.5; }';
  document.head.appendChild(style);

  // Expose reinit for client-side year switching
  window.renderBudgetSankey = function (sankeyData, svcDetails) {
    init(sankeyData, svcDetails);
  };

})();
