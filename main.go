// Command ecfr_title_versions counts versions (“works”) for every eCFR title.
//
// Build & run:
//
//	go mod init example.com/ecfr_title_versions
//	go mod tidy
//	go run .
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	"io/ioutil"

	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const (
	baseURL      = "https://www.ecfr.gov/api/versioner/v1"
	titlesURL    = baseURL + "/titles.json"
	versionsURL  = baseURL + "/versions/title-%d.json" // %s = title number
	structureURL = baseURL + "/structure/%s/title-%d.json"
	fullURL      = baseURL + "/full/%s/title-%d.xml"
	maxWorkers   = 6 // tweak for desired parallelism
	requestLimit = 10 * time.Second
)

type Title struct {
	Number int    `json:"number"`
	Name   string `json:"name"`
}

//https://www.ecfr.gov/api/versioner/v1/api/versioner/v1/structure/2025-03-31/title-37.json
//https://www.ecfr.gov/api/versioner/v1/structure/2025-03-31/title-37.json

// titlesResponse matches /titles.json
type titlesResponse struct {
	Titles []Title `json:"titles"`
}

/*
	{
	     "date": "2017-01-19",
	     "amendment_date": "2017-01-19",
	     "issue_date": "2017-01-19",
	     "identifier": "3474.20",
	     "name": "§ 3474.20   xxx",
	     "part": "3474",
	     "substantive": true,
	     "removed": false,
	     "subpart": null,
	     "title": "2",
	     "type": "section"
	   },
*/
type titleversion struct {
	Date          string  `json:"date"`
	AmendmentDate string  `json:"amendment_date"`
	IssueDate     string  `json:"issue_date"`
	Identifier    string  `json:"identifier"`
	Name          string  `json:"name"`
	Part          string  `json:"part"`
	Substantive   bool    `json:"substantive"`
	Removed       bool    `json:"removed"`
	Subpart       *string `json:"subpart"` // Pointer to handle null values
	Title         string  `json:"title"`
	Type          string  `json:"type"`
}

// versionsResponse matches /versions/title-{n}.json
type versionsResponse struct {
	Versions []titleversion `json:"content_versions"` // we only need the count
}

var testdata string = `{"content_versions":[{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"11.4","name":"§ 11.4   Collection by administrative offset.","part":"11","substantive":true,"removed":false,"subpart":"A","title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"115.5","name":"§ 115.5   General definitions.","part":"115","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"115.6","name":"§ 115.6   Definitions related to sexual abuse and assault.","part":"115","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"21.4","name":"§ 21.4   Definitions.","part":"21","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"Appendix A to Part 21","name":"Appendix A to Part 21 - Activities to Which This Part Applies","part":"21","substantive":true,"removed":false,"subpart":null,"title":"6","type":"appendix"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"Appendix B to Part 21","name":"Appendix B to Part 21 - Activities to Which This Part Applies When a Primary Objective of the Federal Financial Assistance Is To Provide Employment","part":"21","substantive":true,"removed":false,"subpart":null,"title":"6","type":"appendix"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2022-03-15","identifier":"25.2","name":"§ 25.2   Definitions.","part":"25","substantive":false,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"25.2","name":"§ 25.2   Definitions.","part":"25","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"25.6","name":"§ 25.6   Procedures for designation of qualified anti-terrorism technologies.","part":"25","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"Appendix A to Part 27","name":"Appendix A to Part 27 - DHS Chemicals of Interest","part":"27","substantive":true,"removed":false,"subpart":null,"title":"6","type":"appendix"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"29.4","name":"§ 29.4   Protected Critical Infrastructure Information Program administration.","part":"29","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"3.3","name":"§ 3.3   Applicability.","part":"3","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"}]}`

type httpclient interface {
	Do(req *http.Request) (*http.Response, error)
}

type RateLimitedClient struct {
	Client      httpclient
	RateLimiter *time.Ticker
}

func NewRateLimitedClient(client httpclient, rate time.Duration) *RateLimitedClient {
	return &RateLimitedClient{
		Client:      client,
		RateLimiter: time.NewTicker(rate),
	}
}

func (rlc *RateLimitedClient) Do(req *http.Request) (*http.Response, error) {
	<-rlc.RateLimiter.C
	return rlc.Client.Do(req)
}

