package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

/* -------------------------------
   Data model
--------------------------------*/

type Article struct {
	Title       string `json:"title"`
	Content     string `json:"content"`
	Excerpt     string `json:"excerpt"`
	Link        string `json:"link"`
	PublishDate string `json:"published_date"`
	UpdatedDate string `json:"updated_date"`
}

/* -------------------------------
   Transformer – only the client
   constructor changes (custom
   http.Transport tuned for QPS)
--------------------------------*/

type Transformer struct {
	apiBase string
	client  *http.Client
	headers http.Header
}

func NewTransformer(apiBase, apiKeyEnv string, maxConns int) (*Transformer, error) {
	apiBase = strings.TrimSuffix(apiBase, "/")
	key := os.Getenv(apiKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("env %q not set", apiKeyEnv)
	}
	h := make(http.Header)
	h.Set("Authorization", "Bearer "+key)

	tr := &http.Transport{
		MaxIdleConns:        maxConns,
		MaxIdleConnsPerHost: maxConns,
		MaxConnsPerHost:     maxConns,
		IdleConnTimeout:     90 * time.Second,
	}

	return &Transformer{
		apiBase: apiBase,
		headers: h,
		client:  &http.Client{Transport: tr, Timeout: 15 * time.Second},
	}, nil
}

// -----------------------------------------------------------------------------
// Helpers (≈ clean_description, parse_date, build HTML, build metadata)
// -----------------------------------------------------------------------------

func (t *Transformer) cleanHTML(s string) string {
	replacer := strings.NewReplacer("<script", "&lt;script", "</script>", "&lt;/script&gt;")
	s = replacer.Replace(s)
	return s
}

func (t *Transformer) buildHTML(a *Article) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("<h1>%s</h1>\n", html.EscapeString(a.Title)))
	if a.Excerpt != "" {
		b.WriteString(fmt.Sprintf("<p>%s</p>\n", html.EscapeString(a.Excerpt)))
	}
	b.WriteString("<div>\n")
	b.WriteString(t.cleanHTML(a.Content))
	b.WriteString("\n</div>\n")
	b.WriteString("<h3>Metadata</h3>\n")
	b.WriteString(fmt.Sprintf(`<p>Source Url: <a href="%s">%s</a></p>`, a.Link, a.Link))
	b.WriteString(fmt.Sprintf(`<p>Published Date: %s</p>`, a.PublishDate))
	b.WriteString(fmt.Sprintf(`<p>Updated Date: %s</p>`, a.UpdatedDate))
	return b.String()
}

func (t *Transformer) buildMetadata(a *Article) map[string]any {
	return map[string]any{
		"title":         a.Title,
		"creation_date": a.PublishDate,
		"source_url":    a.Link,
	}
}

// -----------------------------------------------------------------------------
// POSTing (≈ post_item)
// -----------------------------------------------------------------------------

func (t *Transformer) postItem(ctx context.Context, htmlContent string, metadata map[string]any, collectionID *int) error {
	// build multipart body
	var body bytes.Buffer
	mp := multipart.NewWriter(&body)

	_ = mp.WriteField("html_content", htmlContent)
	metaBytes, _ := json.Marshal(metadata)
	_ = mp.WriteField("metadata", string(metaBytes))
	if collectionID != nil {
		_ = mp.WriteField("collection_id", fmt.Sprintf("%d", *collectionID))
	}
	mp.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiBase+"/omnipub", &body)
	if err != nil {
		return err
	}
	req.Header = t.headers.Clone()
	req.Header.Set("Content-Type", mp.FormDataContentType())

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	slurp, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return fmt.Errorf("http %d %s", resp.StatusCode, strings.TrimSpace(string(slurp)))
}

/* ---------- worker-friendly wrapper ---------- */

func (t *Transformer) processFile(ctx context.Context, file string, collectionID *int) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	var art Article
	if err := json.NewDecoder(f).Decode(&art); err != nil {
		return err
	}

	return t.postItem(ctx, t.buildHTML(&art), t.buildMetadata(&art), collectionID)
}

/* ============================================================================
   MAIN – Worker‑pool that saturates the API
============================================================================ */

func main() {
	dir := flag.String("dir", ".", "Directory with .json files")
	retryFile := flag.String("retry", "", "File with list of failed files to retry")
	api := flag.String("api", "https://api.example.com/v2", "Omnipub API base")
	collection := flag.Int("collection", 0, "Optional collection_id")
	workers := flag.Int("workers", 10, "Concurrent workers (≈ open TCP conns)")
	backoff := flag.Int("backoff", 0, "Backoff interval in milliseconds between retries (0 = no backoff)")
	maxConns := flag.Int("max-conns", 256, "Max connections per host (sets Transport)")
	apiKeyEnv := flag.String("key-env", "OMNIPUB_API_KEY", "Env var with API key")
	saveFailures := flag.String("save-failures", "", "Save paths of failed files to this file")
	flag.Parse()

	transformer, err := NewTransformer(*api, *apiKeyEnv, *maxConns)
	if err != nil {
		log.Fatal(err)
	}

	var files []string

	// Handle retry file if specified
	if *retryFile != "" {
		files, err = readFileList(*retryFile)
		if err != nil {
			log.Fatalf("Error reading retry file: %v", err)
		}
	} else {
		// Regular directory mode
		files, err = filepath.Glob(filepath.Join(*dir, "*.json"))
		if err != nil {
			log.Fatal(err)
		}
	}

	if len(files) == 0 {
		log.Println("No files to process – nothing to upload.")
		return
	}
	log.Printf("Uploading %d files with %d workers …", len(files), *workers)

	// --- concurrency primitives
	jobs := make(chan string, len(files))
	var ok, fail uint64
	var wg sync.WaitGroup
	ctx := context.Background()

	// To store failures if save-failures is specified
	var failures []string
	var failuresMutex sync.Mutex

	// spawn workers
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				// If backoff is specified, sleep for a short duration to avoid rate limiting
				if *backoff > 0 {
					time.Sleep(time.Duration(*backoff) * time.Millisecond)
				}

				if err := transformer.processFile(ctx, f, func() *int {
					if *collection > 0 {
						return collection
					}
					return nil
				}()); err != nil {
					atomic.AddUint64(&fail, 1)
					log.Printf("FAIL  %s → %v", f, err)

					// Store failure if requested
					if *saveFailures != "" {
						failuresMutex.Lock()
						failures = append(failures, f)
						failuresMutex.Unlock()
					}
				} else {
					atomic.AddUint64(&ok, 1)
				}
			}
		}()
	}

	// enqueue work
	for _, f := range files {
		jobs <- f
	}
	close(jobs)
	wg.Wait()

	// Save failures to file if requested
	if *saveFailures != "" && len(failures) > 0 {
		err := saveFilesToFile(*saveFailures, failures)
		if err != nil {
			log.Printf("Error saving failures file: %v", err)
		} else {
			log.Printf("Saved %d failed paths to %s", len(failures), *saveFailures)
		}
	}

	fmt.Printf("Done. Success: %d  Failure: %d\n", ok, fail)
}

// readFileList reads a list of files from a text file, one path per line
func readFileList(filePath string) ([]string, error) {
	var files []string

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		path := strings.TrimSpace(scanner.Text())
		if path != "" {
			files = append(files, path)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return files, nil
}

// saveFilesToFile saves a list of file paths to a text file
func saveFilesToFile(outputFile string, files []string) error {
	file, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for _, f := range files {
		_, err := writer.WriteString(f + "\n")
		if err != nil {
			return err
		}
	}

	return writer.Flush()
}
