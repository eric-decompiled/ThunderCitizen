package council

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// CheckPdftotext verifies that pdftotext is available on the system.
func CheckPdftotext() error {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return fmt.Errorf("pdftotext not found; install with: brew install poppler (macOS) or apt install poppler-utils (Linux)")
	}
	return nil
}

// ExtractText runs pdftotext on a PDF and returns the text content.
func ExtractText(pdfPath string) (string, error) {
	cmd := exec.Command("pdftotext", "-layout", pdfPath, "-")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pdftotext %s: %w", pdfPath, err)
	}
	return string(out), nil
}

var (
	reRecordedVote = regexp.MustCompile(`(?i)(recorded vote|re-vote)\s+was\s+requested`)
	rePageHeaderV1 = regexp.MustCompile(`(?i)^City Council\s+[–-]`)
	rePageHeaderV2 = regexp.MustCompile(`(?i)^\s*PAGE\s+\d+\s+OF\s+\d+`)
	rePageNum      = regexp.MustCompile(`^\s*\d{1,3}\s*$`)
	reFormFeed     = regexp.MustCompile(`\f`)
	reMovedBy      = regexp.MustCompile(`(?i)MOVED\s+BY[:\s]+(.+)`)
	reSecondedBy   = regexp.MustCompile(`(?i)SECONDED\s+BY[:\s]+(.+)`)
	reInlineVote   = regexp.MustCompile(`^(For|Against|Absent)\s+\((\d+)\):\s*(.*)`)
)

// ParseMotions extracts all motions from minutes PDF text.
// Every MOVED BY block becomes a Motion. Recorded votes are attached
// to the motion they belong to.
func ParseMotions(text string) []Motion {
	text = cleanDocument(text)

	// Pass 1: find all MOVED BY blocks and extract motions.
	motions := parseAllMotions(text)

	// Pass 2: find all recorded vote sections and attach to motions.
	attachRecordedVotes(text, motions)

	return motions
}

// cleanDocument removes form feeds, page numbers, and page headers.
func cleanDocument(text string) string {
	text = reFormFeed.ReplaceAllString(text, "\n")

	lines := strings.Split(text, "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if rePageNum.MatchString(trimmed) {
			continue
		}
		if rePageHeaderV1.MatchString(trimmed) {
			continue
		}
		if rePageHeaderV2.MatchString(trimmed) {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, "\n")
}

// parseAllMotions finds every MOVED BY block and creates a Motion for it.
func parseAllMotions(text string) []Motion {
	movedIndices := reMovedBy.FindAllStringIndex(text, -1)
	if len(movedIndices) == 0 {
		return nil
	}

	var motions []Motion
	for i, idx := range movedIndices {
		start := idx[0]
		var end int
		if i+1 < len(movedIndices) {
			end = movedIndices[i+1][0]
		} else {
			end = len(text)
		}
		block := text[start:end]

		// Look back before MOVED BY for agenda item heading
		contextStart := start - 500
		if contextStart < 0 {
			contextStart = 0
		}
		if i > 0 {
			// Don't look further back than the previous motion's block
			prevEnd := movedIndices[i-1][0]
			if prevEnd > contextStart {
				contextStart = prevEnd
			}
		}
		preamble := text[contextStart:start]

		m := parseMotionBlock(block, start, preamble)
		motions = append(motions, m)
	}

	return motions
}

// parseMotionBlock extracts motion details from a MOVED BY block.
// preamble is the text between the previous motion and this MOVED BY.
func parseMotionBlock(block string, blockStart int, preamble string) Motion {
	var m Motion
	m.blockStart = blockStart
	m.blockEnd = blockStart + len(block)

	if match := reMovedBy.FindStringSubmatch(block); match != nil {
		m.MovedBy = cleanName(match[1])
	}
	if match := reSecondedBy.FindStringSubmatch(block); match != nil {
		m.SecondedBy = cleanName(match[1])
	}

	m.AgendaItem = extractAgendaHeading(preamble)
	m.Text = extractFullMotionText(block)
	m.Result = findResultInBlock(block)

	return m
}

