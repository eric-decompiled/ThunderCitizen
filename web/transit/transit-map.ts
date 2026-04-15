// Transit map — TypeScript migration
// Converted from IIFE to ES module with typed APIs

declare const htmx: { ajax(method: string, url: string, opts: { target: string | Element; swap: string }): void };

import * as L from "leaflet";

// Type augmentation for leaflet-polylinedecorator plugin (no official types).
type LeafletWithDecorator = typeof L & {
  polylineDecorator(
    latlngs: L.LatLngExpression[] | L.LatLngExpression[][],
    options: { patterns: Array<{ offset: string; repeat: string; symbol: unknown }> }
  ): L.Layer;
  Symbol: {
    arrowHead(options: {
      pixelSize?: number;
      polygon?: boolean;
      pathOptions?: L.PathOptions;
    }): unknown;
  };
};
const LDecorator = L as unknown as LeafletWithDecorator;
import type {
  VehiclePayload,
  Stop,
  Timepoint,
  PlanResponse,
  RouteShape,
  Itinerary,
  Leg,
} from "./api.gen";
import type { SSEPayload } from "./types";
interface ThemeColors {
  accent: string;
  statusOk: string;
  statusWarn: string;
  statusError: string;
  statusInfo: string;
  statusEarlyDep: string;
  statusMuted: string;
  surfaceDark: string;
  textColor: string;
  termAccent: string;
  termBg: string;
  termFgDim: string;
}

declare function ThemeColors(): ThemeColors;

let TC: ThemeColors;

// ---------------------------------------------------------------------------
// Local vehicle type (mapped from API Vehicle)
// ---------------------------------------------------------------------------

interface LocalVehicle {
  id: string;
  routeId: string;
  lat: number;
  lon: number;
  bearing: number;
  speed: number;
  status: string;
  stopId: string;
  nearStop: string;
  delay: number | null;
}

// ---------------------------------------------------------------------------
// Marker data map (replaces property hacking on markers)
// ---------------------------------------------------------------------------

interface StopMarkerData {
  stopId: string;
  stopName: string;
  isHub: boolean;
  isTimepoint: boolean;
  hasAlert: boolean;
}

type MapHostElement = HTMLElement & { _leafletMap?: L.Map | null };

const markerData = new Map<L.Layer, StopMarkerData>();

