package proxy

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"time"
)

// CompressContext sends the request body to the compression sidecar (Python)
// and returns the compressed body. If the sidecar is unavailable or fails,
// it returns the original body to ensure reliability.
func (g *Gateway) CompressContext(body []byte) ([]byte, error) {
	if !g.Compression.EnableRTK && !g.Compression.EnableHeadroom && !g.Compression.EnableLLMLingua {
		return body, nil
	}

	// Only compress if the sidecar is configured and potentially alive
	sidecarURL := "http://127.0.0.1:7788/compress"
	
	req, err := http.NewRequest("POST", sidecarURL, bytes.NewBuffer(body))
	if err != nil {
		return body, err
	}
	req.Header.Set("Content-Type", "application/json")

	// Set a reasonable timeout for compression
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Log error but continue with original body
		log.Printf("[COMPRESSION] Sidecar unavailable: %v", err)
		return body, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[COMPRESSION] Sidecar returned status %d", resp.StatusCode)
		return body, nil
	}

	compressedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return body, err
	}

	log.Printf("[COMPRESSION] Reduced body from %d to %d bytes", len(body), len(compressedBody))
	return compressedBody, nil
}
