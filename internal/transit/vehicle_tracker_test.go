package transit

import (
	"math"
	"testing"
)

// Thunder Bay city hall area — the exact values don't matter, but using real
// coordinates keeps the mPerDegLon term realistic.
const (
	testCenterLat = 48.3809
	testCenterLon = -89.2477
)

// offsetM returns a (lat, lon) pair offset from the test center by the given
// north (meters) and east (meters). Uses the same flat-Earth approximation
// the helper uses, so the offsets round-trip cleanly through it.
func offsetM(northM, eastM float64) (lat, lon float64) {
	const mPerDegLat = 111_000.0
	cosLat := math.Cos(testCenterLat * math.Pi / 180)
	mPerDegLon := mPerDegLat * cosLat
	return testCenterLat + northM/mPerDegLat, testCenterLon + eastM/mPerDegLon
}

func TestSegmentCircleCrossing_EntryCase(t *testing.T) {
	// Segment from 100m north to 0m (center). Radius 50m → segment enters
	// the circle at 50m from center, fraction 0.5 along the segment.
	aLat, aLon := offsetM(100, 0)
	bLat, bLon := offsetM(0, 0)
	frac, ok := segmentCircleCrossing(aLat, aLon, bLat, bLon, testCenterLat, testCenterLon, 50)
	if !ok {
		t.Fatal("expected crossing, got ok=false")
	}
	if math.Abs(frac-0.5) > 0.01 {
		t.Fatalf("expected fraction ~0.5, got %f", frac)
	}
}

func TestSegmentCircleCrossing_ExitCase(t *testing.T) {
	// Segment from center to 100m north. Radius 50m → segment exits the
	// circle at fraction 0.5.
	aLat, aLon := offsetM(0, 0)
	bLat, bLon := offsetM(100, 0)
	frac, ok := segmentCircleCrossing(aLat, aLon, bLat, bLon, testCenterLat, testCenterLon, 50)
	if !ok {
		t.Fatal("expected crossing, got ok=false")
	}
	if math.Abs(frac-0.5) > 0.01 {
		t.Fatalf("expected fraction ~0.5, got %f", frac)
	}
}

func TestSegmentCircleCrossing_GrazingMiss(t *testing.T) {
	// Segment runs east-west at 100m north of center. Radius 50m — never
	// touches the circle.
	aLat, aLon := offsetM(100, -200)
	bLat, bLon := offsetM(100, 200)
	if _, ok := segmentCircleCrossing(aLat, aLon, bLat, bLon, testCenterLat, testCenterLon, 50); ok {
		t.Fatal("expected no crossing, got ok=true")
	}
}

func TestSegmentCircleCrossing_BothInside(t *testing.T) {
	// Segment from (10m N) to (20m N). Radius 50m — both endpoints inside.
	// Quadratic has no real roots in [0,1] (both would be outside the
	// segment), so expect ok=false.
	aLat, aLon := offsetM(10, 0)
	bLat, bLon := offsetM(20, 0)
	if _, ok := segmentCircleCrossing(aLat, aLon, bLat, bLon, testCenterLat, testCenterLon, 50); ok {
		t.Fatal("expected no crossing for both-inside segment, got ok=true")
	}
}

func TestSegmentCircleCrossing_ZeroLength(t *testing.T) {
	aLat, aLon := offsetM(30, 0)
	if _, ok := segmentCircleCrossing(aLat, aLon, aLat, aLon, testCenterLat, testCenterLon, 50); ok {
		t.Fatal("expected ok=false for zero-length segment")
	}
}

func TestSegmentCircleCrossing_ChordBothOutside(t *testing.T) {
	// Segment from 200m west to 200m east through the center. Radius 50m →
	// two crossings at ±50m east. The helper returns the first crossing
	// (entry) near fraction 0.375 (150m east of start out of 400m total).
	aLat, aLon := offsetM(0, -200)
	bLat, bLon := offsetM(0, 200)
	frac, ok := segmentCircleCrossing(aLat, aLon, bLat, bLon, testCenterLat, testCenterLon, 50)
	if !ok {
		t.Fatal("expected chord crossing, got ok=false")
	}
	if math.Abs(frac-0.375) > 0.01 {
		t.Fatalf("expected fraction ~0.375, got %f", frac)
	}
}
