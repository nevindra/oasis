package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nevindra/oasis"
)

// batchResponse is the top-level JSON returned by both create and get batch endpoints.
// The actual batch data lives inside the Metadata field.
type batchResponse struct {
	Name     string        `json:"name"`
	Metadata batchMetadata `json:"metadata"`
}

// batchMetadata holds the batch job details nested under the "metadata" key.
type batchMetadata struct {
	State       string          `json:"state"`
	DisplayName string          `json:"displayName"`
	CreateTime  string          `json:"createTime"`
	UpdateTime  string          `json:"updateTime"`
	BatchStats  *batchStatsJSON `json:"batchStats"`
	Output      *batchOutput    `json:"output"`
}

type batchStatsJSON struct {
	RequestCount          stringInt `json:"requestCount"`
	SucceededRequestCount stringInt `json:"successfulRequestCount"`
	FailedRequestCount    stringInt `json:"failedRequestCount"`
	PendingRequestCount   stringInt `json:"pendingRequestCount"`
}

// stringInt handles JSON numbers encoded as strings (e.g. "10" instead of 10).
type stringInt int

func (s *stringInt) UnmarshalJSON(data []byte) error {
	// Try as number first.
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*s = stringInt(n)
		return nil
	}
	// Try as quoted string.
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	if str == "" {
		*s = 0
		return nil
	}
	_, err := fmt.Sscanf(str, "%d", &n)
	*s = stringInt(n)
	return err
}

type batchOutput struct {
	InlinedResponses *batchInlinedResponseList `json:"inlinedResponses"`
}

type batchInlinedResponseList struct {
	InlinedResponses []batchInlinedResponse `json:"inlinedResponses"`
}

type batchInlinedResponse struct {
	Response geminiResponse `json:"response"`
}

