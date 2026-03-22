// Command summarize generates plain-language summaries for council motions
// using the Claude API.
//
// Usage:
//
//	go run ./cmd/summarize                      # summarize all unsummarized motions
//	go run ./cmd/summarize -force               # re-summarize all motions
//	go run ./cmd/summarize -term 2022-2026      # single term
//	go run ./cmd/summarize -id 284              # single motion
//	go run ./cmd/summarize -dry-run             # print prompts without calling API
//	go run ./cmd/summarize -model claude-sonnet-4-5-20250514  # use a different model
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/config"
	"thundercitizen/internal/council"
)

func main() {
	term := flag.String("term", "", "filter by term (e.g. 2022-2026)")
	motionID := flag.Int64("id", 0, "summarize a single motion by ID")
	force := flag.Bool("force", false, "re-summarize motions that already have summaries")
	dryRun := flag.Bool("dry-run", false, "print prompts without calling the API")
	meetingsOnly := flag.Bool("meetings-only", false, "skip motion pass, only summarize meetings")
	reformat := flag.Bool("reformat", false, "reformat existing meeting summaries with paragraph breaks (12 parallel workers)")
	dump := flag.Bool("dump", false, "export all LLM summaries to "+summaryFile)
	load := flag.Bool("load", false, "import LLM summaries from "+summaryFile)
	model := flag.String("model", "", "Claude model for motions (default: claude-haiku-4-5-20251001)")
	meetingModel := flag.String("meeting-model", "", "Claude model for meetings (default: claude-haiku-4-5-20251001)")
	dbURL := flag.String("db", "", "database URL (default: DATABASE_URL env)")
	flag.Parse()

	if *dump {
		if err := exportSummaries(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *load {
		if err := importSummaries(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *reformat {
		if *dbURL == "" {
			*dbURL = config.Secret("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/thundercitizen?sslmode=disable")
		}
		apiKey := config.Secret("ANTHROPIC_API_KEY", "")
		if apiKey == "" {
			fmt.Fprintln(os.Stderr, "error: ANTHROPIC_API_KEY required")
			os.Exit(1)
		}
		pool, err := pgxpool.New(context.Background(), *dbURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer pool.Close()
		if err := reformatMeetings(context.Background(), pool, apiKey, 12); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(*term, *motionID, *force, *dryRun, *meetingsOnly, *model, *meetingModel, *dbURL); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(term string, motionID int64, force, dryRun, meetingsOnly bool, model, meetingModel, dbURL string) error {
	if dbURL == "" {
		dbURL = config.Secret("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/thundercitizen?sslmode=disable")
	}

	apiKey := config.Secret("ANTHROPIC_API_KEY", "")
	if apiKey == "" && !dryRun {
		return fmt.Errorf("ANTHROPIC_API_KEY required in secrets.conf or env (use -dry-run to skip)")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	store := council.NewStore(pool)

	if !meetingsOnly {
		motions, err := store.ListUnsummarized(ctx, term, motionID, force)
		if err != nil {
			return fmt.Errorf("listing motions: %w", err)
		}

		fmt.Printf("Found %d motions to summarize\n", len(motions))

		var succeeded, failed int

		if dryRun {
			for _, m := range motions {
				fmt.Printf("\n--- Motion %d (result: %s) ---\n", m.ID, m.Result)
				fmt.Println(council.BuildPrompt(m))
			}
		} else if len(motions) > 0 {
			summarizer := council.NewSummarizer(apiKey, model)
			modelName := model
			if modelName == "" {
				modelName = "claude-haiku-4-5-20251001"
			}

			const workers = 1
			type result struct {
				idx     int
				motion  council.UnsummarizedMotion
				summary *council.SummaryResult
				err     error
			}

			// Rate limiter: 45 RPM = 1 request per 1.33s
			ticker := time.NewTicker(time.Second * 4 / 3)
			defer ticker.Stop()

			jobs := make(chan int, workers)
			results := make(chan result, len(motions))

			for w := 0; w < workers; w++ {
				go func() {
					for idx := range jobs {
						m := motions[idx]
						res, err := summarizer.Summarize(ctx, m)
						results <- result{idx: idx, motion: m, summary: res, err: err}
					}
				}()
			}

			go func() {
				for i := range motions {
					<-ticker.C
					jobs <- i
				}
				close(jobs)
			}()

			for range motions {
				r := <-results
				m := r.motion
				i := r.idx

				if r.err != nil {
					fmt.Fprintf(os.Stderr, "  [%d/%d] motion %d: ERROR: %v\n", i+1, len(motions), m.ID, r.err)
					failed++
					continue
				}

				err := store.UpdateMotionSummary(ctx, council.MotionSummaryUpdate{
					ID:           m.ID,
					Summary:      r.summary.Summary,
					Label:        r.summary.Label,
					Significance: r.summary.Significance,
					Model:        modelName,
				})
				if err != nil {
					fmt.Fprintf(os.Stderr, "  [%d/%d] motion %d: DB ERROR: %v\n", i+1, len(motions), m.ID, err)
					failed++
					continue
				}

				fmt.Printf("  [%d/%d] motion %d: %s [%s]\n", i+1, len(motions), m.ID, r.summary.Label, r.summary.Significance)
				succeeded++
			}
		}

		fmt.Printf("\nMotions done: %d succeeded, %d failed (of %d total)\n", succeeded, failed, len(motions))
	}

	// --- Meeting summary pass ---
	if motionID > 0 {
		return nil // skip meeting pass when targeting a single motion
	}

	meetings, err := store.ListUnsummarizedMeetings(ctx, term, force)
	if err != nil {
		return fmt.Errorf("listing meetings: %w", err)
	}

	fmt.Printf("\nFound %d meetings to summarize\n", len(meetings))
	if len(meetings) == 0 {
		return nil
	}

	if dryRun {
		for _, m := range meetings {
			fmt.Printf("\n--- Meeting %s (%s, %d motions) ---\n", m.Date, m.Title, len(m.Motions))
			fmt.Println(council.BuildMeetingPrompt(m))
		}
		return nil
	}

	mtgModelName := meetingModel
	if mtgModelName == "" {
		mtgModelName = "claude-haiku-4-5-20251001"
	}
	mtgSummarizer := council.NewSummarizer(apiKey, mtgModelName)

	var mtgSucceeded, mtgFailed int

	const mtgWorkers = 1
	type mtgResult struct {
		idx     int
		meeting council.UnsummarizedMeeting
		summary string
		err     error
	}

	mtgTicker := time.NewTicker(time.Second * 4 / 3)
	defer mtgTicker.Stop()

	mtgJobs := make(chan int, mtgWorkers)
	mtgResults := make(chan mtgResult, len(meetings))

	for w := 0; w < mtgWorkers; w++ {
		go func() {
			for idx := range mtgJobs {
				m := meetings[idx]
				summary, err := mtgSummarizer.SummarizeMeeting(ctx, m)
				mtgResults <- mtgResult{idx: idx, meeting: m, summary: summary, err: err}
			}
		}()
	}

	go func() {
		for i := range meetings {
			<-mtgTicker.C
			mtgJobs <- i
		}
		close(mtgJobs)
	}()

	for range meetings {
		r := <-mtgResults
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "  [%d/%d] meeting %s: ERROR: %v\n", r.idx+1, len(meetings), r.meeting.Date, r.err)
			mtgFailed++
			continue
		}

		if err := store.UpdateMeetingSummary(ctx, r.meeting.ID, r.summary, mtgModelName); err != nil {
			fmt.Fprintf(os.Stderr, "  [%d/%d] meeting %s: DB ERROR: %v\n", r.idx+1, len(meetings), r.meeting.Date, err)
			mtgFailed++
			continue
		}

		preview := r.summary
		if len(preview) > 80 {
			preview = preview[:80] + "..."
		}
		fmt.Printf("  [%d/%d] %s: %s\n", r.idx+1, len(meetings), r.meeting.Date, preview)
		mtgSucceeded++
	}

	fmt.Printf("\nMeetings done: %d succeeded, %d failed (of %d total)\n", mtgSucceeded, mtgFailed, len(meetings))
	return nil
}
