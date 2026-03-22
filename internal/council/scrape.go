package council

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	baseURL     = "https://pub-thunderbay.escribemeetings.com"
	meetingType = "City Council"
	pageSize    = 50
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// pastMeetingsResponse is the eSCRIBE API response envelope.
type pastMeetingsResponse struct {
	D struct {
		Meetings   []apiMeeting `json:"Meetings"`
		TotalCount int          `json:"TotalCount"`
	} `json:"d"`
}

type apiMeeting struct {
	ID           string        `json:"Id"`
	DateLong     string        `json:"DateLong"`
	MeetingType  string        `json:"MeetingType"`
	MeetingLinks []meetingLink `json:"MeetingLinks"`
}

type meetingLink struct {
	Title  string `json:"Title"`
	Type   string `json:"Type"`
	Format string `json:"Format"`
	URL    string `json:"Url"`
}

// ListMeetings fetches all City Council meetings from the eSCRIBE API.
// Returns only meetings that have a PostMinutes PDF link.
func ListMeetings() ([]Meeting, error) {
	var all []Meeting

	for page := 1; ; page++ {
		body, err := json.Marshal(map[string]any{
			"type":       meetingType,
			"pageNumber": page,
		})
		if err != nil {
			return nil, fmt.Errorf("marshaling request: %w", err)
		}

		req, err := http.NewRequest("POST", baseURL+"/MeetingsCalendarView.aspx/PastMeetings", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching page %d: %w", page, err)
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading page %d: %w", page, err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("page %d: status %d", page, resp.StatusCode)
		}

		var result pastMeetingsResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("parsing page %d: %w", page, err)
		}

		for _, m := range result.D.Meetings {
			mtg := convertMeeting(m)
			if mtg.MinutesURL != "" {
				all = append(all, mtg)
			}
		}

		fetched := page * pageSize
		if fetched >= result.D.TotalCount {
			break
		}

		time.Sleep(200 * time.Millisecond)
	}

	return all, nil
}

// convertMeeting transforms an API meeting into our model.
func convertMeeting(m apiMeeting) Meeting {
	mtg := Meeting{
		ID:    m.ID,
		Title: m.MeetingType,
	}

	// Parse date from DateLong (e.g., "March 25, 2024")
	if t, err := time.Parse("January 2, 2006", m.DateLong); err == nil {
		mtg.Date = t.Format("2006-01-02")
		mtg.PDFFile = t.Format("2006-01-02") + ".pdf"
	} else {
		mtg.Date = m.DateLong
	}

	// Find PostMinutes PDF link
	for _, link := range m.MeetingLinks {
		if link.Type == "PostMinutes" && link.Format == ".pdf" {
			mtg.MinutesURL = baseURL + "/" + link.URL
			break
		}
	}

	return mtg
}

// DownloadPDF downloads a minutes PDF to destDir.
// Skips if the file already exists (idempotent).
// Returns the full path to the downloaded file.
func DownloadPDF(url, destDir, filename string) (string, error) {
	destPath := filepath.Join(destDir, filename)

	if _, err := os.Stat(destPath); err == nil {
		return destPath, nil // already exists
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", filename, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: status %d", filename, resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "pdf") && !strings.Contains(ct, "octet-stream") {
		return "", fmt.Errorf("downloading %s: unexpected content-type %q", filename, ct)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", filename, err)
	}

	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", filename, err)
	}

	return destPath, nil
}

// TermForDate returns the council term label for a given meeting date.
// The 2018-2022 term was dropped — pre-cutoff dates return "" so the
// fetcher and importers can skip them.
func TermForDate(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	// 2022-2026 term inaugurated ~Nov 15, 2022
	cutoff := time.Date(2022, 11, 15, 0, 0, 0, 0, time.UTC)
	if t.Before(cutoff) {
		return ""
	}
	return "2022-2026"
}
