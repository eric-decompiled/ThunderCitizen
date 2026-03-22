// chunks.js — frontend metrics aggregation, mirroring chunk.KPI in
// internal/transit/chunk/rollup.go. Reads the chunk data the server
// embeds via @templ.JSONScript("transit-chunks", vm.Chunks) and exposes
// a small set of pure helpers for filtering and reaggregating.
//
// Chunk shape (from internal/transit/chunk/chunk.go::ChunkView, JSON tags):
//
//   {
//     route_id, date, band,
//     trips, on_time, scheduled, cancelled, no_notice,
//     headway_n, headway_sum_sec, headway_sum_sq_sec, sched_headway_sec,
//     otp_pct, ewt_min, cv
//   }
//
// Two-track design (raw counts AND pre-computed display values):
//   - Use the raw counts when SUM-ing across multiple chunks — that's
//     trip-weighted exact arithmetic. Aggregating already-rounded
//     percentages would be wrong.
//   - Use the pre-computed otp_pct / ewt_min / cv when rendering ONE
//     chunk directly (per-day cell, drill-down row, etc.) — they come
//     from the same Go formulas the reaggregators use.
//
// Aggregation rules mirror internal/transit/chunk/rollup.go::KPI exactly.

(function() {
  'use strict';

  // ---------------------------------------------------------------------
  // Loader — read the embedded JSON once on first call.
  // ---------------------------------------------------------------------

  var _cached = null;

  function loadChunks() {
    if (_cached !== null) return _cached;
    var el = document.getElementById('transit-chunks');
    if (!el) {
      _cached = [];
      return _cached;
    }
    try {
      var parsed = JSON.parse(el.textContent || '[]');
      _cached = Array.isArray(parsed) ? parsed : [];
    } catch (e) {
      _cached = [];
    }
    return _cached;
  }

  // ---------------------------------------------------------------------
  // Filters — pure, return new arrays.
  // ---------------------------------------------------------------------

  function filterByBand(chunks, band) {
    return chunks.filter(function(b) { return b.band === band; });
  }

  function filterByRoute(chunks, routeID) {
    return chunks.filter(function(b) { return b.route_id === routeID; });
  }

  function filterByDate(chunks, date) {
    return chunks.filter(function(b) { return b.date === date; });
  }

  // ---------------------------------------------------------------------
  // Grouping — return Map<string, ChunkView[]>.
  // ---------------------------------------------------------------------

  function groupBy(chunks, keyFn) {
    var out = new Map();
    for (var i = 0; i < chunks.length; i++) {
      var k = keyFn(chunks[i]);
      var arr = out.get(k);
      if (!arr) {
        arr = [];
        out.set(k, arr);
      }
      arr.push(chunks[i]);
    }
    return out;
  }

  function groupByRoute(chunks) { return groupBy(chunks, function(b) { return b.route_id; }); }
  function groupByDate(chunks)  { return groupBy(chunks, function(b) { return b.date; }); }
  function groupByBand(chunks)  { return groupBy(chunks, function(b) { return b.band; }); }

  // ---------------------------------------------------------------------
  // Math — line-for-line ports of internal/transit/chunk/math.go.
  // ---------------------------------------------------------------------

  function cvFromSums(n, sumH, sumHSq) {
    if (n < 2 || sumH <= 0) return 0;
    var mean = sumH / n;
    var variance = sumHSq / n - mean * mean;
    if (variance < 0) variance = 0;
    return Math.sqrt(variance) / mean;
  }

  function ewtSecFromSums(sumH, sumHSq, schedHeadwaySec) {
    if (sumH <= 0 || schedHeadwaySec <= 0) return 0;
    var awt = sumHSq / (2 * sumH);
    var swt = schedHeadwaySec / 2;
    if (awt <= swt) return 0;
    return awt - swt;
  }

  function waitMinFromSums(n, sumH) {
    if (n === 0) return 0;
    return (sumH / n) / 60;
  }

  // ---------------------------------------------------------------------
  // KPI — line-for-line port of chunk.KPI in
  // internal/transit/chunk/rollup.go. Single source of truth for
  // reducing a slice of chunks into one KPI reading.
  //
  // metric: 'otp' | 'cancel' | 'notice' | 'wait' | 'ewt' | 'cv'
  // band:   '' (pool all bands) | 'morning' | 'midday' | 'evening'
  // Returns a number, or null when there's not enough data.
  //
  // Aggregation rules:
  //   otp/cancel/notice → trip-weighted SUM of raw counts, divided once.
  //   wait              → pooled mean gap SUM(h) / SUM(n).
  //   cv/ewt            → per-route pool first, then mean across routes.
  //                       Each route gets one vote.
  // ---------------------------------------------------------------------

  function kpi(chunks, metric, band) {
    band = band || '';
    var inBand = function(b) { return band === '' || b.band === band; };

    if (metric === 'otp' || metric === 'cancel' || metric === 'notice') {
      var trips = 0, onTime = 0, scheduled = 0, cancelled = 0, noNotice = 0;
      for (var i = 0; i < chunks.length; i++) {
        var b = chunks[i];
        if (!inBand(b)) continue;
        trips     += b.trips;
        onTime    += b.on_time;
        scheduled += b.scheduled;
        cancelled += b.cancelled;
        noNotice  += b.no_notice;
      }
      if (metric === 'otp')    return trips > 0     ? (onTime * 100 / trips)       : null;
      if (metric === 'cancel') return scheduled > 0 ? (cancelled * 100 / scheduled) : null;
      if (metric === 'notice') return cancelled > 0 ? (noNotice * 100 / cancelled)  : null;
    }

    if (metric === 'wait') {
      var wn = 0, wSum = 0;
      for (var j = 0; j < chunks.length; j++) {
        var c = chunks[j];
        if (!inBand(c)) continue;
        wn   += c.headway_n;
        wSum += c.headway_sum_sec;
      }
      return wn > 0 ? waitMinFromSums(wn, wSum) : null;
    }

    if (metric === 'cv' || metric === 'ewt') {
      var perRoute = new Map();
      for (var k = 0; k < chunks.length; k++) {
        var ch = chunks[k];
        if (!inBand(ch)) continue;
        var a = perRoute.get(ch.route_id);
        if (!a) {
          a = { n: 0, sumH: 0, sumHSq: 0, schedSum: 0, schedN: 0 };
          perRoute.set(ch.route_id, a);
        }
        a.n      += ch.headway_n;
        a.sumH   += ch.headway_sum_sec;
        a.sumHSq += ch.headway_sum_sq_sec;
        if (ch.sched_headway_sec > 0) {
          a.schedSum += ch.sched_headway_sec;
          a.schedN++;
        }
      }
      var sum = 0, count = 0;
      if (metric === 'cv') {
        perRoute.forEach(function(a) {
          if (a.n < 2) return;
          sum += cvFromSums(a.n, a.sumH, a.sumHSq);
          count++;
        });
      } else {
        perRoute.forEach(function(a) {
          if (a.n < 1 || a.schedN === 0) return;
          var sched = a.schedSum / a.schedN;
          sum += ewtSecFromSums(a.sumH, a.sumHSq, sched) / 60;
          count++;
        });
      }
      return count > 0 ? (sum / count) : null;
    }

    return null;
  }

  // ---------------------------------------------------------------------
  // Formatters — match the Go view_helpers.go output exactly.
  // ---------------------------------------------------------------------

  function format(value, metric) {
    if (value == null) return '\u2014';
    switch (metric) {
      case 'otp':    return value.toFixed(0);
      case 'cancel': return value.toFixed(1);
      case 'notice': return value.toFixed(0);
      case 'wait':   return value.toFixed(1);
      case 'ewt':    return value.toFixed(1);
      case 'cv':     return value.toFixed(2);
    }
    return String(value);
  }

  // ---------------------------------------------------------------------
  // Public surface
  // ---------------------------------------------------------------------

  window.transitChunks = {
    loadChunks: loadChunks,
    filterByBand: filterByBand,
    filterByRoute: filterByRoute,
    filterByDate: filterByDate,
    groupByRoute: groupByRoute,
    groupByDate: groupByDate,
    groupByBand: groupByBand,
    kpi: kpi,
    format: format,
    // Math helpers exposed for testing in the browser console.
    cvFromSums: cvFromSums,
    ewtSecFromSums: ewtSecFromSums,
    waitMinFromSums: waitMinFromSums
  };
})();