function getMarkerData(marker: L.Layer): StopMarkerData | undefined {
  return markerData.get(marker);
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const VEHICLE_STREAM_URL = "/api/transit/vehicles/stream";
const VEHICLE_JSON_URL = "/api/transit/vehicles.json"; // fallback

// Thunder Bay center coordinates
const TB_CENTER: L.LatLngTuple = [48.38, -89.25];
const TB_ZOOM = 12;

// All known routes (from GTFS static data)
// Route metadata — populated from server-rendered JSON on init
const ROUTE_COLORS: Record<string, string> = {};
const ROUTE_NAMES: Record<string, string> = {};
const ROUTE_TERMINALS: Record<string, string[]> = {};
// Derived from route meta (7-day trailing window of scheduled service)
const ALL_ROUTES: string[] = [];

interface RouteMetaResponse {
  route_id: string;
  name: string;
  color: string;
  text_color: string;
  terminals: string[];
}

function loadRouteMeta(): void {
  const el = document.getElementById("route-meta");
  if (!el) return;
  try {
    const routes = JSON.parse(el.textContent || "[]") as RouteMetaResponse[];
    for (let i = 0; i < routes.length; i++) {
      const rm = routes[i];
      ALL_ROUTES.push(rm.route_id);
      if (rm.color) ROUTE_COLORS[rm.route_id] = rm.color;
      if (rm.name) ROUTE_NAMES[rm.route_id] = rm.name;
      if (rm.terminals && rm.terminals.length > 0) ROUTE_TERMINALS[rm.route_id] = rm.terminals;
    }
  } catch (_) { /* fallback: route grid still works with IDs */ }
}

const SHAPES_URL = "/static/transit/route-shapes.json";
const STOPS_URL = "/api/transit/stops";
const TIMEPOINTS_URL = "/api/transit/timepoints";
const PLAN_URL = "/api/transit/plan";

// ---------------------------------------------------------------------------
// Module-level state
// ---------------------------------------------------------------------------

let map: L.Map | null = null;
let busLayer: L.LayerGroup | null = null; // L.layerGroup for bus markers
const busMarkers: Record<string, L.Marker> = {};
let routeLines: Record<string, L.LayerGroup> = {}; // route_id -> L.layerGroup
let routeShapes: RouteShape[] = []; // loaded from JSON
let lastFeedTimestamp = 0;
let selectedRoute: string | null = null;
let hoveredRoute: string | null = null;
let sseSource: EventSource | null = null;
let fallbackTimer: number | null = null;
let selectedStop: { id: string; name: string } | null = null;
let infoBarLocked = false;
let lastVehicles: LocalVehicle[] = [];

// Routes with active cancellations (from server-rendered data attribute)
const cancelledRoutes: Record<string, boolean> = {};
// Cancelled trip details: route_id -> [{...}]
interface CancelledTrip {
  start_time?: string;
  end_time?: string;
  headsign?: string;
  upcoming?: boolean;
  lead_min?: number;
  lead_label?: string;
  first_seen?: string;
  snapshot_count?: number;
}
let cancelledTrips: Record<string, CancelledTrip[]> = {};

// Routes with no scheduled service today
const noServiceRoutes: Record<string, boolean> = {};

// Stop-level alerts (from server-rendered JSON)
interface StopAlert {
  header?: string;
  description?: string;
}
let stopAlerts: Record<string, StopAlert[]> = {};

function linkifyStopIds(text: string): string {
  const escaped = escapeHtml(text);
  return escaped.replace(/\b(\d{3,4})\b/g, function (match, id: string) {
    const stop = allStops.find(function (s) { return s.stop_id === id; });
    if (!stop) return match;
    return '<a href="#transit-map" class="alert-stop-link" data-alert-stop="' +
      escapeHtml(id) + '" title="Show stop ' + escapeHtml(id) + ' on map">' + id + '</a>';
  });
}

function dedupeAlerts(alerts: readonly StopAlert[] | undefined): StopAlert[] {
  if (!alerts || alerts.length === 0) return [];
  const seen: Record<string, boolean> = {};
  const out: StopAlert[] = [];
  for (let i = 0; i < alerts.length; i++) {
    const a = alerts[i];
    const header = a.header || '';
    const desc = a.description || '';
    if (!header && !desc) continue;
    const key = header + '\u241f' + desc;
    if (seen[key]) continue;
    seen[key] = true;
    out.push(a);
  }
  return out;
}

function focusStopOnMap(stopId: string): void {
  const stop = allStops.find(function (s) { return s.stop_id === stopId; });
  if (!stop || !map) return;
  const mapEl = document.getElementById('transit-map');
  if (mapEl) mapEl.scrollIntoView({ behavior: 'smooth', block: 'start' });
  const doFly = function () {
    if (!map) return;
    map.flyTo([stop.lat, stop.lon], Math.max(map.getZoom(), 17), { duration: 0.8 });
    lockStopInfo(stop.stop_id, stop.stop_name);
  };
  // Give the window scroll a moment to settle before flyTo so Leaflet's
  // bounds calc uses the post-scroll viewport position.
  window.setTimeout(doFly, 400);
}

document.addEventListener('click', function (e: Event) {
  const target = e.target as HTMLElement | null;
  if (!target) return;
  const link = target.closest('.alert-stop-link') as HTMLElement | null;
  if (!link) return;
  e.preventDefault();
  const stopId = link.getAttribute('data-alert-stop');
  if (stopId) focusStopOnMap(stopId);
});

// Read server-rendered data from the DOM (must be called after DOMContentLoaded)
function loadServerData(): void {
  const cancelRoutesEl = document.getElementById("cancelled-routes-data");
  if (cancelRoutesEl && (cancelRoutesEl as HTMLElement).dataset.routes) {
    (cancelRoutesEl as HTMLElement).dataset.routes!.split(",").forEach(function (r: string) {
      if (r) cancelledRoutes[r.trim()] = true;
    });
  }
  const tripEl = document.getElementById("cancelled-trips-data");
  if (tripEl) {
    try { cancelledTrips = JSON.parse(tripEl.textContent || "{}") || {}; } catch (_e) { /* malformed JSON, use default */ }
  }
  let totalCancelled = 0;
  for (const rid in cancelledTrips) {
    totalCancelled += cancelledTrips[rid].length;
  }
  if (totalCancelled > 0) {
    const wrap = document.getElementById("stat-cancellations-wrap");
    const cancelEl = document.getElementById("stat-cancellations");
    if (wrap) wrap.style.display = "";
    if (cancelEl) cancelEl.textContent = String(totalCancelled);
  }

  const noSvcEl = document.getElementById("no-service-routes-data");
  if (noSvcEl && (noSvcEl as HTMLElement).dataset.routes) {
    (noSvcEl as HTMLElement).dataset.routes!.split(",").forEach(function (r: string) {
      if (r) noServiceRoutes[r.trim()] = true;
    });
  }

  const alertsEl = document.getElementById("stop-alerts-data");
  if (alertsEl) {
    try { stopAlerts = JSON.parse(alertsEl.textContent || "{}") || {}; } catch (_e) { /* malformed JSON, use default */ }
  }
}

let stopLayer: L.LayerGroup | null = null; // L.layerGroup for regular stop markers
let hubLayer: L.LayerGroup | null = null;  // L.layerGroup for transfer hub markers (always visible)
let tpLayer: L.LayerGroup | null = null;   // L.layerGroup for time point ring markers
let allStops: readonly Stop[] = []; // cached for stop layer rebuild
let timepointData: Record<string, { routes: readonly string[]; colors: readonly string[] }> = {}; // stop_id -> { routes, colors }

// ---------------------------------------------------------------------------
// Tile set types
// ---------------------------------------------------------------------------


// ---------------------------------------------------------------------------
// Layer bar state — read from server-rendered DOM buttons
// ---------------------------------------------------------------------------

interface LayerBarState {
  buses: boolean;
  network: boolean;
  timepoints: boolean;
}

const layerState: LayerBarState = { buses: true, network: true, timepoints: false };

// ---------------------------------------------------------------------------
// initMap
// ---------------------------------------------------------------------------

function initMap(): void {
  TC = ThemeColors();
  const el = document.getElementById("transit-map");
  if (!el) return;

  loadServerData();
  loadRouteMeta();
  updateCancelsPanel();

  map = L.map("transit-map", {
    scrollWheelZoom: false,
    minZoom: TB_ZOOM - 2,
  }).setView(TB_CENTER, TB_ZOOM);

  (el as MapHostElement)._leafletMap = map;

  map.on("click", function () {
    if (infoBarLocked) unlockInfoBar();
    if (selectedStop) { selectedStop = null; }
    if (selectedRoute) selectRoute(null);
    if (tripRouteLayer) {
      hideRouteSummaryBar();
      clearTripRoute();
    }
  });

  // --- Vector tiles via Protomaps (ThunderMapTiles from map-tiles.js) ---
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const tiles = (window as any).ThunderMapTiles();
  tiles.addTo(map);

  function applyTiles(): void {
    tiles.swap();
  }

  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", applyTiles);

  // Bus layer group
  busLayer = L.layerGroup().addTo(map);

  // Layer toggles — read initial state from server-rendered buttons
  const layerBar = document.getElementById("layer-bar");
  if (layerBar) {
    const btns = layerBar.querySelectorAll<HTMLButtonElement>("[data-layer]");
    btns.forEach(function (btn) {
      const key = btn.dataset.layer as keyof LayerBarState;
      if (key) layerState[key] = btn.classList.contains("active");
    });

    layerBar.addEventListener("click", function (e: Event) {
      const btn = (e.target as HTMLElement).closest("[data-layer]") as HTMLElement | null;
      if (!btn) return;
      const key = btn.getAttribute("data-layer") as keyof LayerBarState | null;
      if (!key) return;
      layerState[key] = !layerState[key];
      btn.classList.toggle("active", layerState[key]);
      applyLayerVisibility();
    });
  }

  function applyLayerVisibility(): void {
    const st = layerState;

    // Buses
    if (busLayer) {
      if (st.buses && !map!.hasLayer(busLayer)) busLayer.addTo(map!);
      if (!st.buses && map!.hasLayer(busLayer)) map!.removeLayer(busLayer);
    }

    // Network: route lines + stops + hubs
    for (const rid in routeLines) {
      if (st.network && !map!.hasLayer(routeLines[rid])) routeLines[rid].addTo(map!);
      if (!st.network && map!.hasLayer(routeLines[rid])) map!.removeLayer(routeLines[rid]);
    }
    if (stopLayer) {
      if (st.network && map!.getZoom() >= 13 && !map!.hasLayer(stopLayer)) stopLayer.addTo(map!);
      if (!st.network && map!.hasLayer(stopLayer)) map!.removeLayer(stopLayer);
    }
    if (hubLayer) {
      if (st.network && !map!.hasLayer(hubLayer)) hubLayer.addTo(map!);
      if (!st.network && map!.hasLayer(hubLayer)) map!.removeLayer(hubLayer);
    }

    // Time points
    if (tpLayer) {
      if (st.timepoints && !map!.hasLayer(tpLayer)) tpLayer.addTo(map!);
      if (!st.timepoints && map!.hasLayer(tpLayer)) map!.removeLayer(tpLayer);
    }
  }

  // Load route shapes and draw lines
  loadRouteShapes();

  // Load stop markers (shown at higher zoom levels)
  loadStops();
  map.on("zoomend", function () {
    updateStopVisibility();
    applyLayerVisibility();
    // Refresh bus icon sizes for new zoom level
    if (lastVehicles.length > 0) updateMarkers(lastVehicles);
  });

  // Wire up route filter clear button
  const clearBtn = document.getElementById("route-clear-filter");
  if (clearBtn) {
    clearBtn.addEventListener("click", function () {
      selectRoute(null);
    });
  }

  // Locate button in header bar
  const locateBtn = document.getElementById("map-locate-btn");
  if (locateBtn) locateBtn.addEventListener("click", function () { onLocateClick(); });

  // Wire up trip planner
  initTripPlanner();

  connectVehicleStream();
  startTerminalClock();

  // Pause vehicle stream when tab is backgrounded — saves CPU/battery on iPhone.
  document.addEventListener("visibilitychange", function () {
    if (document.hidden) {
      if (sseSource) { sseSource.close(); sseSource = null; }
      if (fallbackTimer !== null) { clearInterval(fallbackTimer); fallbackTimer = null; }
    } else {
      if (!sseSource && fallbackTimer === null) connectVehicleStream();
    }
  });

  // Buses and cancellations are inline tab panels now — populated on data update


}

// ---------------------------------------------------------------------------
// Route shapes
// ---------------------------------------------------------------------------

function loadRouteShapes(): void {
  fetch(SHAPES_URL)
    .then(function (resp) {
      if (!resp.ok) throw new Error("HTTP " + resp.status);
      return resp.json() as Promise<RouteShape[]>;
    })
    .then(function (shapes) {
      routeShapes = shapes;
      drawRouteLines();
    })
    .catch(function (err) {
      console.warn("Failed to load route shapes:", err);
    });
}

function drawRouteLines(): void {
  // Remove existing lines
  for (const id in routeLines) {
    map!.removeLayer(routeLines[id]);
  }
  routeLines = {};

  for (let i = 0; i < routeShapes.length; i++) {
    const shape = routeShapes[i];
    const color = ROUTE_COLORS[shape.route_id] || TC.statusMuted;

    const line = L.polyline(shape.coordinates as [number, number][], {
      color: color,
      weight: routeLineWeight(shape.route_id),
      opacity: routeLineOpacity(shape.route_id),
      smoothFactor: 1,
    });

    // Click route line to select it, hover to preview info card
    (function (routeId: string) {
      line.on("click", function (e: L.LeafletEvent) {
        L.DomEvent.stopPropagation(e as L.LeafletMouseEvent);
        if (selectedRoute === routeId) {
          selectRoute(null);
        } else {
          selectRoute(routeId);
        }
      });
      line.on("mouseover", function () {
        if (!selectedRoute && hoveredRoute !== routeId) {
          hoveredRoute = routeId;
          restyleRouteLines();
        }
        const name = ROUTE_NAMES[routeId] || routeId;
        const color = ROUTE_COLORS[routeId] || TC.statusMuted;
        const busCount = lastVehicles.filter(function (v) { return v.routeId === routeId; }).length;
        showInfoBar(
          '<span class="info-route" style="background:' + color + '">' + routeId + '</span> ' +
          '<span class="info-name">' + name + '</span>' +
          (busCount > 0 ? ' <span class="info-detail">' + busCount + ' bus' + (busCount > 1 ? 'es' : '') + ' active</span>' : '')
        );
      });
      line.on("mouseout", function () {
        if (!selectedRoute && hoveredRoute === routeId) {
          hoveredRoute = null;
          restyleRouteLines();
        }
        hideInfoBar();
      });
    })(shape.route_id);

    // Group multiple shapes per route into a layer group
    if (!routeLines[shape.route_id]) {
      routeLines[shape.route_id] = L.layerGroup().addTo(map!);
    }
    routeLines[shape.route_id].addLayer(line);
  }

  // Bring focused route line to front
  const focusId = selectedRoute || hoveredRoute;
  if (focusId && routeLines[focusId]) {
    routeLines[focusId].eachLayer(function (l) { (l as L.Polyline).bringToFront(); });
  }
}

function routeLineWeight(routeId: string): number {
  if (selectedRoute === routeId) return 5;
  if (hoveredRoute === routeId) return 5;
  return 3;
}

function routeLineOpacity(routeId: string): number {
  // Selected state takes priority
  if (selectedRoute) return selectedRoute === routeId ? 0.9 : 0.1;
  // Hover state
  if (hoveredRoute) return hoveredRoute === routeId ? 0.85 : 0.15;
  // Default
  return 0.4;
}

function restyleRouteLines(): void {
  for (const id in routeLines) {
    const w = routeLineWeight(id);
    const o = routeLineOpacity(id);
    routeLines[id].eachLayer(function (l) {
      (l as L.Polyline).setStyle({ weight: w, opacity: o });
    });
  }
  const focusId = selectedRoute || hoveredRoute;
  if (focusId && routeLines[focusId]) {
    routeLines[focusId].eachLayer(function (l) { (l as L.Polyline).bringToFront(); });
  }
  // Stops and buses always above route lines
  if (hubLayer) hubLayer.eachLayer(function (l) { if ("bringToFront" in l) (l as L.CircleMarker).bringToFront(); });
  if (stopLayer && map!.hasLayer(stopLayer)) stopLayer.eachLayer(function (l) { if ("bringToFront" in l) (l as L.CircleMarker).bringToFront(); });
  if (busLayer) busLayer.eachLayer(function (l) { if ("bringToFront" in l) (l as L.CircleMarker).bringToFront(); });
}

// ---------------------------------------------------------------------------
// Vehicle data handling
// ---------------------------------------------------------------------------

function handleVehicleData(data: VehiclePayload): void {
  if (data.timestamp === lastFeedTimestamp) return;
  lastFeedTimestamp = data.timestamp;

  const vehicles: LocalVehicle[] = (data.vehicles || []).map(function (v) {
    return {
      id: v.id,
      routeId: v.route_id,
      lat: v.lat,
      lon: v.lon,
      bearing: v.bearing || 0,
      speed: v.speed || 0,
      status: v.status || "",
      stopId: v.stop_id || "",
      nearStop: v.near_stop || "",
      delay: v.delay ?? null,
    };
  });

  lastVehicles = vehicles;
  updateMarkers(vehicles);
  updateStats(vehicles);
  updateRouteGrid(vehicles);
  updateStopOccupancy(vehicles);
  setStatValue("terminal-bus-count", String(vehicles.filter(function (v) { return !!v.routeId; }).length));
}

// ---------------------------------------------------------------------------
// SSE + fallback
// ---------------------------------------------------------------------------

function connectVehicleStream(): void {
  if (typeof EventSource === "undefined") {
    fetchVehiclesFallback();
    fallbackTimer = window.setInterval(fetchVehiclesFallback, 6000);
    return;
  }

  sseSource = new EventSource(VEHICLE_STREAM_URL);

  // Only show "Connecting..." if it takes more than 2 seconds
  const slowTimer = setTimeout(() => updateStatus("quiet", "Connecting..."), 2000);

  sseSource.onmessage = function (e: MessageEvent) {
    clearTimeout(slowTimer);
    try {
      const data = JSON.parse(e.data) as SSEPayload;
      if ("sleep" in data) {
        updateStatus("quiet", "Service ended");
        updateRouteGrid([]);
        setStatValue("terminal-bus-count", "0");
        return;
      }
      handleVehicleData(data);
      updateStatus("live");
    } catch (err) {
      console.warn("SSE parse error:", err);
    }
  };

  sseSource.onerror = function () {
    // EventSource auto-reconnects; only show dead if truly closed
    setTimeout(() => {
      if (sseSource && sseSource.readyState === EventSource.CLOSED) {
        updateStatus("dead");
      }
    }, 3000);
  };

  sseSource.onopen = function () {
    clearTimeout(slowTimer);
    updateStatus("live");
  };
}

function fetchVehiclesFallback(): void {
  fetch(VEHICLE_JSON_URL)
    .then(function (resp) {
      if (!resp.ok) throw new Error("HTTP " + resp.status);
      return resp.json() as Promise<VehiclePayload>;
    })
    .then(function (data) {
      handleVehicleData(data);
    })
    .catch(function (err) {
      console.warn("Transit feed error:", err);
      updateStatus("dead");
    });
}

// ---------------------------------------------------------------------------
// Map info bar — single-line slide-up info for hovered/selected entities
// ---------------------------------------------------------------------------

let infoBarDismissTimer = 0;

function showInfoBar(html: string): void {
  if (infoBarLocked) return;
  if (infoBarDismissTimer) { clearTimeout(infoBarDismissTimer); infoBarDismissTimer = 0; }
  const el = document.getElementById("map-info-bar");
  if (!el) return;
  el.innerHTML = html;
  el.classList.add("info-bar-visible");
  el.classList.remove("info-bar-locked");
}

function lockInfoBar(html: string): void {
  infoBarLocked = true;
  if (infoBarDismissTimer) { clearTimeout(infoBarDismissTimer); infoBarDismissTimer = 0; }
  const el = document.getElementById("map-info-bar");
  if (!el) return;
  el.innerHTML = html;
  el.classList.add("info-bar-visible", "info-bar-locked");
}

function unlockInfoBar(): void {
  infoBarLocked = false;
  selectedStop = null;
  const el = document.getElementById("map-info-bar");
  if (el) {
    el.classList.remove("info-bar-visible", "info-bar-locked");
  }
}

function hideInfoBar(): void {
  if (infoBarLocked) return;
  // Delay dismiss by 1 second so the bar lingers briefly
  if (infoBarDismissTimer) clearTimeout(infoBarDismissTimer);
  infoBarDismissTimer = window.setTimeout(function () {
    infoBarDismissTimer = 0;
    if (infoBarLocked) return;
    const el = document.getElementById("map-info-bar");
    if (el) el.classList.remove("info-bar-visible");
  }, 1000);
}

function bearingArrow(deg: number): string {
  const arrows = ['\u2191', '\u2197', '\u2192', '\u2198', '\u2193', '\u2199', '\u2190', '\u2196'];
  return arrows[Math.round(((deg % 360) + 360) % 360 / 45) % 8];
}

function busInfoHtml(v: LocalVehicle): string {
  const color = ROUTE_COLORS[v.routeId] || TC.statusMuted;
  const name = ROUTE_NAMES[v.routeId] || "";
  let status = "";
  if (v.status === "STOPPED_AT") status = "At stop";
  else if (v.status === "INCOMING_AT") status = "Approaching";
  else status = "In transit";
  if (v.nearStop) status += " \u2022 " + v.nearStop;
  let delay = "";
  if (v.delay != null) {
    if (v.delay >= -60 && v.delay <= 60) delay = "On time";
    else if (v.delay > 60) delay = Math.round(v.delay / 60) + "m late";
    else delay = Math.round(-v.delay / 60) + "m early";
  }
  return '<span class="info-route" style="background:' + color + '">' + v.routeId + '</span>' +
    ' <span class="info-id">#' + v.id + '</span>' +
    (name ? ' <span class="info-name">' + name + '</span>' : '') +
    ' <span class="info-detail">' + status + '</span>' +
    (delay ? ' <span class="info-delay">' + delay + '</span>' : '');
}

function stopPredictionsHtml(stopId: string, stopName: string, preds: {route_id: string; route_color: string; minutes_away: number; delay_seconds?: number | null}[]): string {
  let html = '<span class="info-stop-name">' + stopName + '</span>' +
    ' <span class="info-id">#' + stopId + '</span>';
  const seenRoutes: Record<string, boolean> = {};
  let shown = 0;
  for (let i = 0; i < preds.length && shown < 4; i++) {
    const p = preds[i];
    if (seenRoutes[p.route_id]) continue; // only first arrival per route
    seenRoutes[p.route_id] = true;
    shown++;
    const color = ROUTE_COLORS[p.route_id] || (p.route_color ? "#" + p.route_color : TC.statusMuted);
    const mins = p.minutes_away <= 0 ? "Now" : p.minutes_away + " min";
    let minsClass = "info-detail";
    if (p.delay_seconds != null) {
      if (p.delay_seconds >= 300) minsClass = "info-late";
      else if (p.delay_seconds <= -120) minsClass = "info-early";
    }
    html += ' <span class="info-route" style="background:' + color + '">' + p.route_id + '</span>' +
      ' <span class="info-detail">in</span> <span class="' + minsClass + '">' + mins + '</span>';
  }
  if (preds.length === 0) {
    html += ' <span class="info-detail">No upcoming arrivals</span>';
  }
  const alerts = dedupeAlerts(stopAlerts[stopId]);
  for (let j = 0; j < alerts.length; j++) {
    const a = alerts[j];
    const headerLinked = a.header ? linkifyStopIds(a.header) : '';
    const descLinked = a.description ? linkifyStopIds(a.description) : '';
    const fullText = a.header && a.description ? a.header + ' — ' + a.description : (a.header || a.description || '');
    html += ' <span class="info-alert" title="' + escapeHtml(fullText) + '">⚠ ' +
      (headerLinked ? '<strong>' + headerLinked + '</strong>' : '') +
      (headerLinked && descLinked ? ' — ' : '') +
      descLinked +
      '</span>';
  }
  return html;
}

function fetchStopPredictions(stopId: string, stopName: string, lock: boolean): void {
  const loadingHtml = '<span class="info-stop-name">' + stopName + '</span>' +
    ' <span class="info-id">#' + stopId + '</span>' +
    ' <span class="info-detail">Loading...</span>';
  if (lock) lockInfoBar(loadingHtml);
  else showInfoBar(loadingHtml);

  fetch("/api/transit/stop/" + encodeURIComponent(stopId) + "/predictions")
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (data) {
      if (!data) return;
      const html = stopPredictionsHtml(stopId, stopName, data.predictions || []);
      const el = document.getElementById("map-info-bar");
      if (el && el.classList.contains("info-bar-visible")) {
        el.innerHTML = html;
      }
    })
    .catch(function () { /* keep loading display */ });
}