// reAgendaNum matches agenda item numbers like "7.1.2", "3.", "6.1"
var reAgendaNum = regexp.MustCompile(`^\s*\d+(\.\d+)*\.?\s+`)

// extractAgendaHeading pulls the numbered agenda item heading from the preamble.
// Only returns headings that start with an agenda number (e.g., "3. Appointment of Acting Mayor",
// "7.1.2 Report Back - Temporary Shelter Village"). This avoids picking up
// random context lines, names, or dates.
func extractAgendaHeading(preamble string) string {
	lines := strings.Split(preamble, "\n")

	// Scan backwards for the closest numbered agenda item.
	// Stop at previous motion content.
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		// Stop markers — we've gone past any relevant heading
		if strings.HasPrefix(trimmed, "MOVED BY") || strings.HasPrefix(trimmed, "SECONDED BY") {
			break
		}

		if reAgendaNum.MatchString(trimmed) {
			heading := reAgendaNum.ReplaceAllString(trimmed, "")
			heading = strings.TrimSpace(heading)
			// Must be a real heading, not just a numbered list item like "1. January 14, 2025"
			if len(heading) > 10 && !isDateLike(heading) {
				return heading
			}
		}
	}
	return ""
}

func isDateLike(s string) bool {
	months := []string{"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December"}
	for _, m := range months {
		if strings.HasPrefix(s, m) || strings.HasPrefix(s, "The Thunder Bay") {
			return true
		}
	}
	return false
}

// findResultInBlock finds CARRIED/LOST after the motion text,
// stopping before any recorded vote section or next motion.
func findResultInBlock(block string) string {
	lines := strings.Split(block, "\n")

	// Skip MOVED BY and SECONDED BY lines, then look for result
	pastHeader := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !pastHeader {
			if strings.HasPrefix(trimmed, "MOVED BY") || strings.HasPrefix(trimmed, "Moved By") ||
				strings.HasPrefix(trimmed, "SECONDED BY") || strings.HasPrefix(trimmed, "Seconded By") {
				continue
			}
			if trimmed == "" {
				continue
			}
			pastHeader = true
		}

		if trimmed == "CARRIED" || strings.HasPrefix(trimmed, "CARRIED ") {
			return "CARRIED"
		}
		if trimmed == "LOST" || strings.HasPrefix(trimmed, "LOST ") {
			return "LOST"
		}
	}
	return ""
}

// extractFullMotionText extracts the complete motion text from a block.
// This is everything between SECONDED BY and CARRIED/LOST.
func extractFullMotionText(block string) string {
	lines := strings.Split(block, "\n")

	// Find where SECONDED BY ends
	startLine := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "SECONDED BY") || strings.HasPrefix(trimmed, "Seconded By") {
			startLine = i + 1
			break
		}
	}

	// Collect text until CARRIED/LOST or recorded vote or end
	var textLines []string
	for i := startLine; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])

		if trimmed == "CARRIED" || trimmed == "LOST" ||
			strings.HasPrefix(trimmed, "CARRIED ") || strings.HasPrefix(trimmed, "LOST ") {
			break
		}
		if strings.Contains(strings.ToLower(trimmed), "recorded vote") ||
			strings.Contains(strings.ToLower(trimmed), "re-vote") {
			break
		}

		textLines = append(textLines, trimmed)
	}

	text := strings.Join(textLines, "\n")
	text = collapseWhitespace(text)
	text = strings.TrimSpace(text)
	return text
}

