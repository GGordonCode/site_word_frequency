// This program finds the most frequently occurring words of a
// specified minimum and/or maximum length for a given site.  It
// is essentially a web crawler that makes its best effort to stay
// within the hostname of the original site.  On a given page, it
// both scans for text, for which it builds a frequency histogram,
// plus it extracts the "href" links for further processing.
//
// At the end, the most frequent cumulative word counts are displayed
// in sorted order.  It also reports some statistics related to channel
// usage, so in theory, we could performance tune the program.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"runtime/pprof"
	"strconv"
	"sync"
	"syscall"
)

const (
	// Some ASCII graphics sequences.
	bold        = "\033[1m"
	redBold     = "\033[31;1m"
	graphicsOff = "\033[0m"

	// Length of lines that overwrite the previous line.
	outputLength = 75
)

var (
	concurrency = flag.Int("concurrency", 10,
		"number of active concurrent goroutines")
	unlimitedChan = flag.Bool("unlimited_chan", false,
		"if 'true', use the experimental unlimited channel")
	dictSize    = flag.Int("dict_size", 25000, "main dictionary initial size")
	connTimeout = flag.Int("conn_timeout", 10, "HTTP client timeout (secs)")
	minLen      = flag.Uint("min_len", 5,
		"minimum word length to track (0 => no limit)")
	maxLen = flag.Uint("max_len", 8,
		"the maximum word length to track (0 => no limit)")
	totWords   = flag.Uint("tot_words", 10, "show the top 'this many' words")
	iter       = flag.Uint("iter", 0, "if > 0, stop ater this many iterations")
	pprofPort  = flag.Int("pprof_port", 0, "if non-zero, pprof server port")
	cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")
	memprofile = flag.String("memprofile", "", "write memory profile to this file")
)

// A formatter for messages intended for stdout.
type formatter struct {
	isTTY  bool
	fmtStr string
	fmu    sync.Mutex
}

func main() {
	flag.Parse()
	if flag.NArg() < 1 {
		log.Fatal(fmt.Errorf("%s: missing start URL", os.Args[0]))
		os.Exit(1)
	}

	if (*minLen == 0 && *maxLen == 0) ||
		(*maxLen > 0 && *minLen > *maxLen) {
		log.Fatal(fmt.Errorf("%s: invalid min/max length combination: %d, %d",
			os.Args[0], *minLen, *maxLen))
		os.Exit(1)
	}

	startURL := flag.Arg(0)
	surl, err := url.Parse(startURL)
	if err != nil {
		log.Fatal(fmt.Errorf("%s: The url '%s' is not syntactically valid",
			os.Args[0], startURL))
		os.Exit(1)
	}

	if *pprofPort != 0 {
		go func() {
			log.Println(http.ListenAndServe(
				"localhost:"+strconv.Itoa(*pprofPort), nil))
		}()
	}

	if *memprofile != "" && *cpuprofile != "" {
		log.Fatal("Cannot profile CPU and memory at once.")
	}

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	// We'll use escape sequences if stdout is not being redirected
	// to a file.
	formatter := newFormatter()

	finder := newWordFinder(surl, formatter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal handlers for orderly shutdown.  Handle SIGINT and
	// SIGTERM for now.
	go func() {
		ch := make(chan os.Signal)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		sig := <-ch
		l := outputLength - len(sig.String())
		log.Printf("%s%-*.*s", sig, l, l, "... draining queue")
		cancel()
	}()

	finder.run(ctx)
	showStatus(finder)
}

func showStatus(finder *WordFinder) {
	if finder.interrupt {
		log.Printf("%-*.*s\n", outputLength, outputLength,
			"Note: process was interrupted, results are partial.")
	}

	elist := finder.getErrors()
	if elist == nil {
		fmt.Printf("%-*.*s\n", outputLength, outputLength,
			"No errors occurred in run.")
	} else {
		for _, r := range elist {
			fmt.Printf("'%s': error occurred: %s\n", r.url, r.err.Error())
		}
	}
	fmt.Println()

	res := finder.getResults()
	if *maxLen > 0 {
		fmt.Printf("Top %d totals for words of length %d to %d:\n",
			*totWords, *minLen, *maxLen)
	} else {
		fmt.Printf("Top %d totals for words of length >= %d:\n",
			*totWords, *minLen)
	}
	for i, kv := range res {
		fmt.Printf("[%d] %s: %d\n", i+1, kv.key, kv.value)
	}
}

func newFormatter() *formatter {
	f := &formatter{}
	fi, err := os.Stdout.Stat()
	if err == nil {
		if (fi.Mode() & (os.ModeDevice | os.ModeCharDevice)) ==
			(os.ModeDevice | os.ModeCharDevice) {
			f.isTTY = true
		}
	}
	f.fmtStr = fmt.Sprintf("%%s%%-%d.%ds%%s\r", outputLength, outputLength)
	return f
}

func (f *formatter) showStatusLine(text string, interrupt bool) {
	var line string

	if f.isTTY {
		var leading string
		if interrupt {
			leading = redBold
		} else {
			leading = bold
		}
		if len(text) > outputLength {
			text = text[:outputLength-3] + "..."
		}

		// Show links on same line.
		line = fmt.Sprintf(f.fmtStr, leading, text, graphicsOff)
	} else {
		line = fmt.Sprintf("Processing link: '%s'\n", text)
	}

	f.fmu.Lock()
	os.Stdout.Write([]byte(line))
	f.fmu.Unlock()
}