function lockStopInfo(stopId: string, stopName: string): void {
  selectedStop = { id: stopId, name: stopName };
  fetchStopPredictions(stopId, stopName, true);
}

function clickToRoute(fromId: string, fromName: string, toId: string, toName: string): void {
  unlockInfoBar();
  const fromStop = allStops.find(function (s) { return s.stop_id === fromId; });
  const toStop = allStops.find(function (s) { return s.stop_id === toId; });
  if (!fromStop || !toStop) return;

  clearTripRoute();

  // Sync trip planner state so planURL() and Edit work
  tripFrom = { lat: fromStop.lat, lon: fromStop.lon, name: fromName, stopId: fromId };
  tripTo = { lat: toStop.lat, lon: toStop.lon, name: toName, stopId: toId };
  const fromInput = document.getElementById("trip-from") as HTMLInputElement | null;
  if (fromInput) fromInput.value = fromStop.stop_name + " #" + fromStop.stop_id;
  const toInput = document.getElementById("trip-to") as HTMLInputElement | null;
  if (toInput) toInput.value = toStop.stop_name + " #" + toStop.stop_id;

  const url = planURL(true);
  if (!url) return;

  const wrap = document.querySelector(".transit-map-wrap");
  if (wrap) htmx.ajax("GET", url, { target: wrap as Element, swap: "beforeend" });
}

// ---------------------------------------------------------------------------
// Bus markers
// ---------------------------------------------------------------------------

function statusRingColor(status: string): string {
  if (status === "STOPPED_AT") return TC.statusError;    // red — at stop
  if (status === "INCOMING_AT") return "#facc15";    // yellow — approaching
  return "white";                                    // in transit
}

function busIconSize(): number {
  if (!map) return 28;
  const z = map.getZoom();
  if (z >= 15) return 28;
  if (z >= 14) return 24;
  if (z >= 13) return 20;
  if (z >= 12) return 16;
  if (z >= 11) return 13;
  return 10;
}

function busIcon(routeId: string, bearing: number, delay: number | null, status: string): L.DivIcon {
  const sz = busIconSize();
  const half = sz / 2;
  const color = ROUTE_COLORS[routeId] || TC.statusMuted;
  const rotation = bearing || 0;
  const dimmed = (selectedRoute && routeId !== selectedRoute) ||
    (tripPlanRoutes && !tripPlanRoutes[routeId]);
  const opacity = dimmed ? 0.15 : 1;
  const ring = statusRingColor(status);
  const svg =
    '<svg xmlns="http://www.w3.org/2000/svg" width="' + sz + '" height="' + sz + '" viewBox="0 0 28 28" opacity="' + opacity + '">' +
    '<g transform="rotate(' + rotation + ' 14 14)">' +
    '<circle cx="14" cy="14" r="12" fill="' + color + '" stroke="' + ring + '" stroke-width="2.5"/>' +
    '<polygon points="14,4 10,16 14,14 18,16" fill="white" opacity="0.9"/>' +
    "</g></svg>";
  const label = routeId ? "Bus on route " + routeId : "Bus";
  return L.divIcon({
    html: '<span role="img" aria-label="' + label + '">' + svg + "</span>",
    className: "bus-marker no-chrome",
    iconSize: [sz, sz],
    iconAnchor: [half, half],
    popupAnchor: [0, -half],
  });
}