// attachRecordedVotes finds all recorded vote sections and attaches them
// to the correct motion based on position in the document.
func attachRecordedVotes(text string, motions []Motion) {
	// Pass A: old-style "recorded vote was requested" + YEA/NAY table
	voteIndices := reRecordedVote.FindAllStringIndex(text, -1)

	for _, idx := range voteIndices {
		votePos := idx[0]

		// Look forward for the YEA/NAY table
		endSearch := votePos + 3000
		if endSearch > len(text) {
			endSearch = len(text)
		}
		section := text[votePos:endSearch]

		vr := parseYeaNayTable(section)
		if vr == nil || (len(vr.For) == 0 && len(vr.Against) == 0) {
			continue
		}

		// Capture raw text for audit
		rawStart := votePos - 200
		if rawStart < 0 {
			rawStart = 0
		}
		rawEnd := votePos + 1500
		if rawEnd > len(text) {
			rawEnd = len(text)
		}
		rawText := text[rawStart:rawEnd]

		// Find the motion this vote belongs to.
		// The recorded vote is for the motion whose block contains it,
		// or the most recent motion before it.
		bestMotion := -1
		for i, m := range motions {
			if m.blockStart <= votePos && votePos < m.blockEnd {
				bestMotion = i
				break
			}
			if m.blockStart <= votePos {
				bestMotion = i
			}
		}

		if bestMotion >= 0 {
			motions[bestMotion].Votes = vr
			motions[bestMotion].RawText = rawText
			applyVoteResult(&motions[bestMotion])
		}
	}

	// Pass B: inline "For (N): names..." format (2025+ minutes)
	attachInlineVotes(text, motions)
}

// attachInlineVotes finds "For (N): ..." / "Against (N): ..." / "Absent (N): ..."
// lines within each motion block and parses them.
func attachInlineVotes(text string, motions []Motion) {
	for i := range motions {
		if motions[i].Votes != nil {
			continue // already has votes from YEA/NAY pass
		}

		block := text[motions[i].blockStart:motions[i].blockEnd]
		vr := parseInlineVote(block)
		if vr == nil {
			continue
		}

		// Capture raw text for audit
		rawStart := motions[i].blockStart
		rawEnd := motions[i].blockEnd
		if rawEnd-rawStart > 2000 {
			rawEnd = rawStart + 2000
		}
		motions[i].Votes = vr
		motions[i].RawText = text[rawStart:rawEnd]
		applyVoteResult(&motions[i])
	}
}

// parseInlineVote parses the inline "For (N): name, name..." format.
// Names wrap across lines in the PDF text, so we first collect the full text
// for each category (For/Against/Absent), then split on commas.
func parseInlineVote(block string) *VoteRecord {
	lines := strings.Split(block, "\n")

	vr := &VoteRecord{}
	// Collect raw text per category, joining continuation lines before splitting.
	type section struct {
		target *[]string
		text   string
	}
	var sections []section
	found := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if m := reInlineVote.FindStringSubmatch(trimmed); m != nil {
			category := m[1] // For, Against, Absent
			names := m[3]

			// Second "For" block means we've hit the next vote section — stop
			if category == "For" && found {
				break
			}
			found = true

			var target *[]string
			switch category {
			case "For":
				target = &vr.For
			case "Against":
				target = &vr.Against
			case "Absent":
				target = &vr.Absent
			}
			sections = append(sections, section{target: target, text: names})
			continue
		}

		// Continuation line: append to current section's text
		if found && len(sections) > 0 && trimmed != "" {
			if strings.HasPrefix(trimmed, "CARRIED") || strings.HasPrefix(trimmed, "LOST") {
				break
			}
			sections[len(sections)-1].text += " " + trimmed
		}
	}

	if !found {
		return nil
	}

	// Now split each section's joined text on commas and clean names.
	for _, sec := range sections {
		for _, name := range splitInlineNames(sec.text) {
			if n := cleanName(name); n != "" {
				*sec.target = append(*sec.target, n)
			}
		}
	}

	if len(vr.For) == 0 && len(vr.Against) == 0 {
		return nil
	}

	// Deduplicate — consent agenda blocks may contain multiple vote sections
	vr.For = dedup(vr.For)
	vr.Against = dedup(vr.Against)
	vr.Absent = dedup(vr.Absent)

	return vr
}

func dedup(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// splitInlineNames splits a comma/and-separated list of councillor names.
func splitInlineNames(s string) []string {
	// Replace " and " with comma for uniform splitting
	s = strings.ReplaceAll(s, ", and ", ", ")
	s = strings.ReplaceAll(s, " and ", ", ")

	parts := strings.Split(s, ",")
	var names []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			names = append(names, p)
		}
	}
	return names
}

