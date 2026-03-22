// Transit performance charts — used on the report page.
(function () {
  'use strict';

  const STATS_URL = "/api/transit/stats";
  let perfRange = "percentiles";
  const tc = ThemeColors();

  const PCTL_COLORS = {
    p50:  tc.statusOk,
    p90:  tc.statusInfo,
    p99:  tc.statusWarn,
    p999: tc.statusError,
  };

  function init() {
    const btns = document.querySelectorAll(".perf-range-btn");
    for (let i = 0; i < btns.length; i++) {
      btns[i].addEventListener("click", function (e) {
        perfRange = e.currentTarget.getAttribute("data-range");
        const all = document.querySelectorAll(".perf-range-btn");
        for (let j = 0; j < all.length; j++) {
          const isActive = all[j] === e.currentTarget;
          all[j].className = "perf-range-btn" + (isActive ? " perf-range-active" : "");
          all[j].setAttribute("aria-selected", isActive ? "true" : "false");
        }
        fetchData();
      });
    }
    fetchData();
  }

  function fetchData() {
    fetch(STATS_URL + "?range=" + perfRange)
      .then(function (resp) {
        if (!resp.ok) throw new Error("HTTP " + resp.status);
        return resp.json();
      })
      .then(function (data) {
        render(data);
      })
      .catch(function () {
        const chart = document.getElementById("perf-chart");
        if (chart) chart.innerHTML = '<p class="perf-loading">Unable to load performance data.</p>';
      });
  }

  function render(data) {
    if (data.type === "percentiles" && data.buckets.length > 0) {
      renderPercentileChart(data.buckets);
      updatePercentileSummary(data.buckets);
    } else if (data.type === "week" && data.days.length > 0) {
      renderWeekChart(data.days);
      updateWeekSummary(data.days);
    } else {
      const chart = document.getElementById("perf-chart");
      if (chart) chart.innerHTML = '<p class="perf-loading">Collecting data\u2026 Charts appear once delay observations are recorded.</p>';
      setStat("perf-p50", "\u2014");
      setStat("perf-p90", "\u2014");
      setStat("perf-p99", "\u2014");
      setStat("perf-p999", "\u2014");
    }
  }

  function renderPercentileChart(buckets) {
    const container = document.getElementById("perf-chart");
    if (!container) return;

    const W = container.clientWidth || 600;
    const H = 240;
    const PAD = { top: 20, right: 80, bottom: 30, left: 50 };
    const cw = W - PAD.left - PAD.right;
    const ch = H - PAD.top - PAD.bottom;

    const t0 = new Date(buckets[0].time).getTime();
    const t1 = new Date(buckets[buckets.length - 1].time).getTime();
    const tRange = t1 - t0 || 1;

    let maxDelay = 60;
    for (let i = 0; i < buckets.length; i++) {
      maxDelay = Math.max(maxDelay, buckets[i].p999);
    }
    maxDelay = Math.ceil(maxDelay / 60) * 60;

    function xPos(t) { return PAD.left + ((t - t0) / tRange) * cw; }
    function yPos(sec) { return PAD.top + ch - (sec / maxDelay) * ch; }

    let svg = '<svg width="' + W + '" height="' + H + '" role="img" aria-label="Delay percentiles over the last 24 hours">';

    const yStep = maxDelay <= 120 ? 30 : maxDelay <= 300 ? 60 : maxDelay <= 600 ? 120 : 300;
    for (let sec = 0; sec <= maxDelay; sec += yStep) {
      const gy = yPos(sec);
      svg += '<line x1="' + PAD.left + '" y1="' + gy + '" x2="' + (W - PAD.right) + '" y2="' + gy + '" stroke="var(--pico-muted-border-color)" stroke-dasharray="3,3"/>';
      const label = sec < 60 ? sec + "s" : (sec / 60) + "m";
      svg += '<text x="' + (PAD.left - 6) + '" y="' + (gy + 4) + '" text-anchor="end" class="perf-axis-label">' + label + '</text>';
    }

    svg += '<line x1="' + PAD.left + '" y1="' + yPos(0) + '" x2="' + (W - PAD.right) + '" y2="' + yPos(0) + '" stroke="var(--thunder-400)" stroke-width="1" opacity="0.5"/>';

    const hourMs = 3600000;
    const labelInterval = 4 * hourMs;
    const firstLabel = Math.ceil(t0 / labelInterval) * labelInterval;
    for (let lt = firstLabel; lt <= t1; lt += labelInterval) {
      const lx = xPos(lt);
      const d = new Date(lt);
      svg += '<text x="' + lx + '" y="' + (H - 6) + '" text-anchor="middle" class="perf-axis-label">' + d.getHours() + ':00</text>';
    }

    let areaPath = "M";
    for (let i = 0; i < buckets.length; i++) {
      const bx = xPos(new Date(buckets[i].time).getTime());
      areaPath += (i > 0 ? " L" : "") + bx + "," + yPos(buckets[i].p90);
    }
    for (let i = buckets.length - 1; i >= 0; i--) {
      const bx = xPos(new Date(buckets[i].time).getTime());
      areaPath += " L" + bx + "," + yPos(buckets[i].p50);
    }
    areaPath += " Z";
    svg += '<path d="' + areaPath + '" fill="' + PCTL_COLORS.p90 + '" opacity="0.08"/>';

    const lines = [
      { key: "p999", label: "P99.9", dash: "4,3" },
      { key: "p99",  label: "P99",   dash: "6,3" },
      { key: "p90",  label: "P90",   dash: "" },
      { key: "p50",  label: "P50",   dash: "" },
    ];

    const labelPositions = [];
    for (let li = 0; li < lines.length; li++) {
      const line = lines[li];
      const color = PCTL_COLORS[line.key];
      const width = line.key === "p50" ? 2.5 : line.key === "p90" ? 2 : 1.5;

      let path = "M";
      for (let i = 0; i < buckets.length; i++) {
        const bx = xPos(new Date(buckets[i].time).getTime());
        const by = yPos(buckets[i][line.key]);
        path += (i > 0 ? " L" : "") + bx + "," + by;
      }

      const dashAttr = line.dash ? ' stroke-dasharray="' + line.dash + '"' : '';
      svg += '<path d="' + path + '" fill="none" stroke="' + color + '" stroke-width="' + width + '"' + dashAttr + '/>';

      const lastBucket = buckets[buckets.length - 1];
      const ly = yPos(lastBucket[line.key]);
      labelPositions.push({ y: ly + 4, label: line.label, color: color });
    }

    // Space out labels so they don't overlap (minimum 14px apart)
    labelPositions.sort(function (a, b) { return a.y - b.y; });
    for (let i = 1; i < labelPositions.length; i++) {
      if (labelPositions[i].y - labelPositions[i - 1].y < 14) {
        labelPositions[i].y = labelPositions[i - 1].y + 14;
      }
    }

    for (let i = 0; i < labelPositions.length; i++) {
      const lp = labelPositions[i];
      svg += '<text x="' + (W - PAD.right + 6) + '" y="' + lp.y + '" class="perf-axis-label" fill="' + lp.color + '" font-weight="600">' + lp.label + '</text>';
    }

    for (let i = 0; i < buckets.length; i++) {
      const bx = xPos(new Date(buckets[i].time).getTime());
      const halfGap = i < buckets.length - 1
        ? (xPos(new Date(buckets[i + 1].time).getTime()) - bx) / 2
        : (i > 0 ? (bx - xPos(new Date(buckets[i - 1].time).getTime())) / 2 : 20);
      svg += '<rect x="' + (bx - halfGap) + '" y="' + PAD.top + '" width="' + (halfGap * 2) + '" height="' + ch + '" fill="transparent" data-bucket="' + i + '" class="perf-hover-zone"/>';
    }

    svg += "</svg>";
    container.innerHTML = svg;

    const tooltip = document.createElement("div");
    tooltip.className = "perf-tooltip";
    tooltip.style.display = "none";
    container.style.position = "relative";
    container.appendChild(tooltip);

    const svgEl = container.querySelector("svg");
    const crosshair = document.createElementNS("http://www.w3.org/2000/svg", "line");
    crosshair.setAttribute("y1", PAD.top);
    crosshair.setAttribute("y2", PAD.top + ch);
    crosshair.setAttribute("stroke", "var(--thunder-400)");
    crosshair.setAttribute("stroke-width", "1");
    crosshair.setAttribute("stroke-dasharray", "3,2");
    crosshair.setAttribute("opacity", "0");
    svgEl.appendChild(crosshair);

    container.addEventListener("mouseover", function (e) {
      const zone = e.target.closest(".perf-hover-zone");
      if (!zone) return;
      const idx = parseInt(zone.dataset.bucket, 10);
      const b = buckets[idx];
      const bx = xPos(new Date(b.time).getTime());
      const d = new Date(b.time);
      const timeLabel = String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");

      crosshair.setAttribute("x1", bx);
      crosshair.setAttribute("x2", bx);
      crosshair.setAttribute("opacity", "0.5");

      tooltip.innerHTML =
        '<strong>' + timeLabel + '</strong>' +
        '<div><span class="perf-tt-dot" style="background:' + PCTL_COLORS.p50 + '"></span>P50: ' + formatDelay(b.p50) + '</div>' +
        '<div><span class="perf-tt-dot" style="background:' + PCTL_COLORS.p90 + '"></span>P90: ' + formatDelay(b.p90) + '</div>' +
        '<div><span class="perf-tt-dot" style="background:' + PCTL_COLORS.p99 + '"></span>P99: ' + formatDelay(b.p99) + '</div>' +
        '<div><span class="perf-tt-dot" style="background:' + PCTL_COLORS.p999 + '"></span>P99.9: ' + formatDelay(b.p999) + '</div>' +
        '<div class="perf-tt-count">' + b.count + ' observations</div>';
      tooltip.style.display = "block";

      let tipX = bx + 12;
      if (tipX + 140 > W) tipX = bx - 152;
      tooltip.style.left = tipX + "px";
      tooltip.style.top = PAD.top + "px";
    });

    container.addEventListener("mouseout", function (e) {
      if (e.target.closest(".perf-hover-zone")) {
        crosshair.setAttribute("opacity", "0");
        tooltip.style.display = "none";
      }
    });
  }

  function updatePercentileSummary(buckets) {
    const sums = { p50: 0, p90: 0, p99: 0, p999: 0 };
    for (let i = 0; i < buckets.length; i++) {
      sums.p50 += buckets[i].p50;
      sums.p90 += buckets[i].p90;
      sums.p99 += buckets[i].p99;
      sums.p999 += buckets[i].p999;
    }
    const n = buckets.length;
    setStat("perf-p50", formatDelay(sums.p50 / n));
    setStat("perf-p90", formatDelay(sums.p90 / n));
    setStat("perf-p99", formatDelay(sums.p99 / n));
    setStat("perf-p999", formatDelay(sums.p999 / n));
  }

  function renderWeekChart(days) {
    const container = document.getElementById("perf-chart");
    if (!container) return;

    const W = container.clientWidth || 600;
    const barH = 28;
    const gap = 6;
    const H = days.length * (barH + gap) + 40;
    const PAD = { top: 10, right: 16, bottom: 10, left: 60 };
    const cw = W - PAD.left - PAD.right;

    let svg = '<svg width="' + W + '" height="' + H + '" role="img" aria-label="Daily on-time percentage for the last 7 days">';

    for (let i = 0; i < days.length; i++) {
      const d = days[i];
      const y = PAD.top + i * (barH + gap);
      const pct = d.avg_on_time;
      const bw = Math.max(1, (pct / 100) * cw);

      const dt = new Date(d.date);
      const dayLabel = dt.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
      svg += '<text x="' + (PAD.left - 6) + '" y="' + (y + barH / 2 + 4) + '" text-anchor="end" class="perf-axis-label">' + dayLabel + '</text>';

      const color = pct >= 80 ? tc.accent : pct >= 60 ? tc.statusWarn : tc.statusError;
      svg += '<rect x="' + PAD.left + '" y="' + y + '" width="' + bw + '" height="' + barH + '" rx="4" fill="' + color + '" opacity="0.8"/>';
      svg += '<text x="' + (PAD.left + bw + 6) + '" y="' + (y + barH / 2 + 4) + '" class="perf-bar-label">' + pct.toFixed(1) + '%</text>';
    }

    svg += "</svg>";
    container.innerHTML = svg;
  }

  function updateWeekSummary(days) {
    let totalOnTime = 0, totalDelay = 0;
    for (let i = 0; i < days.length; i++) {
      totalOnTime += days[i].avg_on_time;
      totalDelay += days[i].avg_delay;
    }
    const n = days.length;
    setStat("perf-p50", (totalOnTime / n).toFixed(1) + "%");
    setStat("perf-p90", formatDelay(totalDelay / n));
    setStat("perf-p99", "\u2014");
    setStat("perf-p999", "\u2014");
  }

  function formatDelay(seconds) {
    const abs = Math.abs(seconds);
    if (abs < 60) return Math.round(abs) + "s";
    const min = Math.floor(abs / 60);
    const sec = Math.round(abs % 60);
    return min + "m " + sec + "s";
  }

  function setStat(id, value) {
    const el = document.getElementById(id);
    if (el) el.textContent = value;
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