function updateMarkers(vehicles: LocalVehicle[]): void {
  const seen: Record<string, boolean> = {};

  for (let i = 0; i < vehicles.length; i++) {
    const v = vehicles[i];
    seen[v.id] = true;

    if (busMarkers[v.id]) {
      busMarkers[v.id].setLatLng([v.lat, v.lon]);
      busMarkers[v.id].setIcon(busIcon(v.routeId, v.bearing, v.delay, v.status));
    } else {
      busMarkers[v.id] = L.marker([v.lat, v.lon], {
        icon: busIcon(v.routeId, v.bearing, v.delay, v.status),
        keyboard: false,
      }).addTo(busLayer || map!);
      (function(vehicle: LocalVehicle) {
        const vid = vehicle.id, rid = vehicle.routeId;
        busMarkers[vid].on("mouseover", function () {
          showInfoBar(busInfoHtml(vehicle));
          if (!selectedRoute) {
            hoveredRoute = rid;
            restyleRouteLines();
          }
        });
        busMarkers[vid].on("mouseout", function () {
          hideInfoBar();
          if (!selectedRoute && hoveredRoute === rid) {
            hoveredRoute = null;
            restyleRouteLines();
          }
        });
        busMarkers[vid].on("click", function (e: L.LeafletEvent) {
          L.DomEvent.stopPropagation(e as L.LeafletMouseEvent);
          if (infoBarLocked) { unlockInfoBar(); return; }
          lockInfoBar(busInfoHtml(vehicle));
          if (selectedRoute !== rid) selectRoute(rid);
        });
      })(v);
    }
  }

  // Remove markers for vehicles no longer in feed
  for (const id in busMarkers) {
    if (!seen[id]) {
      if (busLayer) busLayer.removeLayer(busMarkers[id]);
      else map!.removeLayer(busMarkers[id]);
      delete busMarkers[id];
    }
  }
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

let prevStats: { active?: number; routes?: number } = {};

function updateStats(vehicles: LocalVehicle[]): void {
  const active = vehicles.filter(function (v) {
    return v.routeId;
  });
  const routes: Record<string, boolean> = {};
  active.forEach(function (v) {
    routes[v.routeId] = true;
  });

  const newActive = active.length;
  const newRoutes = Object.keys(routes).length;

  // Only update DOM (and trigger aria-live) when values actually change
  if (prevStats.active !== newActive) setStatValue("stat-active-buses", String(newActive));

  prevStats = { active: newActive, routes: newRoutes };
}

// ---------------------------------------------------------------------------
// Route grid
// ---------------------------------------------------------------------------

function routeSortKey(id: string): [number, string] {
  const match = id.match(/^(\d+)(.*)$/);
  if (match) return [parseInt(match[1], 10), match[2]];
  return [999, id];
}

function getRouteCounts(vehicles: LocalVehicle[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (let i = 0; i < vehicles.length; i++) {
    const r = vehicles[i].routeId;
    if (r) counts[r] = (counts[r] || 0) + 1;
  }
  return counts;
}

function updateRouteGrid(vehicles: LocalVehicle[]): void {
  const grid = document.getElementById("route-grid");
  if (!grid) return;

  const counts = getRouteCounts(vehicles);

  // Merge known routes + any new ones from feed, sort numerically
  const seen: Record<string, boolean> = {};
  const allIds: string[] = [];
  for (let i = 0; i < ALL_ROUTES.length; i++) {
    allIds.push(ALL_ROUTES[i]);
    seen[ALL_ROUTES[i]] = true;
  }
  const feedIds = Object.keys(counts);
  for (let i = 0; i < feedIds.length; i++) {
    if (!seen[feedIds[i]]) allIds.push(feedIds[i]);
  }
  allIds.sort(function (a, b) {
    const ka = routeSortKey(a), kb = routeSortKey(b);
    return ka[0] - kb[0] || ka[1].localeCompare(kb[1]);
  });

  let html = "";
  for (let i = 0; i < allIds.length; i++) {
    const id = allIds[i];
    const count = counts[id] || 0;
    const color = ROUTE_COLORS[id] || TC.statusMuted;
    const name = ROUTE_NAMES[id] || "";
    const isActive = count > 0;
    const isSelected = selectedRoute === id;
    const trips = cancelledTrips[id] || [];
    const upcomingCount = trips.filter(function (t) { return !!t.upcoming; }).length;
    const totalCancelled = trips.length;

    let cls = "route-pill";
    if (isSelected) cls += " route-pill-selected";
    if (!isActive) cls += " route-pill-inactive";

    // Right column: terminals (always shown when available)
    let rightCol = '';
    if (noServiceRoutes[id]) {
      rightCol = '<span class="route-pill-no-service">No Service</span>';
    } else {
      const terms = ROUTE_TERMINALS[id];
      if (terms && terms.length > 0) {
        rightCol = '<span class="route-pill-term-from">' + escapeHtml(terms[0]) + '</span>';
        if (terms.length > 1 && terms[1] !== terms[0]) {
          rightCol += '<span class="route-pill-term-to">' + escapeHtml(terms[1]) + '</span>';
        }
      }
    }

    // Footer: buses | cancelled | upcoming — KPI-style three-cell strip
    const pastCount = totalCancelled - upcomingCount;
    let footer = '<span class="route-pill-footer">' +
      '<span class="route-pill-ft' + (count > 0 ? ' ft-active' : '') + '">' +
        '<span class="route-pill-ft-val">' + count + '</span>' +
        '<span class="route-pill-ft-label">buses</span></span>' +
      '<span class="route-pill-ft-sep"></span>' +
      '<span class="route-pill-ft' + (pastCount > 0 ? ' ft-error' : '') + '"' +
        (pastCount > 0 ? ' data-cancel-route="' + escapeHtml(id) + '"' : '') + '>' +
        '<span class="route-pill-ft-val">' + pastCount + '</span>' +
        '<span class="route-pill-ft-label">cancel</span></span>' +
      '<span class="route-pill-ft-sep"></span>' +
      '<span class="route-pill-ft' + (upcomingCount > 0 ? ' ft-warn' : '') + '"' +
        (upcomingCount > 0 ? ' data-cancel-route="' + escapeHtml(id) + '"' : '') + '>' +
        '<span class="route-pill-ft-val">' + upcomingCount + '</span>' +
        '<span class="route-pill-ft-label">upcoming</span></span>' +
      '</span>';

    let titleParts = escapeHtml(name || id);
    if (isActive) titleParts += ' — ' + count + ' bus' + (count !== 1 ? 'es' : '');
    else titleParts += ' — no service';
    if (totalCancelled > 0) {
      titleParts += ' — ' + totalCancelled + ' cancellation' + (totalCancelled !== 1 ? 's' : '');
    }

    // long_name comes from internal/transit/route_long_names.go (curated
    // overlay) since Thunder Bay's GTFS feed leaves it empty. If the map
    // doesn't have the route, name == id and we skip the duplicate span.
    let nameSpan = '';
    if (name && name !== id) {
      nameSpan = '<span class="route-pill-name">' + escapeHtml(name) + '</span>';
    }
    html +=
      '<button type="button" class="' + cls + '" role="listitem" data-route="' + escapeHtml(id) + '"' +
      ' style="border-left-color:' + color + '"' +
      ' title="' + titleParts + '">' +
      '<span class="route-pill-id" style="color:' + color + '">' + escapeHtml(id) + '</span>' +
      nameSpan +
      rightCol +
      footer +
      '</button>';
  }

  grid.innerHTML = html;

  const pills = grid.querySelectorAll(".route-pill");
  for (let j = 0; j < pills.length; j++) {
    pills[j].addEventListener("click", onRoutePillClick);
  }

  // Cancel badge click -> modal
  const badges = grid.querySelectorAll("[data-cancel-route]");
  for (let b = 0; b < badges.length; b++) {
    badges[b].addEventListener("click", function (this: HTMLElement, e: Event) {
      e.stopPropagation();
      // Switch to cancellations tab
      const cb = document.getElementById("term-tab-cancels") as HTMLInputElement | null;
      if (cb) cb.checked = true;
    });
  }

  // Update buses panel (vehicles change each tick)
  updateBusesPanel();
}

function highlightTripPills(): void {
  const grid = document.getElementById("route-grid");
  if (!grid) return;
  const pills = grid.querySelectorAll(".route-pill");
  for (let i = 0; i < pills.length; i++) {
    const route = (pills[i] as HTMLElement).getAttribute("data-route");
    if (tripPlanRoutes && route && tripPlanRoutes[route]) {
      pills[i].classList.add("route-pill-trip");
    } else {
      pills[i].classList.remove("route-pill-trip");
    }
  }
}


function onRoutePillClick(this: HTMLElement, e: Event): void {
  const route = (e.currentTarget as HTMLElement).getAttribute("data-route");
  if (selectedRoute === route) {
    selectRoute(null);
  } else {
    selectRoute(route);
  }
}

function selectRoute(route: string | null): void {
  selectedRoute = route;

  // Update clear button visibility
  const clearBtn = document.getElementById("route-clear-filter");
  if (clearBtn) {
    clearBtn.style.display = route ? "" : "none";
  }

  // Re-render route grid with new selection state
  updateRouteGrid(lastVehicles);
  highlightTripPills();

  // Re-render map markers with dimming
  updateMarkers(lastVehicles);

  // Redraw route lines with highlight/dim
  drawRouteLines();

  // Show/hide inline schedule
  loadRouteSchedule(route);
}

// ---------------------------------------------------------------------------
// Route schedule
// ---------------------------------------------------------------------------

let scheduleRouteId: string | null = null;

function loadRouteSchedule(route: string | null): void {
  const container = document.getElementById("route-schedule-inline");
  if (!container) return;

  if (!route) {
    container.hidden = true;
    container.innerHTML = "";
    scheduleRouteId = null;
    return;
  }

  scheduleRouteId = route;
  container.hidden = false;

  container.innerHTML = '<p class="perf-loading">Loading schedule\u2026</p>';

  fetch('/transit/route/' + encodeURIComponent(route) + '?partial=schedule-today')
    .then(function (resp) {
      if (!resp.ok) throw new Error('HTTP ' + resp.status);
      return resp.text();
    })
    .then(function (html) {
      if (scheduleRouteId !== route) return;
      container.innerHTML = html;
      const badge = container.querySelector('#route-badge') as HTMLElement | null;
      if (badge) {
        badge.style.background = badge.dataset.bg || '';
        badge.style.color = badge.dataset.fg || '';
      }
    })
    .catch(function () {
      if (scheduleRouteId !== route) return;
      container.innerHTML = '<p class="perf-loading">Unable to load schedule.</p>';
    });
}

// Day picker: click a day button to reload schedule for that date
document.addEventListener('click', function (e: Event) {
  const btn = (e.target as HTMLElement).closest('.route-day-btn') as HTMLElement | null;
  if (!btn) return;
  const picker = btn.closest('.route-day-picker') as HTMLElement | null;
  if (!picker) return;
  const route = picker.getAttribute('data-route');
  const date = btn.getAttribute('data-date');
  if (!route || !date || date === picker.getAttribute('data-current')) return;

  // Update active button immediately
  const btns = picker.querySelectorAll('.route-day-btn');
  for (let i = 0; i < btns.length; i++) btns[i].classList.remove('route-day-active');
  btn.classList.add('route-day-active');
  picker.setAttribute('data-current', date);

  // Fade out body, swap content, fade back in
  const body = picker.parentElement?.querySelector('.route-schedule-body') as HTMLElement | null;
  if (!body) return;
  body.style.opacity = '0';

  const url = '/transit/route/' + encodeURIComponent(route) + '?partial=schedule&date=' + date;
  fetch(url)
    .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.text(); })
    .then(function (html) {
      const tmp = document.createElement('div');
      tmp.innerHTML = html;
      const inner = tmp.querySelector('.route-schedule-body');
      body.innerHTML = inner ? inner.innerHTML : html;
      body.style.opacity = '1';
    })
    .catch(function () {
      body.innerHTML = '<p class="perf-loading">Unable to load schedule.</p>';
      body.style.opacity = '1';
    });
});

// ---------------------------------------------------------------------------
// Utility DOM helpers
// ---------------------------------------------------------------------------

function setStatValue(id: string, value: string): void {
  const el = document.getElementById(id);
  if (el) el.textContent = value;
}

type StatusState = "live" | "dead" | "quiet";

function updateStatus(state: StatusState, text?: string): void {
  const el = document.getElementById("transit-status");
  if (!el) return;

  switch (state) {
    case "live":
      el.textContent = "Live";
      el.className = "transit-status";
      break;
    case "dead":
      el.textContent = "Offline";
      el.className = "transit-status transit-status-error";
      break;
    case "quiet":
      el.textContent = text || "";
      el.className = "transit-status transit-status-quiet";
      break;
  }
}

function escapeHtml(str: string): string {
  const div = document.createElement("div");
  div.textContent = str;
  return div.innerHTML;
}

// ---------------------------------------------------------------------------
// Stop markers & predictions
// ---------------------------------------------------------------------------

function loadStops(): void {
  // Fetch stops and time point data in parallel.
  const tpPromise = fetch(TIMEPOINTS_URL)
    .then(function (r) { return r.ok ? r.json() as Promise<Timepoint[]> : []; })
    .then(function (tps) {
      timepointData = {};
      for (let i = 0; i < tps.length; i++) {
        timepointData[tps[i].stop_id] = { routes: tps[i].routes, colors: tps[i].colors };
      }
    })
    .catch(function () { timepointData = {}; });

  const stopsPromise = fetch(STOPS_URL)
    .then(function (resp) {
      if (!resp.ok) throw new Error("HTTP " + resp.status);
      return resp.json() as Promise<Stop[]>;
    });

  Promise.all([tpPromise, stopsPromise])
    .then(function (results) { return results[1]; })
    .then(function (stops: Stop[]) {
      allStops = stops;
      stopLayer = L.layerGroup();
      hubLayer = L.layerGroup().addTo(map!);
      tpLayer = L.layerGroup(); // not added to map — starts hidden, toggled via layer bar

      for (let i = 0; i < stops.length; i++) {
        const s = stops[i];
        const dlat = s.lat;
        const dlon = s.lon;
        const hasAlert = !!stopAlerts[s.stop_id];
        const isHub = !!s.transfer;
        const isTP = !!timepointData[s.stop_id];
        const r = stopRadius();
        const targetLayer = isHub ? hubLayer! : stopLayer!;

        // Time point indicator: outer ring like hub rings, colored by first route
        if (isTP) {
          const tp = timepointData[s.stop_id];
          const tpColor = ROUTE_COLORS[tp.routes[0]] || (tp.colors[0] ? "#" + tp.colors[0] : TC.statusMuted);
          const dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
          L.circleMarker([dlat, dlon], {
            radius: r + 4,
            fillColor: tpColor,
            fillOpacity: dark ? 0.25 : 0.1,
            color: tpColor,
            weight: dark ? 3 : 2.5,
            interactive: false,
            dashArray: "4 3",
          }).addTo(tpLayer!);
        }

        // Hub ring: blue outline drawn first so it renders behind the dot
        if (isHub && !isTP) {
          L.circleMarker([dlat, dlon], {
            radius: r + 4,
            fillColor: TC.statusInfo,
            fillOpacity: 0.15,
            color: TC.statusInfo,
            weight: 3,
            interactive: false,
          }).addTo(hubLayer!);
        }

        // Terminal stops (City Hall, Waterfront) get a landmark icon
        if (s.is_terminal) {
          const nameLower = s.stop_name.toLowerCase();
          let landmarkSvg: string;
          if (nameLower.indexOf("city hall") !== -1) {
            // Lucide "landmark" — classic columns
            landmarkSvg = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="3" y1="22" x2="21" y2="22"/><line x1="6" y1="18" x2="6" y2="11"/><line x1="10" y1="18" x2="10" y2="11"/><line x1="14" y1="18" x2="14" y2="11"/><line x1="18" y1="18" x2="18" y2="11"/><polygon points="12 2 20 7 4 7"/><line x1="2" y1="18" x2="22" y2="18"/></svg>';
          } else {
            // Waterfront — Lucide "train-front" (terminal/station)
            landmarkSvg = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3.1V7a4 4 0 008 0V3.1"/><path d="M12 3a5 5 0 00-5 5v6a3 3 0 006 0"/><path d="M12 3a5 5 0 015 5v6a3 3 0 01-6 0"/><path d="M9 18l-3 4"/><path d="M15 18l3 4"/><circle cx="12" cy="12" r="1"/></svg>';
          }
          const lmIcon = L.divIcon({
            html: '<div class="landmark-circle">' + landmarkSvg + '</div>',
            className: "landmark-marker no-chrome",
            iconSize: [28, 28],
            iconAnchor: [14, 14],
            popupAnchor: [0, -14],
          });
          const lmMarker = L.marker([dlat, dlon], { icon: lmIcon, zIndexOffset: 2000 });

          markerData.set(lmMarker, {
            stopId: s.stop_id,
            stopName: s.stop_name,
            hasAlert: hasAlert,
            isHub: true,
            isTimepoint: isTP,
          });

          lmMarker.on("mouseover", function () {
            const el = lmMarker.getElement();
            if (el) el.querySelector('.landmark-circle')?.classList.add('landmark-hover');
            fetchStopPredictions(s.stop_id, s.stop_name, false);
          });
          lmMarker.on("mouseout", function () {
            const el = lmMarker.getElement();
            if (el) el.querySelector('.landmark-circle')?.classList.remove('landmark-hover');
            hideInfoBar();
          });
          hubLayer!.addLayer(lmMarker);
          continue;
        }

        const marker = L.circleMarker([dlat, dlon], {
          radius: hasAlert ? r + 2 : r,
          fillColor: isHub ? "#1e3a5f" : "#334e68",
          fillOpacity: 0.8,
          color: hasAlert ? TC.statusWarn : "white",
          weight: hasAlert ? 2.5 : 1.5,
        });

        // Store metadata in the markerData Map instead of property hacking
        markerData.set(marker, {
          stopId: s.stop_id,
          stopName: s.stop_name,
          hasAlert: hasAlert,
          isHub: isHub,
          isTimepoint: isTP,
        });

        marker.on("click", function (this: L.CircleMarker, e: L.LeafletEvent) {
          L.DomEvent.stopPropagation(e as L.LeafletMouseEvent);
          const md = getMarkerData(this);
          if (!md) return;
          if (infoBarLocked && selectedStop) {
            if (md.stopId === selectedStop.id) { unlockInfoBar(); }
            else { clickToRoute(selectedStop.id, selectedStop.name, md.stopId, md.stopName); }
            return;
          }
          lockStopInfo(md.stopId, md.stopName);
        });
        marker.on("mouseover", function (this: L.CircleMarker) {
          this.setStyle({ radius: (this.options.radius || r) + 3, fillOpacity: 1, weight: 2.5 });
          this.bringToFront();
          const md = getMarkerData(this);
          if (md) fetchStopPredictions(md.stopId, md.stopName, false);
        });
        marker.on("mouseout", function (this: L.CircleMarker) {
          const md = getMarkerData(this);
          const baseR = md && md.hasAlert ? stopRadius() + 2 : stopRadius();
          this.setStyle({ radius: baseR, fillOpacity: 0.8, weight: md && md.hasAlert ? 2.5 : 1.5 });
          hideInfoBar();
        });
        targetLayer.addLayer(marker);
      }
      // Civic landmarks (not transit stops, but important map features)
      const civicLandmarks = [
        { name: "Confederation College", lat: 48.40297, lon: -89.26967, icon: "grad-cap" },
        { name: "Lakehead University", lat: 48.42250, lon: -89.25900, icon: "grad-cap" },
        { name: "Bus Depot", lat: 48.4166, lon: -89.2374, icon: "warehouse" },
      ];

      const CIVIC_ICONS: Record<string, string> = {
        "grad-cap": '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M22 10v6M2 10l10-5 10 5-10 5z"/><path d="M6 12v5c0 2 2 3 6 3s6-1 6-3v-5"/></svg>',
        "warehouse": '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M22 8.35V20a2 2 0 01-2 2H4a2 2 0 01-2-2V8.35A2 2 0 013.26 6.5l8-3.2a2 2 0 011.48 0l8 3.2A2 2 0 0122 8.35z"/><path d="M6 18h12"/><path d="M6 14h12"/></svg>',
      };

      for (let ci = 0; ci < civicLandmarks.length; ci++) {
        const cl = civicLandmarks[ci];
        const civicIcon = L.divIcon({
          html: '<div class="landmark-circle">' + CIVIC_ICONS[cl.icon] + '</div>',
          className: "landmark-marker no-chrome",
          iconSize: [28, 28],
          iconAnchor: [14, 14],
          popupAnchor: [0, -14],
        });
        const civicMarker = L.marker([cl.lat, cl.lon], { icon: civicIcon, zIndexOffset: 2000 });
        civicMarker.on("mouseover", function () {
          const el = civicMarker.getElement();
          if (el) el.querySelector('.landmark-circle')?.classList.add('landmark-hover');
          showInfoBar('<span class="info-stop-name">' + cl.name + '</span>');
        });
        civicMarker.on("mouseout", function () {
          const el = civicMarker.getElement();
          if (el) el.querySelector('.landmark-circle')?.classList.remove('landmark-hover');
          hideInfoBar();
        });
        hubLayer!.addLayer(civicMarker);
      }

      updateStopVisibility();
      })
    .catch(function (err) {
      console.warn("Failed to load stops:", err);
    });
}

