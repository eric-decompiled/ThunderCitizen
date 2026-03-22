package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/jackc/pgx/v5/pgxpool"
)

type meetingRow struct {
	id      string
	date    string
	summary string
}

const reformatSystem = `You reformat council meeting summaries for readability. Break the summary into short paragraphs — one paragraph per distinct topic or decision. Do NOT change any wording, facts, or add new content. Just add paragraph breaks where topics shift. Output plain text only.`

func reformatMeetings(ctx context.Context, pool *pgxpool.Pool, apiKey string, workers int) error {
	rows, err := pool.Query(ctx, `
		SELECT id, date::text, llm_summary
		FROM council_meetings
		WHERE llm_summary != '' AND llm_summary NOT LIKE '%' || E'\n\n' || '%'
		ORDER BY date`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var meetings []meetingRow
	for rows.Next() {
		var m meetingRow
		if err := rows.Scan(&m.id, &m.date, &m.summary); err != nil {
			return err
		}
		meetings = append(meetings, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	fmt.Printf("Reformatting %d meeting summaries with %d workers\n", len(meetings), workers)

	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	ch := make(chan meetingRow, len(meetings))
	for _, m := range meetings {
		ch <- m
	}
	close(ch)

	// Rate limiter: 40 req/min to stay under 50 rpm limit
	ticker := time.NewTicker(time.Duration(60000/40) * time.Millisecond) // ~1.5s between requests
	defer ticker.Stop()

	var succeeded, failed atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range ch {
				<-ticker.C // rate limit

				reformatted, err := reformatOne(ctx, client, m.summary)
				if err != nil {
					fmt.Printf("  ERROR %s: %v\n", m.date, err)
					failed.Add(1)
					continue
				}

				_, err = pool.Exec(ctx, `UPDATE council_meetings SET llm_summary = $1 WHERE id = $2`,
					reformatted, m.id)
				if err != nil {
					fmt.Printf("  DB ERROR %s: %v\n", m.date, err)
					failed.Add(1)
					continue
				}

				fmt.Printf("  OK %s\n", m.date)
				succeeded.Add(1)
			}
		}()
	}

	wg.Wait()
	fmt.Printf("\nDone: %d succeeded, %d failed\n", succeeded.Load(), failed.Load())
	return nil
}

func reformatOne(ctx context.Context, client anthropic.Client, summary string) (string, error) {
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 500,
		System: []anthropic.TextBlockParam{
			{Text: reformatSystem},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(summary)),
		},
	})
	if err != nil {
		return "", err
	}

	for _, block := range resp.Content {
		if block.Type == "text" {
			return strings.TrimSpace(block.Text), nil
		}
	}
	return "", fmt.Errorf("empty response")
}
