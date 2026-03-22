package council

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Summarizer generates plain-language summaries for council motions using Claude.
type Summarizer struct {
	client anthropic.Client
	model  string
}

// NewSummarizer creates a summarizer with the given API key.
// Model defaults to Haiku for cost efficiency.
func NewSummarizer(apiKey string, model string) *Summarizer {
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Summarizer{client: client, model: model}
}

// SummaryResult holds the parsed LLM response.
type SummaryResult struct {
	Summary      string `json:"summary"`
	Label        string `json:"label"`
	Significance string `json:"significance"`
}

const systemPrompt = `You summarize Thunder Bay City Council motions for a civic transparency website. Your audience is residents who want to understand what their council decided without reading legalese.

Respond ONLY with a JSON object — no markdown fences, no extra text.`

// BuildPrompt constructs the user prompt for a motion.
func BuildPrompt(m UnsummarizedMotion) string {
	var b strings.Builder
	b.WriteString("Summarize this council motion.\n\n")

	if m.AgendaItem != "" {
		b.WriteString("Agenda item: " + m.AgendaItem + "\n")
	}
	b.WriteString("Motion text: " + m.Text + "\n")
	b.WriteString("Result: " + m.Result + "\n")
	if m.YeaCount+m.NayCount > 0 {
		b.WriteString(fmt.Sprintf("Recorded vote: %d for, %d against\n", m.YeaCount, m.NayCount))
	}

	b.WriteString(`
Respond in JSON:
{
  "summary": "1-2 sentence plain-language summary. What does this motion actually do? No legalese.",
  "label": "Short label under 60 characters for a grid display. Be specific (e.g. 'Shelter village site at Miles St' not 'Motion re: report').",
  "significance": "one of: headline, notable, routine, procedural"
}

Significance guide:
- headline: major policy decision, likely covered by media, affects many residents
- notable: contentious (close vote margin), substantive policy change, or significant spending
- routine: standard business, clear majority, committee minutes adoption
- procedural: meeting logistics (adjournment, continue past 11pm, agenda adoption)`)

	return b.String()
}

// Summarize calls Claude to generate a summary for one motion.
func (s *Summarizer) Summarize(ctx context.Context, m UnsummarizedMotion) (*SummaryResult, error) {
	prompt := BuildPrompt(m)

	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     s.model,
		MaxTokens: 300,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude API: %w", err)
	}

	// Extract text from response
	var text string
	for _, block := range resp.Content {
		if block.Type == "text" {
			text = block.Text
			break
		}
	}
	if text == "" {
		return nil, fmt.Errorf("empty response from Claude")
	}

	// Strip markdown fences if present
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) > 2 {
			text = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	var result SummaryResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("parse response: %w (raw: %s)", err, text)
	}

	// Validate significance
	switch result.Significance {
	case "headline", "notable", "routine", "procedural":
		// valid
	default:
		result.Significance = "routine"
	}

	// Truncate label if too long
	if len(result.Label) > 60 {
		result.Label = result.Label[:57] + "..."
	}

	return &result, nil
}

const meetingSystemPrompt = `You summarize Thunder Bay City Council meetings for a civic transparency website. Your audience is residents who want a quick sense of what happened at the meeting.

Rules:
- Respond with plain text only — no JSON, no markdown, no bullet points.
- Write 2-4 sentences. Get straight to the substance.
- NEVER open with "Council met on [date]" or "City Council approved X of Y motions" or any formulaic framing. Lead directly with the most consequential decision or theme.
- Name specific topics, dollar amounts, locations, and policy changes. Be concrete.
- If a motion was defeated or contentious, say so — that's news.
- For meetings that were entirely procedural (minutes confirmations, adjournments), a single sentence is fine.`

// BuildMeetingPrompt constructs the user prompt for a meeting summary.
func BuildMeetingPrompt(m UnsummarizedMeeting) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Summarize this %s council meeting from %s.\n\n", m.Title, m.Date)
	b.WriteString("Motions:\n")

	for _, mot := range m.Motions {
		sig := ""
		if mot.Significance == "headline" || mot.Significance == "notable" {
			sig = " [" + mot.Significance + "]"
		}
		fmt.Fprintf(&b, "- %s (%s)%s", mot.Label, mot.Result, sig)
		if mot.Summary != "" {
			fmt.Fprintf(&b, " — %s", mot.Summary)
		}
		b.WriteString("\n")
	}

	b.WriteString(`
Summarize in 2-4 sentences. What actually happened that residents should know about?`)

	return b.String()
}

// SummarizeMeeting calls Claude to generate a meeting-level summary.
func (s *Summarizer) SummarizeMeeting(ctx context.Context, m UnsummarizedMeeting) (string, error) {
	prompt := BuildMeetingPrompt(m)

	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     s.model,
		MaxTokens: 400,
		System: []anthropic.TextBlockParam{
			{Text: meetingSystemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("claude API: %w", err)
	}

	for _, block := range resp.Content {
		if block.Type == "text" {
			return strings.TrimSpace(block.Text), nil
		}
	}
	return "", fmt.Errorf("empty response from Claude")
}