// Color stops that have buses stopped at them
function updateStopOccupancy(vehicles: LocalVehicle[]): void {
  if (!stopLayer && !hubLayer) return;

  // Build map: stopId -> [routeId, ...]
  const stoppedAt: Record<string, string[]> = {};
  for (let i = 0; i < vehicles.length; i++) {
    const v = vehicles[i];
    if (v.status === "STOPPED_AT" && v.stopId && v.routeId) {
      if (!stoppedAt[v.stopId]) stoppedAt[v.stopId] = [];
      stoppedAt[v.stopId].push(v.routeId);
    }
  }

  function styleStop(marker: L.CircleMarker): void {
    const md = getMarkerData(marker);
    if (!md) return;
    const sid = md.stopId;
    const routes = stoppedAt[sid];
    if (!routes || routes.length === 0) {
      // Reset to default
      marker.setStyle({
        fillColor: md.isHub ? "#1e3a5f" : "#334e68",
        color: md.hasAlert ? TC.statusWarn : "white",
      });
      return;
    }
    if (routes.length === 1) {
      // Single bus: fill with route color
      const c = ROUTE_COLORS[routes[0]] || TC.statusMuted;
      marker.setStyle({ fillColor: c, color: "white" });
    } else {
      // Multiple buses: use first route color as fill, second as stroke
      const c1 = ROUTE_COLORS[routes[0]] || TC.statusMuted;
      const c2 = ROUTE_COLORS[routes[1]] || TC.statusMuted;
      marker.setStyle({ fillColor: c1, color: c2, weight: 2.5 });
    }
  }

  if (stopLayer) stopLayer.eachLayer(function (m) { styleStop(m as L.CircleMarker); });
  if (hubLayer) hubLayer.eachLayer(function (m) {
    if (getMarkerData(m) && "setStyle" in m) styleStop(m as L.CircleMarker);
  });
}

function stopRadius(): number {
  if (!map) return 3;
  const z = map.getZoom();
  if (z >= 18) return 8;
  if (z >= 17) return 7;
  if (z >= 16) return 6;
  if (z >= 15) return 5;
  if (z >= 14) return 4;
  return 3;
}

function updateStopVisibility(): void {
  if (!map) return;
  const zoom = map.getZoom();
  const r = stopRadius();

  // Regular stops: only show at zoom >= 13 when network layer is on
  if (stopLayer) {
    if (zoom >= 13 && layerState.network) {
      if (!map.hasLayer(stopLayer)) stopLayer.addTo(map);
      stopLayer.eachLayer(function (m) {
        const cm = m as L.CircleMarker;
        if (cm.setRadius) {
          const md = getMarkerData(cm);
          cm.setRadius(md && md.hasAlert ? r + 2 : r);
        }
      });
    } else {
      if (map.hasLayer(stopLayer)) map.removeLayer(stopLayer);
    }
  }

  // Scale landmark markers with zoom — clamp their visual footprint
  const landmarkScale = zoom >= 15 ? 1 : zoom >= 14 ? 0.8 : zoom >= 13 ? 0.65 : 0.5;
  document.querySelectorAll('.landmark-circle').forEach(function (el) {
    (el as HTMLElement).style.transform = 'scale(' + landmarkScale + ')';
  });

  // Hubs: visible when network layer is on, just resize
  if (hubLayer) {
    if (layerState.network) {
      if (!map.hasLayer(hubLayer)) hubLayer.addTo(map);
      hubLayer.eachLayer(function (m) {
        const cm = m as L.CircleMarker;
        if (cm.setRadius) {
          if (!cm.options.interactive && cm.options.weight === 6) {
            cm.setRadius(r + 4);
          } else {
            const md = getMarkerData(cm);
            cm.setRadius(md && md.hasAlert ? r + 2 : r);
          }
        }
      });
    } else {
      if (map.hasLayer(hubLayer)) map.removeLayer(hubLayer);
    }
  }

  // Time points: visible at all zoom levels when toggled on, resize with zoom
  if (tpLayer) {
    if (layerState.timepoints) {
      if (!map.hasLayer(tpLayer)) tpLayer.addTo(map);
      tpLayer.eachLayer(function (m) {
        const cm = m as L.CircleMarker;
        if (cm.setRadius) cm.setRadius(r + 4);
      });
    } else {
      if (map.hasLayer(tpLayer)) map.removeLayer(tpLayer);
    }
  }
}

// ---------------------------------------------------------------------------
// My Location
// ---------------------------------------------------------------------------

let locationMarker: L.Marker | null = null;
let locationCircle: L.Circle | null = null;
let userLatLng: L.LatLng | null = null; // stored for periodic bus distance updates

function onLocateClick(openPlanner?: boolean): void {
  const btn = document.getElementById("locate-btn") || document.getElementById("map-locate-btn");
  if (btn) btn.classList.add("locate-btn-loading");
  // Safety net: if iOS stalls the geolocation callback (permission prompt, suspended
  // tab, silent failure), make sure the button can still be tapped again. 12s > the
  // 10s geolocation timeout so it only fires as a hard fallback.
  const safetyTimer = window.setTimeout(function () {
    if (btn) btn.classList.remove("locate-btn-loading");
  }, 12000);

  navigator.geolocation.getCurrentPosition(
    function (pos: GeolocationPosition) {
      clearTimeout(safetyTimer);
      if (btn) {
        btn.classList.remove("locate-btn-loading");
        btn.classList.add("active");
        setTimeout(function () { btn.classList.add("locate-fade"); }, 50);
        setTimeout(function () { btn.classList.remove("active", "locate-fade"); }, 1600);
      }
      userLatLng = L.latLng(pos.coords.latitude, pos.coords.longitude);
      const accuracy = pos.coords.accuracy;

      // Place or update the location marker
      if (locationMarker) {
        locationMarker.setLatLng(userLatLng);
        locationCircle!.setLatLng(userLatLng);
        locationCircle!.setRadius(accuracy);
      } else {
        locationCircle = L.circle(userLatLng, {
          radius: accuracy,
          color: TC.statusInfo,
          fillColor: TC.statusInfo,
          fillOpacity: 0.08,
          weight: 1,
          interactive: false,
        }).addTo(map!);

        locationMarker = L.marker(userLatLng, {
          icon: L.divIcon({
            html: '<span class="location-dot" role="img" aria-label="Your location"></span>',
            className: "location-marker no-chrome",
            iconSize: [18, 18],
            iconAnchor: [9, 9],
          }),
          interactive: false,
          zIndexOffset: 1000,
        }).addTo(map!);
      }

      map!.setView(userLatLng, Math.max(map!.getZoom(), 14), { animate: true });

      // Pulse the dot on same timing as the locate button
      const dot = locationMarker!.getElement()?.querySelector(".location-dot") as HTMLElement | null;
      if (dot) {
        dot.classList.remove("locate-pulse-fade");
        dot.classList.add("locate-pulse");
        setTimeout(function () { dot.classList.add("locate-pulse-fade"); }, 50);
        setTimeout(function () { dot.classList.remove("locate-pulse", "locate-pulse-fade"); }, 1600);
      }

      if (openPlanner) {
        // Resolve nearest stop and wire into trip planner
        const nearestStop = findNearestStop(userLatLng.lat, userLatLng.lng);
        const fromLabel = nearestStop ? nearestStop.stop_name + " #" + nearestStop.stop_id : "My Location";
        tripFrom = { lat: userLatLng.lat, lon: userLatLng.lng, name: fromLabel, stopId: nearestStop ? nearestStop.stop_id : null };
        const fromInput = document.getElementById("trip-from") as HTMLInputElement | null;
        if (fromInput) fromInput.value = fromLabel;
        const fromWrap = fromInput && fromInput.closest(".trip-search-wrap") as HTMLElement | null;
        if (fromWrap) fromWrap.classList.add("trip-located");

        if (btn) btn.classList.add("trip-locate-active");

        if (tripFromMarker) tripFromMarker.setLatLng(userLatLng);
        else tripFromMarker = L.circleMarker(userLatLng, {
          radius: 7, fillColor: TC.statusOk, fillOpacity: 1, color: "white", weight: 2,
        }).addTo(map!);

        updateTripGoBtn();
        openTripPlanner();
        if (!tripTo) {
          setTimeout(function () {
            const toInput = document.getElementById("trip-to") as HTMLInputElement | null;
            if (toInput) toInput.focus();
          }, 350);
        }
      }
    },
    function (err: GeolocationPositionError) {
      clearTimeout(safetyTimer);
      if (btn) btn.classList.remove("locate-btn-loading");
      let msg = "Unable to get location";
      if (err.code === 1) msg = "Location permission denied";
      else if (err.code === 2) msg = "Location unavailable";
      else if (err.code === 3) msg = "Location request timed out";
      // Show error in info bar briefly
      lockInfoBar('<span class="info-late">' + msg + '</span>');
      setTimeout(unlockInfoBar, 3000);
    },
    { enableHighAccuracy: true, timeout: 10000, maximumAge: 30000 }
  );
}

