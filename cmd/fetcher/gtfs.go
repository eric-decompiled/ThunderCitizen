package main

import (
	"fmt"

	"thundercitizen/internal/transit"
)

func runGTFS() {
	ctx, cancel := rootContext()
	defer cancel()

	sources, err := transit.DiscoverGTFSSources(ctx)
	if err != nil {
		fail("discover: %v", err)
	}
	printSources(sources)

	if !confirm() {
		fmt.Println("cancelled")
		return
	}

	if err := transit.FetchGTFS(ctx); err != nil {
		fail("fetch: %v", err)
	}
}
