package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/tui"
)

// localOllama is a provider that runs models on this machine through Ollama,
// so a model it does not have can be downloaded (Ollama Cloud's cannot).
func localOllama(p config.Provider) bool {
	return p.Discover == "ollama" && !strings.Contains(p.BaseURL, "ollama.com")
}

// pullModel downloads a model into Ollama with POST /api/pull. Verified on
// Ollama 0.13.5: the reply is one JSON object per line — {"status":"pulling
// manifest"}, then per layer {"status":"pulling <digest>","digest","total",
// "completed"}, then {"status":"success"}; a bad name is {"error":"pull model
// manifest: file does not exist"}. Progress sums bytes over the layers seen.
func pullModel(ctx context.Context, baseURL, model string, progress func(tui.Progress)) error {
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaRoot(baseURL)+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req) // no timeout: a model is gigabytes; ctx stops it
	if err != nil {
		return fmt.Errorf("download %s: %w", model, err)
	}
	defer res.Body.Close()
	layers := map[string][2]int64{} // digest → completed, total
	dec := json.NewDecoder(res.Body)
	for {
		var line struct {
			Status, Digest, Error string
			Total, Completed      int64
		}
		switch err := dec.Decode(&line); {
		case errors.Is(err, io.EOF):
			return fmt.Errorf("download %s: Ollama stopped before it finished", model)
		case err != nil && res.StatusCode != http.StatusOK:
			return fmt.Errorf("download %s: HTTP %d", model, res.StatusCode)
		case err != nil:
			return fmt.Errorf("download %s: %w", model, err)
		}
		if line.Error != "" {
			if strings.Contains(line.Error, "file does not exist") {
				return fmt.Errorf("Ollama has no model called %s; check the name at ollama.com/library", model)
			}
			return fmt.Errorf("download %s: %s", model, line.Error)
		}
		if line.Digest != "" {
			layers[line.Digest] = [2]int64{line.Completed, line.Total}
		}
		p := tui.Progress{Status: line.Status}
		for _, l := range layers {
			p.Done, p.Total = p.Done+l[0], p.Total+l[1]
		}
		progress(p)
		if line.Status == "success" {
			return nil
		}
	}
}

// ollamaRoot is the server address without the OpenAI-compatible /v1 path.
func ollamaRoot(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
}