// ---------------------------------------------------------------------------
// Trip planner
// ---------------------------------------------------------------------------

interface TripEndpoint {
  lat: number;
  lon: number;
  name: string;
  stopId?: string | null;
}

let tripPlanRoutes: Record<string, boolean> | null = null; // set of route IDs used by active trip plan
let tripFrom: TripEndpoint | null = null;
let tripTo: TripEndpoint | null = null;
let tripFromMarker: L.CircleMarker | null = null;
let tripToMarker: L.CircleMarker | null = null;
let tripRouteLayer: L.LayerGroup | null = null;

function initTripPlanner(): void {
  // Open/close is handled by #trip-toggle checkbox + CSS — no JS needed
  // When a route is drawn, Find Route button acts like Edit
  const findRouteLabel = document.getElementById('find-route-btn');
  if (findRouteLabel) {
    findRouteLabel.addEventListener('click', function (e) {
      if (tripRouteLayer) {
        e.preventDefault();
        hideRouteSummaryBar();
        openTripPlanner();
      }
    });
  }

  const goBtn = document.getElementById("trip-go");
  if (goBtn) goBtn.addEventListener("click", doTripPlan);

  const randomBtn = document.getElementById("trip-random");
  if (randomBtn) randomBtn.addEventListener("click", randomTrip);

  // Default time input to now
  const timeInput = document.getElementById("trip-time") as HTMLInputElement | null;
  if (timeInput) {
    const now = new Date();
    timeInput.value = ("0" + now.getHours()).slice(-2) + ":" + ("0" + now.getMinutes()).slice(-2);
  }

  // Wire search inputs
  wireStopSearch("trip-from", "trip-from-results", function (stop: Stop) {
    selectTripStop("from", stop);
  }, true);
  wireStopSearch("trip-to", "trip-to-results", function (stop: Stop) {
    selectTripStop("to", stop);
  }, false);

  // HTMX swap handler — draw route on map after server renders results
  document.body.addEventListener("htmx:afterSwap", function (e: Event) {
    const detail = (e as CustomEvent).detail;
    const planEl = document.getElementById("plan-data");
    if (!planEl) return;
    let plan: PlanResponse;
    try { plan = JSON.parse(planEl.textContent || "{}"); } catch (_) { return; }
    planEl.remove();

    if (!plan.itineraries || plan.itineraries.length === 0) return;
    drawTripRoute(plan.itineraries[0]);

    const goBtn = document.getElementById("trip-go") as HTMLButtonElement | null;
    if (goBtn) goBtn.disabled = false;

    // Summary bar: wire Edit/Clear buttons
    const summaryBar = document.getElementById("route-summary-bar");
    if (summaryBar) {
      const findRouteLabel = document.getElementById('find-route-btn');
      if (findRouteLabel) findRouteLabel.classList.add('active');

      const focusBtn = summaryBar.querySelector(".route-summary-focus");
      if (focusBtn) focusBtn.addEventListener("click", function (ev: Event) {
        ev.stopPropagation();
        if (tripRouteLayer && map) {
          const bounds = L.latLngBounds([]);
          tripRouteLayer.eachLayer(function (l) {
            if ((l as L.Polyline).getBounds) bounds.extend((l as L.Polyline).getBounds());
            else if ((l as L.CircleMarker).getLatLng) bounds.extend((l as L.CircleMarker).getLatLng());
          });
          if (bounds.isValid()) map.fitBounds(bounds, { padding: [40, 40] });
        }
      });
      const editBtn = summaryBar.querySelector(".route-summary-edit");
      if (editBtn) editBtn.addEventListener("click", function (ev: Event) {
        ev.stopPropagation();
        hideRouteSummaryBar();
        openTripPlanner();
      });
      const closeBtn = summaryBar.querySelector(".route-summary-close");
      if (closeBtn) closeBtn.addEventListener("click", function (ev: Event) {
        ev.stopPropagation();
        hideRouteSummaryBar();
        clearTripRoute();
      });
    }

    // Accordion: wire header toggle + "See route on map" button
    const target = detail.target as HTMLElement;
    if (target.id === "trip-results") {
      target.addEventListener("click", function (ev: Event) {
        const header = (ev.target as HTMLElement).closest(".trip-itin-header") as HTMLElement | null;
        if (header) {
          const item = header.parentElement as HTMLElement;
          const body = item.querySelector(".trip-itin-body") as HTMLElement;
          const wasOpen = body.classList.contains("open");
          target.querySelectorAll(".trip-itin-body").forEach(function (b) { b.classList.remove("open"); });
          target.querySelectorAll(".trip-itin-header").forEach(function (h) { h.classList.remove("active"); });
          if (!wasOpen) {
            body.classList.add("open");
            header.classList.add("active");
          }
          return;
        }
        const seeBtn = (ev.target as HTMLElement).closest(".trip-see-route") as HTMLElement | null;
        if (seeBtn) {
          setTripPlannerOpen(false);
          // Fetch summary bar via HTMX
          const summaryUrl = planURL(true);
          const wrap = document.querySelector(".transit-map-wrap");
          if (summaryUrl && wrap) htmx.ajax("GET", summaryUrl, { target: wrap as Element, swap: "beforeend" });
        }
      });

    }
  });
}

function wireStopSearch(inputId: string, resultsId: string, onSelect: (stop: Stop) => void, showLocate: boolean): void {
  const input = document.getElementById(inputId) as HTMLInputElement | null;
  const resultsEl = document.getElementById(resultsId) as HTMLElement | null;
  if (!input || !resultsEl) return;

  let debounce: ReturnType<typeof setTimeout> | null = null;

  input.addEventListener("input", function () {
    if (debounce !== null) clearTimeout(debounce);
    // Clear drawn route when user edits an endpoint
    if (tripRouteLayer) {
      clearTripRoute();
      hideRouteSummaryBar();
    }
    if (inputId === "trip-from") { tripFrom = null; }
    else { tripTo = null; }
    const goBtn = document.getElementById("trip-go") as HTMLButtonElement | null;
    if (goBtn) goBtn.disabled = true;
    // Clear locate state when from field is manually edited
    if (inputId === "trip-from") {
      const locBtn = document.getElementById("locate-btn");
      if (locBtn) locBtn.classList.remove("trip-locate-active");
      const wrap = input.closest(".trip-search-wrap") as HTMLElement | null;
      if (wrap) wrap.classList.remove("trip-located");
    }
    const q = input.value.trim();
    if (q.length < 2) {
      resultsEl.hidden = true;
      return;
    }
    debounce = setTimeout(function () {
      const matches = searchStops(q);
      if (matches.length === 0) {
        resultsEl.hidden = true;
        return;
      }
      let html = "";
      for (let i = 0; i < matches.length; i++) {
        const s = matches[i];
        html += '<button type="button" class="trip-search-item" data-idx="' + i + '">';
        html += '<span class="trip-search-name">' + escapeHtml(s.stop_name) + '</span>';
        html += '<span class="trip-search-id">#' + escapeHtml(s.stop_id) + '</span>';
        html += '</button>';
      }
      resultsEl.innerHTML = html;
      resultsEl.hidden = false;

      // Wire click handlers
      const items = resultsEl.querySelectorAll(".trip-search-item");
      for (let j = 0; j < items.length; j++) {
        (function (stop: Stop) {
          items[j].addEventListener("mousedown", function (e: Event) {
            e.preventDefault();
            resultsEl.hidden = true;
            input.value = stop.stop_name + " #" + stop.stop_id;
            onSelect(stop);
            input.blur();
          });
        })(matches[j]);
      }
    }, 150);
  });

  // Sorted stop list for arrow key enumeration
  let sortedStops: Stop[] | null = null;
  let enumIdx = -1;

  function getSortedStops(): Stop[] {
    if (sortedStops) return sortedStops;
    sortedStops = allStops.slice().sort(function (a, b) {
      return a.stop_name.localeCompare(b.stop_name);
    });
    return sortedStops;
  }

  input.addEventListener("keydown", function (e: KeyboardEvent) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      // If dropdown is open, navigate items
      const items = resultsEl.querySelectorAll(".trip-search-item");
      if (!resultsEl.hidden && items.length > 0) {
        const active = resultsEl.querySelector(".trip-search-active") as HTMLElement | null;
        let idx = -1;
        for (let i = 0; i < items.length; i++) {
          if (items[i] === active) idx = i;
        }
        if (active) active.classList.remove("trip-search-active");
        if (e.key === "ArrowDown") idx = (idx + 1) % items.length;
        else idx = (idx - 1 + items.length) % items.length;
        items[idx].classList.add("trip-search-active");
        items[idx].scrollIntoView({ block: "nearest" });
        return;
      }
      // Empty or no dropdown — enumerate all stops
      const stops = getSortedStops();
      if (stops.length === 0) return;
      if (e.key === "ArrowDown") enumIdx = (enumIdx + 1) % stops.length;
      else enumIdx = (enumIdx - 1 + stops.length) % stops.length;
      const s = stops[enumIdx];
      input.value = s.stop_name + " #" + s.stop_id;
      onSelect(s);
    } else if (e.key === "Enter") {
      const activeItem = resultsEl.querySelector(".trip-search-active") as HTMLElement | null;
      if (activeItem && !resultsEl.hidden) {
        e.preventDefault();
        activeItem.dispatchEvent(new Event("mousedown"));
      }
    }
  });

  input.addEventListener("blur", function () {
    setTimeout(function () { resultsEl.hidden = true; }, 200);
  });

  input.addEventListener("focus", function () {
    enumIdx = -1;
    // Select all text on focus so user can easily retype
    input.select();
    // Always show defaults on focus — user can type to search
    showDefaultStops(resultsEl, input, onSelect, showLocate);
  });
}

const locateIcon = '<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"></circle><circle cx="12" cy="12" r="3"></circle><line x1="12" y1="2" x2="12" y2="6"></line><line x1="12" y1="18" x2="12" y2="22"></line><line x1="2" y1="12" x2="6" y2="12"></line><line x1="18" y1="12" x2="22" y2="12"></line></svg>';

function showDefaultStops(resultsEl: HTMLElement, input: HTMLInputElement, onSelect: (stop: Stop) => void, showLocate: boolean): void {
  let html = "";

  // Show transfer hubs first, then highest route-count stops
  const hubs: Stop[] = [];
  const regular: Stop[] = [];
  for (let i = 0; i < allStops.length; i++) {
    const s = allStops[i];
    if (s.transfer) hubs.push(s);
    else if (s.routes >= 3) regular.push(s);
  }
  // Sort hubs by route count desc, then take top regular
  hubs.sort(function (a, b) { return (b.routes || 0) - (a.routes || 0); });
  regular.sort(function (a, b) { return (b.routes || 0) - (a.routes || 0); });
  const stops = hubs.concat(regular).slice(0, 8);

  // "My location" blended in as the first option
  if (showLocate) {
    html += '<button type="button" class="trip-search-item" data-locate="1">';
    html += '<span class="trip-search-name">' + locateIcon + ' Use my location</span>';
    html += '</button>';
  }

  if (stops.length > 0) {
    html += '<div class="trip-search-hint">Popular stops</div>';
    for (let j = 0; j < stops.length; j++) {
      const s = stops[j];
      html += '<button type="button" class="trip-search-item" data-idx="' + j + '">';
      html += '<span class="trip-search-name">' + escapeHtml(s.stop_name) + '</span>';
      html += '<span class="trip-search-id">#' + escapeHtml(s.stop_id) + '</span>';
      html += '</button>';
    }
  }

  if (!html) return;
  resultsEl.innerHTML = html;
  resultsEl.hidden = false;

  // Wire locate option
  const locateOpt = resultsEl.querySelector("[data-locate]");
  if (locateOpt) {
    locateOpt.addEventListener("mousedown", function (e: Event) {
      e.preventDefault();
      resultsEl.hidden = true;
      onLocateClick(true);
      input.blur();
    });
  }

  // Wire stop items
  const items = resultsEl.querySelectorAll(".trip-search-item:not([data-locate])");
  for (let k = 0; k < items.length; k++) {
    (function (stop: Stop) {
      items[k].addEventListener("mousedown", function (e: Event) {
        e.preventDefault();
        resultsEl.hidden = true;
        input.value = stop.stop_name + " #" + stop.stop_id;
        onSelect(stop);
        input.blur();
      });
    })(stops[k]);
  }
}