func main() {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// reusable HTTP client with timeout
	client := NewCachingClient("cache", NewRateLimitedClient(&http.Client{}, 3*time.Second))

	// 1. Fetch all titles
	var tResp titlesResponse
	if err := fetchJSON(ctx, client, titlesURL, &tResp); err != nil {
		log.Fatalf("fetch titles: %v", err)
	}

	// 2. Concurrently fetch versions per title
	type result struct {
		title string
		count int32
		err   []error
	}
	results := make(chan result)

	for _, t := range tResp.Titles {
		go func(title Title) {
			url := fmt.Sprintf(versionsURL, title.Number)
			var vResp versionsResponse
			dates := map[string]bool{}
			if err := fetchJSON(ctx, client, url, &vResp); err != nil {
				results <- result{title: title.Name, count: 0, err: []error{err}}
			}
			for _, v := range vResp.Versions {
				if v.Substantive && !v.Removed {
					dates[v.Date] = true
				}
			}

			dateresults := make(chan result)
			for d := range dates {
				go func(d string) {
					furl := fmt.Sprintf(fullURL, d, title.Number)
					data, err := fetchXML(ctx, client, furl)
					if err != nil {
						log.Printf("fetch %s: %v", furl, err)
						dateresults <- result{title: title.Name, count: 0, err: []error{err}}
						return
					}

					var count int32
					scanner := bufio.NewScanner(strings.NewReader(data))
					scanner.Split(bufio.ScanWords) //segment.SplitWords)
					for scanner.Scan() {
						count++
					}
					if err := scanner.Err(); err != nil {
						log.Fatalf("scanner fail , %d %s %s %v", count, data[:30], cacheKey(furl), err)
						dateresults <- result{title: title.Name, count: 0, err: []error{err}}
					}

					/*seg := segment.NewWordSegmenter(strings.NewReader(sanitizeInput(ensureUTF8(data))))

					for seg.Segment() {
						count++
					}
					if seg.Err() != nil {
						log.Fatalf("segment %s: %v", data[:500], seg.Err())
						dateresults <- result{title: title.Name, count: 0, err: []error{seg.Err()}}
						return
					}*/
					fmt.Printf("Fetched date %d, %s, size %d, wordcount %d %s %s\n", title.Number, d, len(data), count, cacheKey(url), url)
					dateresults <- result{count: count, err: nil}
				}(d)
			}

			titleresult := result{title: title.Name}
			for range len(dates) {
				r := <-dateresults
				if r.err != nil {
					titleresult.err = append(titleresult.err, r.err...)
					continue
				}
				titleresult.count += r.count
			}

			results <- titleresult

		}(t)
	}

	// 3. Print report
	fmt.Println("Title\tVersionCount")
	for range len(tResp.Titles) {
		r := <-results
		if r.err != nil {
			fmt.Printf("%s\tERROR: %v\n", r.title, r.err)
			continue
		}
		fmt.Printf("%s\t%d\n", r.title, r.count)
	}
}

func ensureUTF8(input string) string {
	reader := transform.NewReader(strings.NewReader(input), xunicode.UTF8.NewDecoder())
	result, _ := ioutil.ReadAll(reader)
	return string(result)
}

func sanitizeInput(input string) string {
	var sanitized strings.Builder
	for _, r := range input {
		if unicode.IsPrint(r) || unicode.IsSpace(r) {
			sanitized.WriteRune(r)
		}
	}
	return sanitized.String()
}

// fetchJSON GETs url and decodes JSON into out.
func fetchJSON(ctx context.Context, c httpclient, url string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, url)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// fetchJSON GETs url and decodes JSON into out.
func fetchXML(ctx context.Context, c httpclient, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/xml")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := resp.Header.Get("Retry-After")
			log.Printf("HTTP 429 Too Many Requests. Retry-After: %s", retryAfter)
		}
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, url)
	}
	return plainText(resp.Body)
}

func plainText(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var sb strings.Builder

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if ch, ok := tok.(xml.CharData); ok {
			sb.Write(bytes.TrimSpace(ch)) // strips CR/LF/indent
			sb.WriteByte(' ')             // word boundary
		}
	}
	//returna  reader? with pipe?
	return sb.String(), nil
}
