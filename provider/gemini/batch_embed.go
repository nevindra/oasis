package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nevindra/oasis"
)

// batchEmbedDest holds the destination field for batch embedding responses.
type batchEmbedDest struct {
	InlinedEmbedContentResponses []batchEmbedInlinedResponse `json:"inlinedEmbedContentResponses"`
}

type batchEmbedInlinedResponse struct {
	Embedding *embedValues `json:"embedding"`
}

// batchEmbedResponse is the top-level JSON for batch embedding endpoints.
type batchEmbedResponse struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	State       string          `json:"state"`
	CreateTime  string          `json:"createTime"`
	UpdateTime  string          `json:"updateTime"`
	BatchStats  *batchStatsJSON `json:"batchStats"`
	Dest        *batchEmbedDest `json:"dest"`
}

// BatchEmbed submits multiple embedding requests as an inline batch job.
func (e *GeminiEmbedding) BatchEmbed(ctx context.Context, texts [][]string) (oasis.BatchJob, error) {
	inlineReqs := make([]map[string]any, 0, len(texts))
	for i, group := range texts {
		parts := make([]map[string]any, 0, len(group))
		for _, text := range group {
			parts = append(parts, map[string]any{"text": text})
		}
		inlineReqs = append(inlineReqs, map[string]any{
			"request": map[string]any{
				"content": map[string]any{
					"parts": parts,
				},
				"outputDimensionality": e.dims,
			},
			"metadata": map[string]any{"key": fmt.Sprintf("req-%d", i)},
		})
	}

	payload := map[string]any{
		"batch": map[string]any{
			"input_config": map[string]any{
				"requests": map[string]any{
					"requests": inlineReqs,
				},
			},
		},
	}

	url := fmt.Sprintf("%s/models/%s:batchEmbedContent?key=%s", baseURL, e.model, e.apiKey)
	return e.doBatchRequest(ctx, url, payload)
}

// BatchEmbedStatus returns the current state of a batch embedding job.
func (e *GeminiEmbedding) BatchEmbedStatus(ctx context.Context, jobID string) (oasis.BatchJob, error) {
	url := fmt.Sprintf("%s/%s?key=%s", baseURL, jobID, e.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return oasis.BatchJob{}, e.wrapErr("create status request: " + err.Error())
	}

	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return oasis.BatchJob{}, e.wrapErr("status request failed: " + err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return oasis.BatchJob{}, e.wrapErr("read status response: " + err.Error())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oasis.BatchJob{}, httpErr(resp, string(respBody))
	}

	var br batchResponse
	if err := json.Unmarshal(respBody, &br); err != nil {
		return oasis.BatchJob{}, e.wrapErr("parse status response: " + err.Error())
	}

	return toBatchJob(br), nil
}

// BatchEmbedResults retrieves embedding vectors for a completed batch job.
func (e *GeminiEmbedding) BatchEmbedResults(ctx context.Context, jobID string) ([][]float32, error) {
	job, err := e.BatchEmbedStatus(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job.State != oasis.BatchSucceeded {
		return nil, e.wrapErr(fmt.Sprintf("batch job not completed: state=%s", job.State))
	}

	url := fmt.Sprintf("%s/%s?key=%s", baseURL, jobID, e.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, e.wrapErr("create results request: " + err.Error())
	}

	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return nil, e.wrapErr("results request failed: " + err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, e.wrapErr("read results response: " + err.Error())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpErr(resp, string(respBody))
	}

	var br batchEmbedResponse
	if err := json.Unmarshal(respBody, &br); err != nil {
		return nil, e.wrapErr("parse results response: " + err.Error())
	}

	if br.Dest == nil {
		return nil, e.wrapErr("no results in batch embedding response")
	}

	results := make([][]float32, 0, len(br.Dest.InlinedEmbedContentResponses))
	for _, inlined := range br.Dest.InlinedEmbedContentResponses {
		if inlined.Embedding == nil {
			results = append(results, nil)
			continue
		}
		vec := make([]float32, len(inlined.Embedding.Values))
		for j, v := range inlined.Embedding.Values {
			vec[j] = float32(v)
		}
		results = append(results, vec)
	}

	return results, nil
}

// doBatchRequest sends a POST with the given payload and parses the batch job response.
func (e *GeminiEmbedding) doBatchRequest(ctx context.Context, url string, payload map[string]any) (oasis.BatchJob, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return oasis.BatchJob{}, e.wrapErr("marshal batch request: " + err.Error())
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		return oasis.BatchJob{}, e.wrapErr("create batch request: " + err.Error())
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return oasis.BatchJob{}, e.wrapErr("batch request failed: " + err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return oasis.BatchJob{}, e.wrapErr("read batch response: " + err.Error())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oasis.BatchJob{}, httpErr(resp, string(respBody))
	}

	var br batchResponse
	if err := json.Unmarshal(respBody, &br); err != nil {
		return oasis.BatchJob{}, e.wrapErr("parse batch response: " + err.Error())
	}

	return toBatchJob(br), nil
}

func (e *GeminiEmbedding) wrapErr(msg string) error {
	return &oasis.ErrLLM{Provider: "gemini", Message: msg}
}

// Compile-time interface assertion.
var _ oasis.BatchEmbeddingProvider = (*GeminiEmbedding)(nil)