// applyVoteResult overrides the text-based result with the authoritative vote count.
func applyVoteResult(m *Motion) {
	yeas := len(m.Votes.For)
	nays := len(m.Votes.Against)
	if yeas+nays > 0 {
		if yeas > nays {
			m.Result = "CARRIED"
		} else if nays > yeas {
			m.Result = "LOST"
		} else {
			m.Result = "TIE"
		}
	}
}

// parseYeaNayTable finds and parses a YEA/NAY column table.
func parseYeaNayTable(section string) *VoteRecord {
	lines := strings.Split(section, "\n")

	headerIdx := -1
	var yeaCol, nayCol int
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		yp := strings.Index(line, "YEA")
		np := strings.Index(line, "NAY")
		if yp >= 0 && np >= 0 && !isCouncillorLine(trimmed) {
			headerIdx = i
			yeaCol = yp
			nayCol = np
			break
		}
	}
	if headerIdx < 0 {
		return nil
	}

	midpoint := (yeaCol + nayCol) / 2
	vr := &VoteRecord{}

	type nameEntry struct {
		name  string
		isNay bool
	}
	var pending []nameEntry
	blankRun := 0

	for i := headerIdx + 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "MOVED BY") || strings.HasPrefix(trimmed, "Moved By") {
			break
		}
		if trimmed == "CARRIED" || trimmed == "LOST" ||
			strings.HasPrefix(trimmed, "CARRIED ") || strings.HasPrefix(trimmed, "LOST ") {
			break
		}
		if strings.Contains(strings.ToLower(trimmed), "recorded vote") ||
			strings.Contains(strings.ToLower(trimmed), "re-vote") {
			break
		}

		if trimmed == "" {
			blankRun++
			if blankRun >= 3 {
				break
			}
			continue
		}
		blankRun = 0

		leftPart := ""
		rightPart := ""
		if len(line) > nayCol {
			leftPart = strings.TrimSpace(line[:nayCol])
			rightPart = strings.TrimSpace(line[nayCol:])
		} else {
			leadingSpaces := len(line) - len(strings.TrimLeft(line, " \t"))
			if leadingSpaces > midpoint {
				rightPart = trimmed
			} else {
				leftPart = trimmed
			}
		}

		leftIsName := leftPart != "" && isCouncillorLine(leftPart)
		rightIsName := rightPart != "" && isCouncillorLine(rightPart)

		if !leftIsName && !rightIsName && len(pending) > 0 {
			cont := leftPart
			if cont == "" {
				cont = rightPart
			}
			if cont != "" {
				if looksLikeBareName(cont) {
					leadingSpaces := len(line) - len(strings.TrimLeft(line, " \t"))
					pending = append(pending, nameEntry{name: cont, isNay: leadingSpaces > midpoint})
				} else {
					pending[len(pending)-1].name += " " + cont
				}
			}
			continue
		}

		if leftIsName {
			pending = append(pending, nameEntry{name: leftPart, isNay: false})
		}
		if rightIsName {
			pending = append(pending, nameEntry{name: rightPart, isNay: true})
		}
	}

	for _, e := range pending {
		name := cleanName(e.name)
		if name == "" {
			continue
		}
		if e.isNay {
			vr.Against = append(vr.Against, name)
		} else {
			vr.For = append(vr.For, name)
		}
	}

	if len(vr.For) == 0 && len(vr.Against) == 0 {
		return nil
	}
	return vr
}

// isCouncillorLine checks if a string starts with a councillor/mayor title.
func isCouncillorLine(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(lower, "councillor") ||
		strings.HasPrefix(lower, "mayor") ||
		strings.HasPrefix(lower, "deputy mayor")
}

// looksLikeBareName checks if text looks like a full name without a title.
func looksLikeBareName(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 5 || len(s) > 40 {
		return false
	}
	words := strings.Fields(s)
	if len(words) < 2 {
		return false
	}
	for _, w := range words {
		for _, r := range w {
			if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '\'' || r == '-' || r == '.') {
				return false
			}
		}
	}
	return true
}

