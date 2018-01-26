// The finder drives the main processing of the crawler, accumulates
// error results and stats, and reports the final word count tallies.
package main

import (
	"context"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// The WordFinder is the struct that controls the overall processing.
// It collates the results to get the longest word at the end.
type WordFinder struct {
	words     map[string]int
	errRecs   []SearchRecord
	target    string
	startURL  *url.URL
	filter    chan ([]string)
	interrupt bool
	mu        sync.Mutex
	fmtr      *formatter
	stats     *RunStats
}

type RunStats struct {
	chanFree    int64
	chanBlocked int64
}

// The following two structs are for sorting the frequency map.
type kvPair struct {
	key   string
	value int
}

type kvSorter []kvPair

// Ensure we've implemented all the sort.Interface methods.
var _ sort.Interface = (*kvSorter)(nil)

// Creates a new WordFinder with the given start URL.
func newWordFinder(startURL *url.URL, f *formatter) *WordFinder {

	// Restrict crawling to within initial site for a reasonable demo.
	// So a site that has our host in it (we don't need the www part
	// to comapre) is a link we'll follow/
	target := startURL.Hostname()
	if strings.HasPrefix(target, "www.") {
		target = target[4:]
	}

	return &WordFinder{
		words:    make(map[string]int),
		startURL: startURL,
		target:   target,
		filter:   make(chan []string, (*multiplier)*(*concurrency)),
		fmtr:     f,
		stats:    &RunStats{},
	}
}

// This is the main run loop from the crawler.  It creates the
// worker goroutines, filters and submits new URL processing tasks,
// and waits for the entire process to complete before returning.
func (wf *WordFinder) run(ctx context.Context) {

	log.Printf("Beginning run, type Ctrl-C to interrupt.\n\n")

	// Create and launch the goroutines.
	visited := make(map[string]bool)
	tasks := make(chan SearchRecord, (*multiplier)*(*concurrency))
	var wg sync.WaitGroup
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for rec := range tasks {
				rec.processLink(ctx, wf)
			}
		}()
	}

	// Prime the pump by feeding the start url into the work channel.
	tasks <- SearchRecord{url: wf.startURL.String()}

	// Loop until there is no more work.  By keeping a count, we know
	// when there is no more work left.  The loop decrements once each
	// time through to balance the result of adding a new search task.
	for cnt := 1; cnt > 0; cnt-- {

		// At the start of each loop iteration, we block on the "filter"
		// channel, which contains results from each page scan (all the
		// links found for a page are in a single slice).  The filter
		// also removes any links already visited.
		l := <-wf.filter
		if wf.interrupt {
			continue
		}

		for _, link := range l {
			if visited[link] == false {
				visited[link] = true
				// Every link sent into the "task" channel
				// adds one to the count.  Note if we received
				// an interrupt, we'll stop sending new tasks
				// and wait for the queue to drain.
				cnt++
				select {
				case <-ctx.Done():
					cnt--
					wf.interrupt = true
				case tasks <- SearchRecord{url: link}:
				}
			}
		}
	}

	// Don't leak goroutines (yeah, it's a demo, but still).
	// Note: due to the counting in the loop above, we know
	// that all sending and receiving of data is done, so
	// it is safe to close the channels here.
	if wf.interrupt {
		log.Printf("%-75.75s\n",
			"Note: process was interrupted, results are partial.")
	}
	close(tasks)
	close(wf.filter)
	wg.Wait()
}

// When a goroutine is finished processing a link, it transfers it's
// link and word count data to the finder.
func (wf *WordFinder) addLinkData(sr *SearchRecord,
	wds map[string]int, links []string) {
	wf.mu.Lock()

	// Only append records with errors.
	if sr.err != nil {
		wf.errRecs = append(wf.errRecs, *sr)
	}
	for k, v := range wds {
		wf.words[k] += v
	}

	// Only create a new goroutine to send the link if the channel
	// would block.  One way or another, we want to keep the thread
	// available for processing.
	select {
	case wf.filter <- links:
		wf.stats.chanFree++
		wf.mu.Unlock()
	default:
		wf.stats.chanBlocked++
		wf.mu.Unlock()
		go func() {
			wf.filter <- links
		}()
	}
}

// Show any errors and the top word counts.
func (wf *WordFinder) getResults() []kvPair {
	sorter := make(kvSorter, len(wf.words))
	i := 0
	for k, v := range wf.words {
		sorter[i] = kvPair{k, v}
		i++
	}
	sort.Sort(sorter)
	cnt := *totWords
	if len(sorter) < cnt {
		cnt = len(sorter)
	}
	return sorter[:cnt]
}

// Returns the search records that contained errors or
// nil if no errors occurred.
func (wf *WordFinder) getErrors() []SearchRecord {
	return wf.errRecs
}

func (wf *WordFinder) getRunStats() *RunStats {
	return wf.stats
}

// The following methods are used to to sort the histogram by value.
// Len is part of sort.Interface.
func (kvs kvSorter) Len() int {
	return len(kvs)
}

// Swap is part of sort.Interface.
func (kvs kvSorter) Swap(i, j int) {
	kvs[i], kvs[j] = kvs[j], kvs[i]
}

// Less is part of sort.Interface.
func (kvs kvSorter) Less(i, j int) bool {
	return kvs[j].value < kvs[i].value
}
