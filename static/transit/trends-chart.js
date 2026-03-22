// Route comparison chart and KPI card interaction.
(function () {
  'use strict';
  var tc = ThemeColors();

  var KPI_LABELS = {
    otp: 'On-Time Performance',
    cancel: 'Cancellation Rate',
    cv: 'Headway Covariance',
    notice: 'Cancel Notice',
    ewt: 'Excess Wait Time',
    wait: 'Worst-Stop Wait'
  };

  var MONTHS = ['', 'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

  function readRouteColors() {
    var routeColors = {};
    var el = document.getElementById('route-meta');
    if (!el) return routeColors;
    try {
      var routes = JSON.parse(el.textContent || '[]') || [];
      for (var i = 0; i < routes.length; i++) {
        var route = routes[i];
        if (route.route_id && route.color) routeColors[route.route_id] = route.color;
      }
    } catch (_e) { /* invalid JSON */ }
    return routeColors;
  }

  // Read active KPI from server-rendered data attribute, default to 'otp'.
  var reportKPI = (function() {
    var grid = document.querySelector('.kpi-grid[data-active-kpi]');
    return grid ? grid.getAttribute('data-active-kpi') : 'otp';
  })();

  // Cancel log data — embedded by the server via @templ.JSONScript.
  function loadCancelledTrips() {
    var el = document.getElementById('cancelled-trips');
    if (!el) return [];
    try { return JSON.parse(el.textContent || '[]') || []; }
    catch (e) { return []; }
  }

  function init() {
    cancelLogData = loadCancelledTrips();
    initReportCardChart();
    initRouteCompareChart();
    if (reportKPI === 'cancel') toggleCancelPanels(true);
  }

  // ==========================================================================
  // KPI CARD SELECTION
  // ==========================================================================

  function initReportCardChart() {
    var cards = document.querySelectorAll('.kpi-card[data-kpi]');
    for (var i = 0; i < cards.length; i++) {
      cards[i].addEventListener('click', function (e) {
        var kpi = e.currentTarget.getAttribute('data-kpi');
        if (kpi) selectReportKPI(kpi);
      });
    }
  }

  function selectReportKPI(kpi) {
    reportKPI = kpi;

    var cards = document.querySelectorAll('.kpi-card[data-kpi]');
    for (var i = 0; i < cards.length; i++) {
      if (cards[i].getAttribute('data-kpi') === kpi) {
        cards[i].classList.add('kpi-active');
      } else {
        cards[i].classList.remove('kpi-active');
      }
    }

    if (window.updateRouteCompare) window.updateRouteCompare(kpi);
    toggleCancelPanels(kpi === 'cancel');
  }

  // ==========================================================================
  // ROUTE COMPARISON BAR CHART
  //
  // Reads embedded chunk data via window.transitChunks.loadChunks() and
  // aggregates per route in the browser. No fetch — the data is on the
  // page from first paint.
  // ==========================================================================

  function buildRouteCompareData() {
    if (!window.transitChunks) return [];
    var chunks = window.transitChunks.loadChunks();
    var routeMeta = {};
    var metaEl = document.getElementById('route-meta');
    if (metaEl) {
      try {
        var routes = JSON.parse(metaEl.textContent || '[]') || [];
        for (var i = 0; i < routes.length; i++) {
          routeMeta[routes[i].route_id] = routes[i];
        }
      } catch (_e) { /* ignore */ }
    }

    var grouped = window.transitChunks.groupByRoute(chunks);
    var out = [];
    grouped.forEach(function(routeChunks, routeID) {
      var meta = routeMeta[routeID] || { short_name: routeID };
      out.push({
        route_id: routeID,
        short_name: meta.short_name || routeID,
        otp:    window.transitChunks.kpi(routeChunks, 'otp',    '') || 0,
        cancel: window.transitChunks.kpi(routeChunks, 'cancel', '') || 0,
        ewt:    window.transitChunks.kpi(routeChunks, 'ewt',    '') || 0,
        cv:     window.transitChunks.kpi(routeChunks, 'cv',     '') || 0,
      });
    });
    return out;
  }

  function initRouteCompareChart() {
    var container = document.getElementById('route-compare-chart');
    if (!container) return;
    var routeColors = readRouteColors();

    var routes = buildRouteCompareData();
    if (!routes.length) return;

    var KPI_TO_COMPARE = { otp: 'otp', cancel: 'cancel', cv: 'cv', ewt: 'ewt', notice: 'cancel', wait: 'ewt' };
    var currentMetric = KPI_TO_COMPARE[reportKPI] || 'otp';

    var COMPARE_METRICS = {
      otp:    { key: 'otp',    label: 'On-Time %',         unit: '%',    lower: false, fmt: function(v) { return v.toFixed(1); } },
      cancel: { key: 'cancel', label: 'Cancellation Rate', unit: '%',    lower: true,  fmt: function(v) { return v.toFixed(1); } },
      cv:     { key: 'cv',     label: 'Headway Covariance', unit: '',    lower: true,  fmt: function(v) { return v.toFixed(2); } },
      ewt:    { key: 'ewt',    label: 'Excess Wait Time',  unit: '',     lower: true,  fmt: function(v) { return v.toFixed(1); } },
    };

    var sorted = routes.slice().sort(function (a, b) {
      var an = parseInt(a.short_name, 10) || 999;
      var bn = parseInt(b.short_name, 10) || 999;
      return an - bn || a.short_name.localeCompare(b.short_name);
    });

    function buildDOM() {
      var html = '<div class="rc-label"></div>';
      html += '<div class="rc-cols">';
      for (var j = 0; j < sorted.length; j++) {
        var r = sorted[j];
        var color = routeColors[r.route_id] || routeColors[r.short_name] || tc.statusMuted;
        html += '<div class="rc-col">';
        html += '<span class="rc-val"></span>';
        html += '<div class="rc-col-track"><div class="rc-col-bar" style="height:0;background:' + color + '"></div></div>';
        html += '<span class="rc-badge" style="color:' + color + '">' + r.short_name + '</span>';
        html += '</div>';
      }
      html += '</div>';
      container.innerHTML = html;
    }

    function updateBars() {
      var m = COMPARE_METRICS[currentMetric];
      if (!m) return;

      var maxVal = 0;
      for (var i = 0; i < sorted.length; i++) {
        if (sorted[i][m.key] > maxVal) maxVal = sorted[i][m.key];
      }
      if (maxVal === 0) maxVal = 1;

      var labelEl = container.querySelector('.rc-label');
      if (labelEl) labelEl.textContent = m.label + (m.lower ? ' \u2014 lower is better' : ' \u2014 higher is better');

      var bars = container.querySelectorAll('.rc-col-bar');
      var vals = container.querySelectorAll('.rc-val');
      for (var k = 0; k < sorted.length; k++) {
        var val = sorted[k][m.key];
        var pct = Math.max(3, (val / maxVal) * 100);
        if (bars[k]) bars[k].style.height = pct.toFixed(0) + '%';
        if (vals[k]) vals[k].textContent = m.fmt(val) + m.unit;
      }

      var titleEl = document.getElementById('route-compare-title');
      if (titleEl) titleEl.textContent = 'Route Comparison — ' + m.label;
    }

    // Build DOM, force layout, then animate to initial values
    buildDOM();
    requestAnimationFrame(function () { updateBars(); });

    window.updateRouteCompare = function (kpi) {
      var mapped = KPI_TO_COMPARE[kpi] || kpi;
      if (COMPARE_METRICS[mapped]) {
        currentMetric = mapped;
        updateBars();
      }
    };
  }

  // ==========================================================================
  // CANCELLED TRIPS LOG (by date)
  // ==========================================================================

  var cancelLogData = null;
  var cancelLogBuilt = false;

  function toggleCancelPanels(show) {
    var el = document.getElementById('cancel-log-container');
    if (el) el.hidden = !show;
    if (show && !cancelLogBuilt) renderCancelLog();
  }

  function renderCancelLog() {
    var container = document.getElementById('cancel-log');
    if (!container || !cancelLogData || !cancelLogData.length) return;

    var routeColors = readRouteColors();
    var routeMeta = {};
    var routeNames = {};
    try {
      var metaEl = document.getElementById('route-meta');
      if (metaEl) {
        var routes = JSON.parse(metaEl.textContent || '[]') || [];
        for (var i = 0; i < routes.length; i++) {
          routeMeta[routes[i].route_id] = routes[i];
          routeNames[routes[i].route_id] = routes[i].short_name;
        }
      }
    } catch (_e) { /* ignore */ }

    // Group by date
    var byDate = {};
    var dates = [];
    for (var d = 0; d < cancelLogData.length; d++) {
      var trip = cancelLogData[d];
      if (!byDate[trip.date]) {
        byDate[trip.date] = [];
        dates.push(trip.date);
      }
      byDate[trip.date].push(trip);
    }

    var DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

    // Build flat row data with sortable values
    var rows = [];
    for (var r = 0; r < cancelLogData.length; r++) {
      var tr = cancelLogData[r];
      var name = routeNames[tr.route_id] || tr.route_id;
      var color = routeColors[tr.route_id] || routeColors[name] || tc.statusMuted;
      var timeRange = tr.start_time + (tr.end_time ? ' – ' + tr.end_time : '');
      var reported = tr.first_seen || '';
      var notice = '';
      var noticeBad = true;
      var noticeSortVal = -9999;
      if (tr.first_seen && tr.start_time) {
        var seenParts = tr.first_seen.split(':');
        var depParts = tr.start_time.split(':');
        var seenMin = parseInt(seenParts[0], 10) * 60 + parseInt(seenParts[1], 10);
        var depMin = parseInt(depParts[0], 10) * 60 + parseInt(depParts[1], 10);
        var diff = depMin - seenMin;
        noticeSortVal = diff;
        var absDiff = Math.abs(diff);
        var dur = absDiff >= 60 ? Math.floor(absDiff / 60) + 'h ' + (absDiff % 60) + 'm' : absDiff + 'm';
        if (diff > 0) {
          notice = dur + ' before';
          noticeBad = diff < 15;
        } else {
          notice = dur + ' after';
        }
      }
      var dt = new Date(tr.date + 'T12:00:00');
      var dayLabel = DAYS[dt.getDay()] + ', ' + MONTHS[dt.getMonth() + 1] + ' ' + dt.getDate();
      rows.push({
        date: tr.date, dayLabel: dayLabel, name: name, color: color,
        timeRange: timeRange, startTime: tr.start_time, reported: reported,
        reportedMin: reported ? parseInt(reported.split(':')[0], 10) * 60 + parseInt(reported.split(':')[1], 10) : 0,
        notice: notice, noticeBad: noticeBad, noticeSortVal: noticeSortVal,
        headsign: tr.headsign || '', routeNum: parseInt(name, 10) || 999
      });
    }

    var SORT_COLS = {
      date:     function (a, b) { return a.date.localeCompare(b.date) || a.startTime.localeCompare(b.startTime); },
      route:    function (a, b) { return (a.routeNum - b.routeNum) || a.name.localeCompare(b.name); },
      time:     function (a, b) { return a.startTime.localeCompare(b.startTime); },
      headsign: function (a, b) { return a.headsign.localeCompare(b.headsign); },
      reported: function (a, b) { return a.reportedMin - b.reportedMin; },
      notice:   function (a, b) { return a.noticeSortVal - b.noticeSortVal; }
    };
    var currentSort = 'date';
    var sortAsc = true;

    function renderTable() {
      var sorted = rows.slice();
      var cmp = SORT_COLS[currentSort] || SORT_COLS.date;
      sorted.sort(function (a, b) { return sortAsc ? cmp(a, b) : cmp(b, a); });

      var groupByDate = currentSort === 'date';
      var tbody = '';
      var lastDate = '';
      for (var r = 0; r < sorted.length; r++) {
        var row = sorted[r];
        var stripe = r % 2 === 1 ? ' cl-stripe' : '';
        var dateCell = '';
        var dateCls = '';
        if (groupByDate) {
          if (row.date !== lastDate) {
            dateCell = row.dayLabel;
            dateCls = ' cl-td-date-first';
            lastDate = row.date;
          }
        } else {
          dateCell = row.dayLabel;
        }
        tbody += '<tr class="' + stripe + '">';
        tbody += '<td class="cl-td-date' + dateCls + '">' + dateCell + '</td>';
        tbody += '<td class="cl-td-route" style="color:' + row.color + '">' + row.name + '</td>';
        tbody += '<td class="cl-td-time">' + row.timeRange + '</td>';
        tbody += '<td class="cl-td-headsign">' + row.headsign + '</td>';
        tbody += '<td class="cl-td-reported">' + row.reported + '</td>';
        tbody += '<td class="cl-td-notice ' + (row.noticeBad ? 'cl-notice-bad' : 'cl-notice-ok') + '">' + row.notice + '</td>';
        tbody += '</tr>';
      }

      var table = container.querySelector('.cl-table');
      if (table) {
        table.querySelector('tbody').innerHTML = tbody;
        // Update sort indicators
        var ths = table.querySelectorAll('th[data-sort]');
        for (var h = 0; h < ths.length; h++) {
          var key = ths[h].getAttribute('data-sort');
          ths[h].classList.toggle('cl-sort-active', key === currentSort);
          ths[h].classList.toggle('cl-sort-desc', key === currentSort && !sortAsc);
        }
      }
    }

    // Initial render with headers
    var html = '<table class="cl-table">';
    html += '<thead><tr>';
    html += '<th data-sort="date" class="cl-th-date cl-sort-active">Date</th>';
    html += '<th data-sort="route" class="cl-th-route">Route</th>';
    html += '<th data-sort="time" class="cl-th-time">Scheduled</th>';
    html += '<th data-sort="headsign" class="cl-th-headsign">Headsign</th>';
    html += '<th data-sort="reported" class="cl-th-reported">Observed At</th>';
    html += '<th data-sort="notice" class="cl-th-notice">Notice</th>';
    html += '</tr></thead>';
    html += '<tbody></tbody></table>';
    html += '<div class="cl-summary">' + cancelLogData.length + ' cancelled trips across ' + dates.length + ' day' + (dates.length !== 1 ? 's' : '') + '</div>';
    container.innerHTML = html;

    // Wire sort clicks
    var ths = container.querySelectorAll('th[data-sort]');
    for (var h = 0; h < ths.length; h++) {
      ths[h].addEventListener('click', function (e) {
        var key = e.currentTarget.getAttribute('data-sort');
        if (currentSort === key) {
          sortAsc = !sortAsc;
        } else {
          currentSort = key;
          sortAsc = true;
        }
        renderTable();
      });
    }

    renderTable();
    cancelLogBuilt = true;
  }

  // ==========================================================================
  // HELPERS
  // ==========================================================================

  // HTML duration: compact decimal hours — "20.4<small>h</small>", "45<small>m</small>"
  window.fmtDurHTML = fmtDurHTML;
  function fmtDurHTML(minutes) {
    var u = '<small class="kpi-unit">';
    var ue = '</small>';
    var h = minutes / 60;
    if (h >= 1) {
      var s = h >= 10 ? Math.round(h).toString() : h.toFixed(1).replace(/\.0$/, '');
      return s + u + 'h' + ue;
    }
    return Math.round(minutes) + u + 'm' + ue;
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