function findNearestStop(lat: number, lng: number): Stop | null {
  if (!allStops || allStops.length === 0) return null;
  let best: Stop | null = null;
  let bestDist = Infinity;
  for (let i = 0; i < allStops.length; i++) {
    const s = allStops[i];
    const dlat = s.lat - lat;
    const dlon = s.lon - lng;
    const d = dlat * dlat + dlon * dlon;
    if (d < bestDist) {
      bestDist = d;
      best = s;
    }
  }
  return best;
}

function searchStops(query: string): Stop[] {
  const q = query.toLowerCase();
  const results: Stop[] = [];
  for (let i = 0; i < allStops.length; i++) {
    const s = allStops[i];
    if (s.stop_name.toLowerCase().indexOf(q) !== -1 || s.stop_id.indexOf(q) !== -1) {
      results.push(s);
      if (results.length >= 8) break;
    }
  }
  return results;
}

function selectTripStop(which: 'from' | 'to', stop: Stop): void {
  const latlng = { lat: stop.lat, lon: stop.lon };
  const label = stop.stop_name + " #" + stop.stop_id;

  if (which === "from") {
    tripFrom = { lat: latlng.lat, lon: latlng.lon, name: label, stopId: stop.stop_id };
    if (tripFromMarker) tripFromMarker.setLatLng([latlng.lat, latlng.lon]);
    else tripFromMarker = L.circleMarker([latlng.lat, latlng.lon], {
      radius: 7, fillColor: TC.statusOk, fillOpacity: 1, color: "white", weight: 2,
    }).addTo(map!);
  } else {
    tripTo = { lat: latlng.lat, lon: latlng.lon, name: label, stopId: stop.stop_id };
    if (tripToMarker) tripToMarker.setLatLng([latlng.lat, latlng.lon]);
    else tripToMarker = L.circleMarker([latlng.lat, latlng.lon], {
      radius: 7, fillColor: TC.statusError, fillOpacity: 1, color: "white", weight: 2,
    }).addTo(map!);
  }

  // Sync hidden inputs for HTMX form
  const prefix = which === "from" ? "trip-from" : "trip-to";
  const latEl = document.getElementById(prefix + "-lat") as HTMLInputElement | null;
  const lonEl = document.getElementById(prefix + "-lon") as HTMLInputElement | null;
  const stopEl = document.getElementById(prefix + "-stop") as HTMLInputElement | null;
  if (latEl) latEl.value = String(latlng.lat);
  if (lonEl) lonEl.value = String(latlng.lon);
  if (stopEl) stopEl.value = stop.stop_id;

  map!.flyTo([latlng.lat, latlng.lon], Math.max(map!.getZoom(), 14), { duration: 0.8 });
  updateTripGoBtn();

  // Focus the next empty field
  if (which === "to" && !tripFrom) {
    const fromInput = document.getElementById("trip-from") as HTMLInputElement | null;
    if (fromInput) fromInput.focus();
  } else if (which === "from" && !tripTo) {
    const toInput = document.getElementById("trip-to") as HTMLInputElement | null;
    if (toInput) toInput.focus();
  }
}

function setTripPlannerOpen(open: boolean): void {
  const cb = document.getElementById("trip-toggle") as HTMLInputElement | null;
  if (cb) cb.checked = open;
}

function openTripPlanner(): void {
  setTripPlannerOpen(true);
  hideRouteSummaryBar();
  // Enable Go button and run search if both endpoints are set (e.g. from click-to-route)
  if (tripFrom && tripTo) {
    const btn = document.getElementById("trip-go") as HTMLButtonElement | null;
    if (btn) btn.disabled = false;
    doTripPlan();
  }
}

function hideRouteSummaryBar(): void {
  const bar = document.getElementById("route-summary-bar");
  if (bar) bar.remove();
  const findRouteLabel = document.getElementById('find-route-btn');
  if (findRouteLabel) findRouteLabel.classList.remove('active');
}


function updateTripGoBtn(): void {
  const btn = document.getElementById("trip-go") as HTMLButtonElement | null;
  if (btn) btn.disabled = !(tripFrom && tripTo);
  // Auto-search when both fields are filled
  if (tripFrom && tripTo) {
    doTripPlan();
  }
}

function randomTrip(): void {
  if (allStops.length < 2) return;
  const a = allStops[Math.floor(Math.random() * allStops.length)];
  let b = a;
  while (b.stop_id === a.stop_id) {
    b = allStops[Math.floor(Math.random() * allStops.length)];
  }
  selectTripStop("from", a);
  const fromInput = document.getElementById("trip-from") as HTMLInputElement | null;
  if (fromInput) fromInput.value = a.stop_name + " #" + a.stop_id;
  selectTripStop("to", b);
  const toInput = document.getElementById("trip-to") as HTMLInputElement | null;
  if (toInput) toInput.value = b.stop_name + " #" + b.stop_id;

  // Open planner and auto-search
  openTripPlanner();
  setTimeout(doTripPlan, 50);
}

function planURL(summary: boolean): string {
  if (!tripFrom || !tripTo) return "";
  let url = PLAN_URL +
    "?from_lat=" + tripFrom.lat + "&from_lon=" + tripFrom.lon +
    "&to_lat=" + tripTo.lat + "&to_lon=" + tripTo.lon +
    "&partial=plan";
  if (tripFrom.stopId) url += "&from_stop=" + encodeURIComponent(tripFrom.stopId);
  if (tripTo.stopId) url += "&to_stop=" + encodeURIComponent(tripTo.stopId);
  if (summary) url += "&summary=1";

  const timeVal = document.getElementById("trip-time") as HTMLInputElement | null;
  const modeRadio = document.querySelector('input[name="trip-time-mode"]:checked') as HTMLInputElement | null;
  const modeVal = modeRadio ? modeRadio.value : "depart";
  const timeStr = timeVal ? timeVal.value : "";

  if (modeVal === "arrive" && timeStr) {
    url += "&arrive_by=" + timeStr;
  } else if (timeStr) {
    url += "&depart=" + timeStr;
  } else {
    const now = new Date();
    url += "&depart=" + ("0" + now.getHours()).slice(-2) + ":" + ("0" + now.getMinutes()).slice(-2);
  }
  return url;
}

function doTripPlan(): void {
  if (!tripFrom || !tripTo) return;
  const url = planURL(false);
  if (!url) return;

  const results = document.getElementById("trip-results");
  if (results) results.innerHTML = '<p class="nearby-loading">Finding route...</p>';
  const intro = document.getElementById("trip-planner-intro");
  if (intro) intro.hidden = true;

  clearTripRoute();
  htmx.ajax("GET", url, { target: "#trip-results", swap: "innerHTML" });
}

// ---------------------------------------------------------------------------
// Draw trip route on map
// ---------------------------------------------------------------------------

function drawTripRoute(itin: Itinerary): void {
  clearTripRoute();
  tripRouteLayer = L.layerGroup().addTo(map!);

  // Collect route IDs used by this itinerary
  tripPlanRoutes = {};
  for (let r = 0; r < itin.legs.length; r++) {
    if (itin.legs[r].type === "transit" && itin.legs[r].route_id) {
      tripPlanRoutes[itin.legs[r].route_id] = true;
    }
  }

  // Highlight route pills used by this trip
  highlightTripPills();

  // Re-render bus markers with dimming
  updateMarkers(lastVehicles);

  // Dim all route lines while showing trip
  for (const rid in routeLines) {
    routeLines[rid].eachLayer(function (layer) {
      if ((layer as L.Polyline).setStyle) (layer as L.Polyline).setStyle({ opacity: 0.08 });
    });
  }

  // Draw lines and collect key points for labels
  const bounds = L.latLngBounds([]);
  const transitLegsArr: { leg: Leg; from: L.LatLng; to: L.LatLng }[] = [];

  for (let i = 0; i < itin.legs.length; i++) {
    const leg = itin.legs[i];
    const from = L.latLng(leg.from.lat, leg.from.lon);
    const to = L.latLng(leg.to.lat, leg.to.lon);
    bounds.extend(from);
    bounds.extend(to);

    if (leg.type === "walk") {
      // Walk halo (contrast)
      L.polyline([from, to], {
        color: TC.termBg, weight: 7, opacity: 0.85,
        lineCap: "round", lineJoin: "round",
      }).addTo(tripRouteLayer);
      // Walk dashes on top
      L.polyline([from, to], {
        color: TC.termFgDim, weight: 4, dashArray: "2,8", opacity: 1,
        lineCap: "round", lineJoin: "round",
      }).addTo(tripRouteLayer);
      if (leg.distance_m >= 100) {
        const mid = L.latLng((from.lat + to.lat) / 2, (from.lng + to.lng) / 2);
        const dist = leg.distance_m >= 1000 ? (leg.distance_m / 1000).toFixed(1) + "km" : Math.round(leg.distance_m) + "m";
        L.marker(mid, {
          icon: L.divIcon({
            html: '<span class="trip-walk-label">' + dist + '</span>',
            className: "trip-walk-label-wrap no-chrome",
            iconSize: [0, 0],
            iconAnchor: [0, 4],
          }),
          interactive: false,
          zIndexOffset: 400,
        }).addTo(tripRouteLayer);
      }
    } else {
      transitLegsArr.push({ leg: leg, from: from, to: to });
      const color = ROUTE_COLORS[leg.route_id] || TC.statusMuted;
      const shapeCoords = extractShapeSegment(leg.route_id, from, to);
      // Halo (contrast outline) — drawn first, underneath
      L.polyline(shapeCoords, {
        color: TC.termBg, weight: 11, opacity: 0.9,
        lineCap: "round", lineJoin: "round",
      }).addTo(tripRouteLayer);
      // Main colored stroke on top
      L.polyline(shapeCoords, {
        color: color, weight: 7, opacity: 1,
        lineCap: "round", lineJoin: "round",
      }).addTo(tripRouteLayer);
      // Arrows: open chevrons (less ink), larger and more frequent
      LDecorator.polylineDecorator(shapeCoords, {
        patterns: [{
          offset: "8%", repeat: "12%",
          symbol: LDecorator.Symbol.arrowHead({
            pixelSize: 14, polygon: false,
            pathOptions: { stroke: true, fill: false, color: TC.termBg, weight: 3, opacity: 0.95, lineCap: "round", lineJoin: "round" },
          }),
        }],
      }).addTo(tripRouteLayer);

      // Board/alight dots (with drop-shadow via className)
      L.circleMarker(from, {
        radius: 7, fillColor: "white", fillOpacity: 1, color: color, weight: 3.5,
        className: "trip-leg-dot",
      }).addTo(tripRouteLayer);
      L.circleMarker(to, {
        radius: 7, fillColor: color, fillOpacity: 1, color: "white", weight: 2.5,
        className: "trip-leg-dot",
      }).addTo(tripRouteLayer);
    }
  }

  // Start label (at origin)
  const firstLeg = itin.legs[0];
  if (firstLeg) {
    const startPt = L.latLng(firstLeg.from.lat, firstLeg.from.lon);
    const startText = itin.leave_by ? "Leave " + itin.leave_by : "Start";
    addTimeLabel(startPt, startText, TC.statusOk);
  }

  // Board + transfer labels at each transit leg start
  for (let t = 0; t < transitLegsArr.length; t++) {
    const tl = transitLegsArr[t];
    const tColor = ROUTE_COLORS[tl.leg.route_id] || TC.statusMuted;
    if (t === 0) {
      // First boarding
      addTimeLabel(tl.from,
        "\u2776 Board " + tl.leg.route_id + " " + tl.leg.departure,
        tColor);
    } else {
      // Transfer
      addTimeLabel(tl.from,
        "\u2776 Transfer to " + tl.leg.route_id + " " + tl.leg.departure,
        tColor);
    }
  }

  // Arrive label
  const lastLeg = itin.legs[itin.legs.length - 1];
  if (lastLeg) {
    const endPt = L.latLng(lastLeg.to.lat, lastLeg.to.lon);
    addTimeLabel(endPt, "Arrive " + itin.arrival, TC.statusError);
  }

  if (bounds.isValid()) {
    map!.fitBounds(bounds, { padding: [50, 50] });
  }
}

