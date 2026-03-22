// Protomaps vector tile themes for ThunderCitizen.
// Solarized light + phosphor green dark, matching the site's terminal aesthetic.
// Usage: var tiles = ThunderMapTiles(); tiles.addTo(map);

/* global protomapsL */
/* eslint-disable no-unused-vars */

(function () {
  'use strict';

  var PMTILES_URL = '/static/thunderbay.pmtiles';
  var ATTRIBUTION = '© <a href="https://openstreetmap.org/copyright">OpenStreetMap</a> · <a href="https://protomaps.com">Protomaps</a>';

  // --- Solarized Light theme ---
  // Cream bg (#fdf6e3), olive accent (#546e00), warm grey text
  var solarized = {
    background: "#f5eedc",
    earth: "#fdf6e3",
    park_a: "#e8e4c9",
    park_b: "#d5d0a8",
    hospital: "#f0e6d6",
    industrial: "#ede8d5",
    school: "#f0e6d6",
    wood_a: "#e0dcc0",
    wood_b: "#d0cca8",
    pedestrian: "#f0ebda",
    scrub_a: "#e4e0c8",
    scrub_b: "#d8d4b0",
    glacier: "#eee8d5",
    sand: "#ede7d0",
    beach: "#ede7d0",
    aerodrome: "#eee8d5",
    runway: "#e0dbc8",
    water: "#93a1a1",
    zoo: "#e4e0c8",
    military: "#eee8d5",
    tunnel_other_casing: "#e0dbc8",
    tunnel_minor_casing: "#e0dbc8",
    tunnel_link_casing: "#e0dbc8",
    tunnel_major_casing: "#e0dbc8",
    tunnel_highway_casing: "#e0dbc8",
    tunnel_other: "#ede8d5",
    tunnel_minor: "#ede8d5",
    tunnel_link: "#ede8d5",
    tunnel_major: "#ede8d5",
    tunnel_highway: "#ede8d5",
    pier: "#e0dbc8",
    buildings: "#e0dbc8",
    minor_service_casing: "#e0dbc8",
    minor_casing: "#e0dbc8",
    link_casing: "#e0dbc8",
    major_casing_late: "#d6d1be",
    highway_casing_late: "#d6d1be",
    other: "#f0ebda",
    minor_service: "#f0ebda",
    minor_a: "#f0ebda",
    minor_b: "#fdf6e3",
    link: "#fdf6e3",
    major_casing_early: "#e0dbc8",
    major: "#fdf6e3",
    highway_casing_early: "#d6d1be",
    highway: "#fdf6e3",
    railway: "#b8b0a0",
    boundaries: "#93a1a1",
    bridges_other_casing: "#e0dbc8",
    bridges_minor_casing: "#e0dbc8",
    bridges_link_casing: "#e0dbc8",
    bridges_major_casing: "#d6d1be",
    bridges_highway_casing: "#d6d1be",
    bridges_other: "#f0ebda",
    bridges_minor: "#fdf6e3",
    bridges_link: "#fdf6e3",
    bridges_major: "#fdf6e3",
    bridges_highway: "#fdf6e3",
    roads_label_minor: "#657b83",
    roads_label_minor_halo: "#fdf6e3",
    roads_label_major: "#586e75",
    roads_label_major_halo: "#fdf6e3",
    ocean_label: "#657b83",
    subplace_label: "#839496",
    subplace_label_halo: "#fdf6e3",
    city_label: "#475b65",
    city_label_halo: "#fdf6e3",
    state_label: "#93a1a1",
    state_label_halo: "#fdf6e3",
    country_label: "#839496",
    address_label: "#839496",
    address_label_halo: "#fdf6e3",
  };

  // --- Phosphor Green Dark theme ---
  // Near-black green bg (#0d1a0d), phosphor green accent (#4ade80)
  var phosphor = {
    background: "#0a140a",
    earth: "#0d1a0d",
    park_a: "#0f200f",
    park_b: "#122412",
    hospital: "#141e14",
    industrial: "#0f180f",
    school: "#141e14",
    wood_a: "#0f200f",
    wood_b: "#112211",
    pedestrian: "#101a10",
    scrub_a: "#0f1f0f",
    scrub_b: "#112211",
    glacier: "#121c12",
    sand: "#111b11",
    beach: "#131d13",
    aerodrome: "#101a10",
    runway: "#1a2a1a",
    water: "#0a2a1a",
    zoo: "#0f200f",
    military: "#101a10",
    tunnel_other_casing: "#0a120a",
    tunnel_minor_casing: "#0a120a",
    tunnel_link_casing: "#0a120a",
    tunnel_major_casing: "#0a120a",
    tunnel_highway_casing: "#0a120a",
    tunnel_other: "#142014",
    tunnel_minor: "#142014",
    tunnel_link: "#142014",
    tunnel_major: "#142014",
    tunnel_highway: "#142014",
    pier: "#1a261a",
    buildings: "#081008",
    minor_service_casing: "#0d1a0d",
    minor_casing: "#0d1a0d",
    link_casing: "#0d1a0d",
    major_casing_late: "#0d1a0d",
    highway_casing_late: "#0d1a0d",
    other: "#162016",
    minor_service: "#162016",
    minor_a: "#1a281a",
    minor_b: "#162016",
    link: "#1a281a",
    major_casing_early: "#0d1a0d",
    major: "#1a281a",
    highway_casing_early: "#0d1a0d",
    highway: "#1e2e1e",
    railway: "#1a2a1a",
    boundaries: "#2a4a2a",
    bridges_other_casing: "#0d1a0d",
    bridges_minor_casing: "#0d1a0d",
    bridges_link_casing: "#0d1a0d",
    bridges_major_casing: "#0d1a0d",
    bridges_highway_casing: "#0d1a0d",
    bridges_other: "#162016",
    bridges_minor: "#162016",
    bridges_link: "#1a281a",
    bridges_major: "#1a281a",
    bridges_highway: "#1e2e1e",
    roads_label_minor: "#2d6e2d",
    roads_label_minor_halo: "#0d1a0d",
    roads_label_major: "#3a8a3a",
    roads_label_major_halo: "#0d1a0d",
    ocean_label: "#2a5a2a",
    subplace_label: "#2d6e2d",
    subplace_label_halo: "#0d1a0d",
    city_label: "#4ade80",
    city_label_halo: "#0d1a0d",
    state_label: "#1a4a1a",
    state_label_halo: "#0d1a0d",
    country_label: "#2a5a2a",
    address_label: "#2d6e2d",
    address_label_halo: "#0d1a0d",
  };

  function prefersDark() {
    if (document.documentElement.dataset.theme === 'dark') return true;
    if (document.documentElement.dataset.theme === 'light') return false;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
  }

  function createLayer(theme) {
    return protomapsL.leafletLayer({
      url: PMTILES_URL,
      paintRules: protomapsL.paintRules(theme),
      labelRules: protomapsL.labelRules(theme, 'en'),
      backgroundColor: theme.background,
      attribution: ATTRIBUTION,
    });
  }

  // Public API: returns an object with addTo(map) and swap() for theme changes.
  window.ThunderMapTiles = function () {
    var currentLayer = null;
    var currentMap = null;
    var currentTheme = null;

    function apply(map) {
      var theme = prefersDark() ? phosphor : solarized;
      if (theme === currentTheme && currentLayer) return;
      if (currentLayer && currentMap) currentMap.removeLayer(currentLayer);
      currentLayer = createLayer(theme);
      currentLayer.addTo(map);
      currentLayer.bringToBack();
      currentTheme = theme;
      currentMap = map;
    }

    return {
      addTo: function (map) {
        currentMap = map;
        apply(map);
        return this;
      },
      swap: function () {
        if (currentMap) apply(currentMap);
      },
      getAttribution: function () {
        return ATTRIBUTION;
      },
    };
  };
})();
