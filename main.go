// Command ecfr_title_versions counts versions (“works”) for every eCFR title.
//
// Build & run:
//
//	go mod init example.com/ecfr_title_versions
//	go mod tidy
//	go run .
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/blevesearch/segment"
	"github.com/samber/lo"
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
	Date       string `json:"date"`
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
}

// versionsResponse matches /versions/title-{n}.json
type versionsResponse struct {
	Versions []titleversion `json:"content_versions"` // we only need the count
}

var testdata string = `{"content_versions":[{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"11.4","name":"§ 11.4   Collection by administrative offset.","part":"11","substantive":true,"removed":false,"subpart":"A","title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"115.5","name":"§ 115.5   General definitions.","part":"115","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"115.6","name":"§ 115.6   Definitions related to sexual abuse and assault.","part":"115","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"21.4","name":"§ 21.4   Definitions.","part":"21","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"Appendix A to Part 21","name":"Appendix A to Part 21 - Activities to Which This Part Applies","part":"21","substantive":true,"removed":false,"subpart":null,"title":"6","type":"appendix"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"Appendix B to Part 21","name":"Appendix B to Part 21 - Activities to Which This Part Applies When a Primary Objective of the Federal Financial Assistance Is To Provide Employment","part":"21","substantive":true,"removed":false,"subpart":null,"title":"6","type":"appendix"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2022-03-15","identifier":"25.2","name":"§ 25.2   Definitions.","part":"25","substantive":false,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"25.2","name":"§ 25.2   Definitions.","part":"25","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"25.6","name":"§ 25.6   Procedures for designation of qualified anti-terrorism technologies.","part":"25","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"Appendix A to Part 27","name":"Appendix A to Part 27 - DHS Chemicals of Interest","part":"27","substantive":true,"removed":false,"subpart":null,"title":"6","type":"appendix"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"29.4","name":"§ 29.4   Protected Critical Infrastructure Information Program administration.","part":"29","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"},{"date":"2016-12-22","amendment_date":"2016-12-22","issue_date":"2016-12-22","identifier":"3.3","name":"§ 3.3   Applicability.","part":"3","substantive":true,"removed":false,"subpart":null,"title":"6","type":"section"}]}`

func main() {

	ctx, cancel := context.WithTimeout(context.Background(), 100*requestLimit)
	defer cancel()

	// reusable HTTP client with timeout
	client := &http.Client{Timeout: requestLimit}

	// 1. Fetch all titles
	var tResp titlesResponse
	if err := fetchJSON(ctx, client, titlesURL, &tResp); err != nil {
		log.Fatalf("fetch titles: %v", err)
	}

	// 2. Concurrently fetch versions per title
	type result struct {
		title string
		count int
		err   error
	}
	jobs := make(chan Title)
	results := make(chan result)

	var wg sync.WaitGroup
	for range maxWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for title := range jobs {
				url := fmt.Sprintf(versionsURL, title.Number)
				var vResp versionsResponse
				dates := map[string]bool{}
				if err := fetchJSON(ctx, client, url, &vResp); err == nil {
					for _, v := range vResp.Versions {
						dates[v.Date] = true
					}
				}
				fmt.Printf("Fetched %s %d versions(%d) dates(%d) %s\n", title.Name, title.Number, len(vResp.Versions), len(lo.Keys(dates)), url)

				count := 0
				for d := range dates {
					url = fmt.Sprintf(fullURL, d, title.Number)
					data, err := fetchXML(ctx, client, url)
					if err != nil {
						continue //need mulit error tracking
					}

					seg := segment.NewWordSegmenter(strings.NewReader(data))

					datecount := 0
					for seg.Segment() {
						datecount++
					}
					fmt.Printf("Fetched date %d, %s, size%d, wordcount%d\n", title.Number, d, len(data), datecount)
					count += datecount
				}

				results <- result{title: title.Name, count: count, err: nil}
			}
		}()
	}

	go func() {
		for _, t := range tResp.Titles {
			//fmt.Printf("Fetching %s (%d)\n", t.Name, t.Number)
			jobs <- t
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	// 3. Print report
	fmt.Println("Title\tVersionCount")
	for r := range results {
		if r.err != nil {
			fmt.Printf("%s\tERROR: %v\n", r.title, r.err)
			continue
		}
		fmt.Printf("%s\t%d\n", r.title, r.count)
	}
}

// fetchJSON GETs url and decodes JSON into out.
func fetchJSON(ctx context.Context, c *http.Client, url string, out interface{}) error {
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
func fetchXML(ctx context.Context, c *http.Client, url string) (string, error) {
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