function addTimeLabel(latlng: L.LatLng, text: string, color: string): void {
  L.marker(latlng, {
    icon: L.divIcon({
      html: '<span class="trip-time-label" style="background:' + color + '">' + escapeHtml(text) + '</span>',
      className: "trip-time-label-wrap no-chrome",
      iconSize: [0, 0],
      iconAnchor: [0, -8],
    }),
    interactive: false,
    zIndexOffset: 500,
  }).addTo(tripRouteLayer!);
}

// Find the segment of a route shape between two points
function extractShapeSegment(routeId: string, fromLatLng: L.LatLng, toLatLng: L.LatLng): L.LatLngExpression[] {
  let bestShape: readonly (readonly [number, number])[] | null = null;
  let bestScore = Infinity;

  // Find the shape for this route that best matches from->to
  for (let i = 0; i < routeShapes.length; i++) {
    const shape = routeShapes[i];
    if (shape.route_id !== routeId) continue;
    const coords = shape.coordinates;
    if (!coords || coords.length < 2) continue;

    const fromIdx = nearestPointIdx(coords, fromLatLng);
    const toIdx = nearestPointIdx(coords, toLatLng);

    // Shape must go from->to in order (not backwards)
    if (fromIdx >= toIdx) continue;

    const score = pointDist(coords[fromIdx], fromLatLng) + pointDist(coords[toIdx], toLatLng);
    if (score < bestScore) {
      bestScore = score;
      bestShape = coords.slice(fromIdx, toIdx + 1);
    }
  }

  // Fallback: straight line
  if (!bestShape) return [fromLatLng, toLatLng];
  return bestShape as unknown as L.LatLngExpression[];
}

function nearestPointIdx(coords: readonly (readonly [number, number])[], latlng: L.LatLng): number {
  let bestIdx = 0;
  let bestDist = Infinity;
  for (let i = 0; i < coords.length; i++) {
    const d = pointDist(coords[i], latlng);
    if (d < bestDist) {
      bestDist = d;
      bestIdx = i;
    }
  }
  return bestIdx;
}

function pointDist(coord: readonly [number, number], latlng: L.LatLng): number {
  const dlat = coord[0] - latlng.lat;
  const dlon = coord[1] - latlng.lng;
  return dlat * dlat + dlon * dlon;
}

function clearTripRoute(): void {
  if (tripRouteLayer) {
    map!.removeLayer(tripRouteLayer);
    tripRouteLayer = null;
  }
  tripPlanRoutes = null;
  highlightTripPills();
  // Restore route line opacity and bus markers
  for (const rid in routeLines) {
    routeLines[rid].eachLayer(function (layer) {
      if ((layer as L.Polyline).setStyle) (layer as L.Polyline).setStyle({ opacity: 0.4 });
    });
  }
  updateMarkers(lastVehicles);
}

// ---------------------------------------------------------------------------
// Buses modal
// ---------------------------------------------------------------------------

function busDelayInfo(v: LocalVehicle): { text: string; cls: string } {
  if (v.delay == null) return { text: '', cls: '' };
  if (v.delay >= -60 && v.delay <= 300) return { text: 'On time', cls: 'info-detail' };
  if (v.delay < -60) return { text: Math.round(Math.abs(v.delay) / 60) + 'm early', cls: 'info-early' };
  return { text: Math.round(v.delay / 60) + 'm late', cls: 'info-late' };
}

function busStatusText(v: LocalVehicle): string {
  let s = '';
  if (v.status === 'STOPPED_AT') s = 'At stop';
  else if (v.status === 'INCOMING_AT') s = 'Approaching';
  else s = 'In transit';
  if (v.nearStop) s += ' \u2022 ' + v.nearStop;
  if (v.speed > 0) s += ' \u2022 ' + Math.round(v.speed * 3.6) + ' km/h ' + bearingArrow(v.bearing);
  return s;
}

function buildBusRow(v: LocalVehicle): HTMLElement {
  const color = ROUTE_COLORS[v.routeId] || TC.statusMuted;
  const name = ROUTE_NAMES[v.routeId] || '';
  const delay = busDelayInfo(v);

  const row = document.createElement('div');
  row.className = 'stat-modal-row';
  row.dataset.busId = v.id;

  const badge = document.createElement('span');
  badge.className = 'stat-modal-route';
  badge.style.background = color;
  badge.textContent = v.routeId;

  const detail = document.createElement('span');
  detail.className = 'stat-modal-detail';

  const top = document.createElement('span');
  top.className = 'stat-modal-bus-top';
  if (name) {
    const nameEl = document.createElement('span');
    nameEl.className = 'info-name';
    nameEl.textContent = name;
    top.appendChild(nameEl);
  }
  top.appendChild(document.createTextNode(' #' + v.id));
  if (delay.text) {
    const delayEl = document.createElement('span');
    delayEl.className = delay.cls;
    delayEl.textContent = delay.text;
    top.appendChild(document.createTextNode(' '));
    top.appendChild(delayEl);
  }

  const sub = document.createElement('span');
  sub.className = 'stat-modal-sub';
  sub.textContent = busStatusText(v);

  detail.appendChild(top);
  detail.appendChild(sub);

  const focus = document.createElement('button');
  focus.type = 'button';
  focus.className = 'stat-modal-focus';
  focus.title = 'Focus on map';
  focus.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><line x1="12" y1="2" x2="12" y2="6"/><line x1="12" y1="18" x2="12" y2="22"/><line x1="2" y1="12" x2="6" y2="12"/><line x1="18" y1="12" x2="22" y2="12"/></svg>';
  focus.addEventListener('click', function () {
    if (map) {
      const wrap = document.querySelector('.transit-map-wrap');
      if (wrap) wrap.scrollIntoView({ behavior: 'smooth', block: 'center' });
      map.flyTo([v.lat, v.lon], Math.max(map.getZoom(), 15), { duration: 1.2, easeLinearity: 0.25 });
      showInfoBar(busInfoHtml(v));
    }
  });

  row.appendChild(badge);
  row.appendChild(detail);
  row.appendChild(focus);
  return row;
}

function updateBusesPanel(): void {
  const el = document.getElementById("terminal-buses-list");
  if (!el) return;
  const sorted = lastVehicles.slice().filter(function (v) { return !!v.routeId; });
  sorted.sort(function (a, b) {
    const ka = routeSortKey(a.routeId), kb = routeSortKey(b.routeId);
    return ka[0] - kb[0] || ka[1].localeCompare(kb[1]);
  });

  el.innerHTML = '';
  if (sorted.length === 0) {
    el.innerHTML = '<p class="info-detail">No active buses</p>';
    return;
  }
  for (let i = 0; i < sorted.length; i++) {
    el.appendChild(buildBusRow(sorted[i]));
  }
}

function buildCancelRow(rid: string, t: CancelledTrip): HTMLElement {
  const color = ROUTE_COLORS[rid] || TC.statusMuted;
  const name = ROUTE_NAMES[rid] || '';
  const timeRange = (t.start_time || '') + (t.end_time ? ' \u2013 ' + t.end_time : '');

  const wrap = document.createElement('div');
  wrap.className = 'cancel-row-wrap';

  const row = document.createElement('div');
  row.className = 'stat-modal-row';

  const badge = document.createElement('span');
  badge.className = 'stat-modal-route';
  badge.style.background = color;
  badge.textContent = rid;

  const detail = document.createElement('span');
  detail.className = 'stat-modal-detail';

  const top = document.createElement('span');
  top.className = 'stat-modal-bus-top';
  if (name) {
    const nameEl = document.createElement('span');
    nameEl.className = 'info-name';
    nameEl.textContent = name;
    top.appendChild(nameEl);
  }
  if (t.headsign) {
    const headsignEl = document.createElement('span');
    headsignEl.className = 'cancel-headsign';
    headsignEl.textContent = ' to ' + t.headsign;
    top.appendChild(headsignEl);
  }

  const sub = document.createElement('span');
  sub.className = 'stat-modal-sub';
  if (timeRange) {
    const timeEl = document.createElement('span');
    timeEl.className = 'cancel-time-range';
    timeEl.textContent = timeRange;
    sub.appendChild(timeEl);
  }
  if (t.first_seen) {
    if (timeRange) {
      const sep = document.createElement('span');
      sep.className = 'cancel-info-sep';
      sep.textContent = '\u00B7';
      sub.appendChild(sep);
    }
    const label = document.createElement('span');
    label.className = 'cancel-info-label';
    label.textContent = 'First reported';
    sub.appendChild(label);

    const time = document.createElement('span');
    time.className = 'cancel-info-time';
    time.textContent = t.first_seen;
    sub.appendChild(time);

    if (t.lead_min != null) {
      const m = Math.abs(t.lead_min);
      let dur: string;
      if (m >= 60) {
        const h = Math.floor(m / 60);
        const rem = m % 60;
        dur = rem === 0 ? h + 'h' : h + 'h ' + rem + 'm';
      } else {
        dur = m + ' min';
      }
      const rel = t.lead_min > 0 ? dur + ' before' : (t.lead_min < 0 ? dur + ' after' : 'at departure');

      const sep = document.createElement('span');
      sep.className = 'cancel-info-sep';
      sep.textContent = '\u2014';
      sub.appendChild(sep);

      const notice = document.createElement('span');
      notice.className = 'cancel-info-notice';
      notice.textContent = rel;
      sub.appendChild(notice);
    }
  }

  detail.appendChild(top);
  detail.appendChild(sub);

  row.appendChild(badge);
  row.appendChild(detail);

  wrap.appendChild(row);
  return wrap;
}

function updateCancelsPanel(): void {
  const el = document.getElementById("terminal-cancels-list");
  if (!el) return;

  type Entry = { rid: string; trip: CancelledTrip };
  const upcoming: Entry[] = [];
  const previous: Entry[] = [];
  for (const rid in cancelledTrips) {
    const trips = cancelledTrips[rid];
    for (let i = 0; i < trips.length; i++) {
      const entry = { rid, trip: trips[i] };
      if (trips[i].upcoming) upcoming.push(entry);
      else previous.push(entry);
    }
  }

  const byTime = function (a: Entry, b: Entry): number {
    const at = a.trip.start_time || '';
    const bt = b.trip.start_time || '';
    if (at !== bt) return at.localeCompare(bt);
    const ka = routeSortKey(a.rid), kb = routeSortKey(b.rid);
    return ka[0] - kb[0] || ka[1].localeCompare(kb[1]);
  };
  upcoming.sort(byTime);
  previous.sort(byTime);

  el.innerHTML = '';
  if (upcoming.length === 0 && previous.length === 0) {
    el.innerHTML = '<p class="info-detail">No cancellations today</p>';
    return;
  }

  const appendSection = function (title: string, entries: Entry[], headerCls: string) {
    if (entries.length === 0) return;
    const header = document.createElement('div');
    header.className = 'cancel-section-header ' + headerCls;
    header.textContent = title + ' \u00B7 ' + entries.length;
    el.appendChild(header);
    for (let i = 0; i < entries.length; i++) {
      el.appendChild(buildCancelRow(entries[i].rid, entries[i].trip));
    }
  };

  appendSection('Upcoming', upcoming, 'cancel-section-upcoming');
  appendSection('Previous', previous, 'cancel-section-previous');
}

// ---------------------------------------------------------------------------
// Terminal clock
// ---------------------------------------------------------------------------

function startTerminalClock(): void {
  const el = document.getElementById("terminal-time");
  if (!el) return;
  const dateEl = document.getElementById("terminal-date");
  if (dateEl) {
    dateEl.textContent = new Date().toLocaleDateString("en-US", {
      month: "long", day: "numeric", timeZone: "America/Thunder_Bay",
    });
  }
  function tick(): void {
    el!.textContent = new Date().toLocaleTimeString([], {
      hour: "2-digit", minute: "2-digit", timeZone: "America/Thunder_Bay", hour12: false,
    });
  }
  tick();
  setInterval(tick, 10_000);
}

// ---------------------------------------------------------------------------
// Initialize when DOM is ready
// ---------------------------------------------------------------------------

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", initMap);
} else {
  initMap();
}