// cleanName strips titles and whitespace from a councillor name.
func cleanName(s string) string {
	s = strings.TrimSpace(s)

	// Normalize smart quotes to straight apostrophe
	s = strings.ReplaceAll(s, "\u2018", "'")
	s = strings.ReplaceAll(s, "\u2019", "'")

	for _, prefix := range []string{
		"Mayor ", "Councillor. ", "Councillor ", "Deputy Mayor ",
	} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, ".,;:")
	s = strings.TrimSpace(s)

	if len(s) > 30 || len(s) < 2 {
		return ""
	}
	words := strings.Fields(s)
	if len(words) > 3 {
		return ""
	}

	lower := strings.ToLower(s)
	for _, junk := range []string{
		"by-law", "resolution", "amended", "financing", "soccer",
		"committee", "report", "schedule", "motion", "agenda",
		"carried", "lost", "budget",
	} {
		if strings.Contains(lower, junk) {
			return ""
		}
	}

	if n, ok := knownNames[s]; ok {
		return n
	}

	// Only accept names in the canonical whitelist — reject everything else.
	if canonicalNames[s] {
		return s
	}

	return ""
}

// canonicalNames is the whitelist of full councillor names across all terms.
// cleanName rejects any name not in this set or in knownNames.
var canonicalNames = map[string]bool{
	// 2022-2026
	"Ken Boshcoff":       true,
	"Rajni Agarwal":      true,
	"Albert Aiello":      true,
	"Mark Bentz":         true,
	"Shelby Ch'ng":       true,
	"Kasey Etreni":       true,
	"Andrew Foulds":      true,
	"Trevor Giertuga":    true,
	"Greg Johnsen":       true,
	"Kristen Oliver":     true,
	"Dominic Pasqualino": true,
	"Michael Zussino":    true,
	// 2018-2022
	"Bill Mauro":      true,
	"Brian Hamilton":  true,
	"Brian McKinnon":  true,
	"Rebecca Johnson": true,
	"Cody Fraser":     true,
	"Aldo Ruberto":    true,
	"Peng You":        true,
	// 2014-2018
	"Keith Hobbs":   true,
	"Joe Virdiramo": true,
	"Paul Pugh":     true,
	"Lori Paras":    true,
	"Frank Iozzo":   true,
	"Larry Hebert":  true,
}

// knownNames maps initial-format and variant names to canonical full names.
var knownNames = map[string]string{
	// 2022-2026 term
	"K. Boshcoff":       "Ken Boshcoff",
	"R. Agarwal":        "Rajni Agarwal",
	"A. Aiello":         "Albert Aiello",
	"M. Bentz":          "Mark Bentz",
	"S. Ch'ng":          "Shelby Ch'ng",
	"Shelby Ch\u2019ng": "Shelby Ch'ng",
	"K. Etreni":         "Kasey Etreni",
	"A. Foulds":         "Andrew Foulds",
	"T. Giertuga":       "Trevor Giertuga",
	"G. Johnsen":        "Greg Johnsen",
	"K. Oliver":         "Kristen Oliver",
	"D. Pasqualino":     "Dominic Pasqualino",
	"M. Zussino":        "Michael Zussino",
	"M. Zus":            "Michael Zussino",
	"Michael Zussinno":  "Michael Zussino",

	// 2018-2022 term
	"B. Mauro":    "Bill Mauro",
	"B. Hamilton": "Brian Hamilton",
	"B. McKinnon": "Brian McKinnon",
	"R. Johnson":  "Rebecca Johnson",
	"C. Fraser":   "Cody Fraser",
	"C. Ch'ng":    "Shelby Ch'ng",
	"A. Ruberto":  "Aldo Ruberto",

	// 2014-2018 term
	"K. Hobbs":     "Keith Hobbs",
	"J. Virdiramo": "Joe Virdiramo",
	"P. Pugh":      "Paul Pugh",
	"L. Paras":     "Lori Paras",
	"F. Iozzo":     "Frank Iozzo",
	"L. Hebert":    "Larry Hebert",

	// Shared across terms
	"P. You": "Peng You",
}

// collapseWhitespace replaces runs of whitespace with single spaces.
func collapseWhitespace(s string) string {
	return regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
}
