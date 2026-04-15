package views

import (
	"sort"
	"strings"
)

// NotFoundViewModel is rendered at the themed 404 page. Matches are
// ranked against the known public routes by Levenshtein edit distance.
type NotFoundViewModel struct {
	Method    string
	Path      string
	Matches   []RouteMatch
	RegistryN int
}

// RouteMatch is one registry entry ranked against the requested path.
// Distance is the Levenshtein edit distance (lower = closer).
type RouteMatch struct {
	Path     string
	Label    string
	Distance int
}

// registry is populated once at startup by SetNotFoundRegistry from the
// chi router — never written again, so no lock is needed in the
// request-path read below.
var registry []RouteMatch

// SetNotFoundRegistry records the list of public paths the 404 page
// should rank against. Call from main after the router is built, before
// Listen. Paths are deduplicated; labels are derived from the last
// path segment.
func SetNotFoundRegistry(paths []string) {
	seen := make(map[string]bool, len(paths))
	list := make([]RouteMatch, 0, len(paths))
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		list = append(list, RouteMatch{Path: p, Label: deriveLabel(p)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	registry = list
}

// NewNotFoundViewModel ranks the registry against the requested path
// and returns the top 10 matches. Ties broken by path length (shorter
// first), then alphabetical.
func NewNotFoundViewModel(method, path string) NotFoundViewModel {
	norm := strings.ToLower(strings.TrimRight(path, "/"))
	if norm == "" {
		norm = "/"
	}

	scored := make([]RouteMatch, len(registry))
	for i, r := range registry {
		r.Distance = levenshtein(norm, strings.ToLower(r.Path))
		scored[i] = r
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Distance != scored[j].Distance {
			return scored[i].Distance < scored[j].Distance
		}
		if len(scored[i].Path) != len(scored[j].Path) {
			return len(scored[i].Path) < len(scored[j].Path)
		}
		return scored[i].Path < scored[j].Path
	})

	top := 10
	if len(scored) < top {
		top = len(scored)
	}

	return NotFoundViewModel{
		Method:    method,
		Path:      path,
		Matches:   scored[:top],
		RegistryN: len(registry),
	}
}

// deriveLabel turns a path into a short human label for the match
// table. "/" → "Home"; multi-segment paths use the last segment.
func deriveLabel(path string) string {
	if path == "/" {
		return "Home"
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	last := parts[len(parts)-1]
	if last == "" {
		return "Home"
	}
	last = strings.ReplaceAll(last, "-", " ")
	last = strings.ReplaceAll(last, "_", " ")
	return strings.ToUpper(last[:1]) + last[1:]
}

// levenshtein returns the minimum edit distance between a and b using
// the classic two-row dynamic programming variant. Case handling is
// the caller's job — pass pre-lowered strings.
func levenshtein(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min3(
				prev[j]+1,      // deletion
				curr[j-1]+1,    // insertion
				prev[j-1]+cost, // substitution
			)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
