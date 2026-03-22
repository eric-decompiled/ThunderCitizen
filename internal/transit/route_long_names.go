package transit

// Curated long names for Thunder Bay Transit routes.
//
// Thunder Bay's GTFS feed leaves `route_long_name` empty for every route —
// only `route_short_name` (the route_id) is published. Without these names
// the route grid shows the id twice, which looks broken. We maintain the
// list ourselves against the official names on
// https://www.thunderbay.ca/en/city-services/schedules-and-maps.aspx and
// the deriveRoute step in derive.go overlays them onto the empty GTFS
// long_name during every GTFS reload, so the data survives a re-fetch.
//
// If a route appears in GTFS that isn't in this map, deriveRoute leaves
// its long_name empty (fall-through to short_name in the AllRouteMeta
// query). Add new routes here when Thunder Bay Transit publishes them.
var routeLongNames = map[string]string{
	"1":  "Mainline",
	"2":  "Crosstown",
	"3C": "County Park",
	"3J": "Jumbo Gardens",
	"3M": "Memorial",
	"4":  "Neebing",
	"5":  "Edward",
	"6":  "Mission",
	"7":  "Hudson",
	"8":  "James",
	"9":  "Junot",
	"10": "Northwood",
	"11": "John",
	"12": "East End",
	"13": "John-Jumbo",
	"14": "Arthur",
	"15": "Beverly",
	"16": "Balmoral",
	"17": "Current River",
	"18": "Westfort",
}
