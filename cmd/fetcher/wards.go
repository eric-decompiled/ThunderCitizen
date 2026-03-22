package main

import (
	"fmt"

	"thundercitizen/internal/wards"
)

func runWards() {
	ctx, cancel := rootContext()
	defer cancel()

	fmt.Println("Discovering ward shapes via Open North...")
	sources, err := wards.DiscoverSources(ctx)
	if err != nil {
		fail("discover: %v", err)
	}
	printSources(sources)

	if !confirm() {
		fmt.Println("cancelled")
		return
	}

	if err := wards.Fetch(ctx); err != nil {
		fail("fetch: %v", err)
	}
}
