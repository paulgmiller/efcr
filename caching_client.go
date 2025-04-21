package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"log"
)

type CachingClient struct {
	CacheDir string
	Client   httpclient
}

func NewCachingClient(cacheDir string, client httpclient) *CachingClient {
	return &CachingClient{
		CacheDir: cacheDir,
		Client:   client,
	}
}

func (c *CachingClient) Do(req *http.Request) (*http.Response, error) {
	// Generate a cache key based on the request URL
	cacheKey := cacheKey(req.URL.String())
	cachePath := filepath.Join(c.CacheDir, cacheKey)

	// Check if the response is already cached
	if cachedResponse, err := os.Open(cachePath); err == nil {
		return &http.Response{
			Request:       req,
			Header:        make(http.Header),
			Body:          cachedResponse,
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Proto:         "HTTP/1.1",
			ContentLength: -1,
		}, nil
	}

	// If not cached, make the request
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()

	// Cache the response body
	cacheFile, err := os.Create(cachePath)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(cacheFile, resp.Body); err != nil {
		cacheFile.Close()
		return nil, err
	}
	cacheFile.Close()
	log.Printf("Cached response for %s to %s", req.URL.String(), cachePath)

	// Return a new response based on the cached data
	cachedResponse, err := os.Open(cachePath)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		Request:       req,
		Header:        resp.Header.Clone(),
		Body:          cachedResponse,
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ContentLength: -1,
	}, nil
}

func cacheKey(url string) string {
	hash := sha256.Sum256([]byte(url))
	return hex.EncodeToString(hash[:])
}