// BatchChat submits multiple chat requests as an inline batch job.
func (g *Gemini) BatchChat(ctx context.Context, requests []oasis.ChatRequest) (oasis.BatchJob, error) {
	inlineReqs := make([]map[string]any, 0, len(requests))
	for i, req := range requests {
		body, err := g.buildBody(req.Messages, nil, req.ResponseSchema, req.GenerationParams)
		if err != nil {
			return oasis.BatchJob{}, g.wrapErr(fmt.Sprintf("build body for request %d: %s", i, err))
		}
		inlineReqs = append(inlineReqs, map[string]any{
			"request":  body,
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

	url := fmt.Sprintf("%s/models/%s:batchGenerateContent?key=%s", baseURL, g.model, g.apiKey)
	return g.doBatchRequest(ctx, url, payload)
}

// BatchStatus returns the current state of a batch job.
func (g *Gemini) BatchStatus(ctx context.Context, jobID string) (oasis.BatchJob, error) {
	url := fmt.Sprintf("%s/%s?key=%s", baseURL, jobID, g.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return oasis.BatchJob{}, g.wrapErr("create status request: " + err.Error())
	}

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return oasis.BatchJob{}, g.wrapErr("status request failed: " + err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return oasis.BatchJob{}, g.wrapErr("read status response: " + err.Error())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oasis.BatchJob{}, httpErr(resp, string(respBody))
	}

	var br batchResponse
	if err := json.Unmarshal(respBody, &br); err != nil {
		return oasis.BatchJob{}, g.wrapErr("parse status response: " + err.Error())
	}

	return toBatchJob(br), nil
}

// BatchChatResults retrieves chat responses for a completed batch job.
// Returns error if the job has not yet succeeded.
func (g *Gemini) BatchChatResults(ctx context.Context, jobID string) ([]oasis.ChatResponse, error) {
	job, err := g.BatchStatus(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job.State != oasis.BatchSucceeded {
		return nil, g.wrapErr(fmt.Sprintf("batch job not completed: state=%s", job.State))
	}

	// Re-fetch the full response to get inlined results.
	url := fmt.Sprintf("%s/%s?key=%s", baseURL, jobID, g.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, g.wrapErr("create results request: " + err.Error())
	}

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, g.wrapErr("results request failed: " + err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, g.wrapErr("read results response: " + err.Error())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpErr(resp, string(respBody))
	}

	var br batchResponse
	if err := json.Unmarshal(respBody, &br); err != nil {
		return nil, g.wrapErr("parse results response: " + err.Error())
	}

	if br.Metadata.Output == nil || br.Metadata.Output.InlinedResponses == nil {
		return nil, g.wrapErr("no results in batch response")
	}

	inlined := br.Metadata.Output.InlinedResponses.InlinedResponses
	results := make([]oasis.ChatResponse, 0, len(inlined))
	for _, item := range inlined {
		results = append(results, parseGeminiResponse(item.Response))
	}

	return results, nil
}

// BatchCancel requests cancellation of a running or pending batch job.
func (g *Gemini) BatchCancel(ctx context.Context, jobID string) error {
	url := fmt.Sprintf("%s/%s:cancel?key=%s", baseURL, jobID, g.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return g.wrapErr("create cancel request: " + err.Error())
	}

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return g.wrapErr("cancel request failed: " + err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return httpErr(resp, string(b))
	}

	return nil
}

// doBatchRequest sends a POST with the given payload and parses the batch job response.
func (g *Gemini) doBatchRequest(ctx context.Context, url string, payload map[string]any) (oasis.BatchJob, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return oasis.BatchJob{}, g.wrapErr("marshal batch request: " + err.Error())
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		return oasis.BatchJob{}, g.wrapErr("create batch request: " + err.Error())
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return oasis.BatchJob{}, g.wrapErr("batch request failed: " + err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return oasis.BatchJob{}, g.wrapErr("read batch response: " + err.Error())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oasis.BatchJob{}, httpErr(resp, string(respBody))
	}

	var br batchResponse
	if err := json.Unmarshal(respBody, &br); err != nil {
		return oasis.BatchJob{}, g.wrapErr("parse batch response: " + err.Error())
	}

	return toBatchJob(br), nil
}

// parseGeminiResponse converts a geminiResponse into an oasis.ChatResponse.
// Reuses the same parsing logic as doGenerate.
func parseGeminiResponse(parsed geminiResponse) oasis.ChatResponse {
	var content strings.Builder
	var thinking strings.Builder
	var toolCalls []oasis.ToolCall

	if len(parsed.Candidates) > 0 {
		for _, part := range parsed.Candidates[0].Content.Parts {
			if part.Thought {
				if part.Text != nil {
					thinking.WriteString(*part.Text)
				}
				continue
			}
			if part.Text != nil {
				content.WriteString(*part.Text)
			}
			if part.FunctionCall != nil {
				tc := oasis.ToolCall{
					ID:   part.FunctionCall.Name,
					Name: part.FunctionCall.Name,
					Args: part.FunctionCall.Args,
				}
				if part.ThoughtSignature != "" {
					meta, _ := json.Marshal(map[string]string{
						"thoughtSignature": part.ThoughtSignature,
					})
					tc.Metadata = meta
				}
				toolCalls = append(toolCalls, tc)
			}
		}
	}

	var usage oasis.Usage
	if parsed.UsageMetadata != nil {
		usage.InputTokens = parsed.UsageMetadata.PromptTokenCount
		usage.OutputTokens = parsed.UsageMetadata.CandidatesTokenCount
	}

	return oasis.ChatResponse{
		Content:   content.String(),
		Thinking:  thinking.String(),
		ToolCalls: toolCalls,
		Usage:     usage,
	}
}

// toBatchJob converts a Gemini batch response into an oasis.BatchJob.
func toBatchJob(br batchResponse) oasis.BatchJob {
	m := br.Metadata
	job := oasis.BatchJob{
		ID:          br.Name,
		State:       mapBatchState(m.State),
		DisplayName: m.DisplayName,
	}

	if m.BatchStats != nil {
		job.Stats = oasis.BatchStats{
			TotalCount:     int(m.BatchStats.RequestCount),
			SucceededCount: int(m.BatchStats.SucceededRequestCount),
			FailedCount:    int(m.BatchStats.FailedRequestCount),
		}
	}

	if t, err := time.Parse(time.RFC3339Nano, m.CreateTime); err == nil {
		job.CreateTime = t
	}
	if t, err := time.Parse(time.RFC3339Nano, m.UpdateTime); err == nil {
		job.UpdateTime = t
	}

	return job
}

// mapBatchState converts a Gemini batch state string to an oasis.BatchState.
func mapBatchState(state string) oasis.BatchState {
	switch state {
	case "BATCH_STATE_PENDING", "JOB_STATE_PENDING":
		return oasis.BatchPending
	case "BATCH_STATE_RUNNING", "JOB_STATE_RUNNING":
		return oasis.BatchRunning
	case "BATCH_STATE_SUCCEEDED", "JOB_STATE_SUCCEEDED":
		return oasis.BatchSucceeded
	case "BATCH_STATE_FAILED", "JOB_STATE_FAILED":
		return oasis.BatchFailed
	case "BATCH_STATE_CANCELLED", "JOB_STATE_CANCELLED":
		return oasis.BatchCancelled
	case "BATCH_STATE_EXPIRED", "JOB_STATE_EXPIRED":
		return oasis.BatchExpired
	default:
		return oasis.BatchState(state)
	}
}

// Compile-time interface assertion.
var _ oasis.BatchProvider = (*Gemini)(nil)
