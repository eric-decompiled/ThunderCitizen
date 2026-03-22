package models

import (
	"net/url"
	"strings"
)

// SourceKind identifies the type of a citation source.
// Auto-detected from URL by Kind() — no need to set manually.
type SourceKind string

const (
	SourceTBNews SourceKind = "tbnews" // tbnewswatch.com
	SourceCBC    SourceKind = "cbc"    // cbc.ca
	SourceCity   SourceKind = "city"   // thunderbay.ca, tbdssab.ca
	SourceGitHub SourceKind = "github" // github.com
	SourcePDF    SourceKind = "pdf"    // .pdf URLs (when not a known publisher)
	SourceData   SourceKind = "data"   // government data portals
	SourceCode   SourceKind = "code"   // code references
	SourceMedia  SourceKind = "media"  // video, podcasts
	SourceWeb    SourceKind = "web"    // generic fallback
)

// SourceRef is a deep-linkable citation to a source.
// The URL includes any fragment for direct navigation (#page=5, #:~:text=..., #L42).
// Note is a human-readable location label displayed alongside the link ("p. 160").
type SourceRef struct {
	URL   string `json:"url"`             // full URL with fragment
	Label string `json:"label,omitempty"` // display text (empty → auto from Kind)
	Note  string `json:"note,omitempty"`  // human location label shown next to link
}

// Kind auto-detects the source type from the URL.
// Publisher identity (TBNews, CBC, City) trumps file format (PDF).
func (r SourceRef) Kind() SourceKind {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" {
		return SourceWeb
	}
	host := strings.ToLower(strings.TrimPrefix(u.Host, "www."))

	switch {
	case strings.HasSuffix(host, "tbnewswatch.com"):
		return SourceTBNews
	case strings.HasSuffix(host, "cbc.ca"):
		return SourceCBC
	case strings.HasSuffix(host, "thunderbay.ca"):
		return SourceCity
	case strings.HasSuffix(host, "tbdssab.ca"):
		return SourceCity
	case strings.HasSuffix(host, "github.com"):
		return SourceGitHub
	case strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be"):
		return SourceMedia
	case strings.HasSuffix(strings.SplitN(r.URL, "#", 2)[0], ".pdf"):
		return SourcePDF
	default:
		return SourceWeb
	}
}

// knownLabels maps source kinds to nice display names.
var knownLabels = map[SourceKind]string{
	SourceTBNews: "TBNewsWatch",
	SourceCBC:    "CBC News",
	SourceCity:   "City of Thunder Bay",
	SourceGitHub: "GitHub",
}

// DisplayLabel returns the Label, or a nice name for known sources,
// or extracts a short domain from the URL.
func (r SourceRef) DisplayLabel() string {
	if r.Label != "" {
		return r.Label
	}
	if label, ok := knownLabels[r.Kind()]; ok {
		return label
	}
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" {
		return r.URL
	}
	host := strings.TrimPrefix(u.Host, "www.")
	// TBDSSAB gets its own label despite sharing SourceCity kind
	if strings.HasSuffix(host, "tbdssab.ca") {
		return "TBDSSAB"
	}
	return host
}
