package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gastownhall/gascity/internal/api/genclient"
	"github.com/gastownhall/gascity/internal/beads"
)

// The worker client methods are the caller-side half of the /worker family.
// They exist so a session running off the controller's host can do a worker's
// job over the API instead of against a local store — the routing ladder's
// remote leg for `gc hook` and the worker `gc bd` subset.
//
// None of them retries. A claim is a compare-and-swap whose outcome is
// meaningful, so a blind retry after an ambiguous transport failure could
// convert one claim into two attempts against a moved snapshot. Every method
// surfaces the first answer it gets.

// workerRouteError adds the one cause of a /worker/* 404 the worker itself
// cannot see: a controller that predates this route family, which answers 404
// for every route in it. Without the hint, a session whose city is simply too
// old reads "not found" and goes looking for a missing bead that was never the
// problem — and the check that would tell it apart lives on the other side of
// the wire.
//
// The hint is added only when the response carries no problem detail. Every 404
// this family raises deliberately says what was missing ("session x not found",
// "bead y not found"); a bare 404 with nothing in it is the router's, not a
// handler's.
func workerRouteError(route string, status int, pd *genclient.ErrorModel, err error) error {
	if err == nil || status != http.StatusNotFound {
		return err
	}
	if pd != nil && pd.Detail != nil && strings.TrimSpace(*pd.Detail) != "" {
		return err
	}
	return fmt.Errorf("%w; this city has no %s route — a controller older than the worker route family answers 404 for the whole family, so upgrade it or run this session on the controller's host", err, route)
}

// checkWorkerRead maps a worker read response to an error, with the hint.
func checkWorkerRead(route string, resp interface{ StatusCode() int }) error {
	pd := pdOf(resp)
	return workerRouteError(route, resp.StatusCode(), pd, apiErrorFromResponse(resp.StatusCode(), pd))
}

// checkWorkerMutation is checkMutation plus the route-family hint.
func checkWorkerMutation(route string, resp interface{ StatusCode() int }, transportErr error) error {
	err := checkMutation(resp, transportErr)
	if err == nil || resp == nil || isNil(resp) {
		return err
	}
	return workerRouteError(route, resp.StatusCode(), pdOf(resp), err)
}

// WorkerCurrent reads the bead the session most recently claimed via
// GET /v0/city/{cityName}/worker/current. An empty string means the session
// exists and has claimed nothing — the caller's own contract decides whether
// that is a failure.
func (c *Client) WorkerCurrent(sessionID string) (string, error) {
	if err := c.requireCityScope(); err != nil {
		return "", err
	}
	resp, err := c.cw.GetV0CityByCityNameWorkerCurrentWithResponse(
		context.Background(), c.cityName,
		&genclient.GetV0CityByCityNameWorkerCurrentParams{SessionId: sessionID})
	if err != nil {
		return "", &connError{err: fmt.Errorf("request failed: %w", err)}
	}
	if resp == nil {
		return "", &connError{err: fmt.Errorf("nil response")}
	}
	if err := checkWorkerRead("GET /worker/current", resp); err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", fmt.Errorf("API returned %d with no body", resp.StatusCode())
	}
	return resp.JSON200.BeadId, nil
}

// WorkerDrainAck acknowledges the session's drain via
// POST /v0/city/{cityName}/worker/drain-ack.
func (c *Client) WorkerDrainAck(sessionID string) error {
	if err := c.requireCityScope(); err != nil {
		return err
	}
	resp, err := c.cw.PostV0CityByCityNameWorkerDrainAckWithResponse(
		context.Background(), c.cityName, nil,
		genclient.PostV0CityByCityNameWorkerDrainAckJSONRequestBody{SessionId: sessionID})
	return checkWorkerMutation("POST /worker/drain-ack", resp, err)
}

// WorkerCommentBead appends a comment via
// POST /v0/city/{cityName}/worker/comment.
func (c *Client) WorkerCommentBead(beadID, text string) error {
	if err := c.requireCityScope(); err != nil {
		return err
	}
	resp, err := c.cw.PostV0CityByCityNameWorkerCommentWithResponse(
		context.Background(), c.cityName, nil,
		genclient.PostV0CityByCityNameWorkerCommentJSONRequestBody{BeadId: beadID, Text: text})
	return checkWorkerMutation("POST /worker/comment", resp, err)
}

// UpdateBeadOpts is the field subset a worker updates. Pointer fields keep
// "not set" distinct from "set to empty", matching the server's own
// absent-vs-null handling on the update route.
type UpdateBeadOpts struct {
	Title       *string
	Status      *string
	Description *string
	Assignee    *string
}

// UpdateBead applies a field update via POST /v0/city/{cityName}/bead/{id}/update.
// It is the caller-side half of the route `gc bd update` maps onto; the route
// already existed, so nothing new is registered for it.
func (c *Client) UpdateBead(id string, opts UpdateBeadOpts) error {
	if err := c.requireCityScope(); err != nil {
		return err
	}
	resp, err := c.cw.PostV0CityByCityNameBeadByIdUpdateWithResponse(
		context.Background(), c.cityName, id, nil,
		genclient.PostV0CityByCityNameBeadByIdUpdateJSONRequestBody{
			Title:       opts.Title,
			Status:      opts.Status,
			Description: opts.Description,
			Assignee:    opts.Assignee,
		})
	return checkMutation(resp, err)
}

// Client-side legs for the worker lifecycle family (cr-gdeav.5.4 draft).
//
// These requests are hand-built rather than generated because the four routes
// are new: the generated client is a projection of the committed OpenAPI spec,
// so the ops appear there only once the spec is regenerated (the EDGE PR's
// `make check-generated-docs-drift` step). Everything that matters about the
// transport is still the remote client's: the CSRF header, the live bearer, the
// per-request city-write grant, and the REST transport with its re-auth
// RoundTripper — which is why this file uses c.restClient instead of building
// an http.Client of its own.

// WorkerVerbRequest is the body the claim / heartbeat / release legs send. The
// claimant is the caller's identity (BEADS_ACTOR on the CLI), not the transport
// credential, so a shared bearer cannot silently become the owner of a bead.
type WorkerVerbRequest struct {
	SessionID    string `json:"session_id,omitempty"`
	Assignee     string `json:"assignee"`
	BeadID       string `json:"bead_id"`
	CurrentClaim string `json:"current_claim,omitempty"`
}

// WorkerCloseRequest is the body the typed close sends. Outcome carries the
// ADR-0009 disposition; Commit/Branch only accompany a shipped close.
type WorkerCloseRequest struct {
	// SessionID and Assignee carry the worker's ownership into the close, which
	// is what lets the server refuse a stranger's close instead of writing
	// whatever work record the caller typed.
	SessionID string `json:"session_id,omitempty"`
	Assignee  string `json:"assignee"`
	BeadID    string `json:"bead_id"`
	Outcome   string `json:"outcome"`
	Commit    string `json:"commit,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// WorkerVerbResult is what a worker verb reports back. Status is the verb's own
// result vocabulary (claimed / renewed / released / skipped / closed) and Bead
// is the row as the server persisted it, so the CLI can answer with the same
// shape its local leg does instead of reading again.
type WorkerVerbResult struct {
	Status   string
	Bead     beads.Bead
	Revision string // precondition token in plain form ("" when the store has none)
	// Skipped reports a release that found a different holder — a result, not
	// an error, which is why it is a field rather than an err.
	Skipped bool
}

// WorkerClaim acquires a bead for assignee over POST /worker/claim.
func (c *Client) WorkerClaim(ctx context.Context, req WorkerVerbRequest) (WorkerVerbResult, error) {
	return c.doWorkerVerb(ctx, http.MethodPost, "/worker/claim", req)
}

// WorkerHeartbeat refreshes the lease over POST /worker/heartbeat.
func (c *Client) WorkerHeartbeat(ctx context.Context, req WorkerVerbRequest) (WorkerVerbResult, error) {
	return c.doWorkerVerb(ctx, http.MethodPost, "/worker/heartbeat", req)
}

// WorkerRelease hands a claim back over DELETE /worker/claim. A different
// holder is Skipped with a nil error.
func (c *Client) WorkerRelease(ctx context.Context, req WorkerVerbRequest) (WorkerVerbResult, error) {
	return c.doWorkerVerb(ctx, http.MethodDelete, "/worker/claim", req)
}

// WorkerClose closes with a typed work record over POST /worker/close.
func (c *Client) WorkerClose(ctx context.Context, req WorkerCloseRequest) (WorkerVerbResult, error) {
	return c.doWorkerVerb(ctx, http.MethodPost, "/worker/close", req)
}

// workerVerbResponse is the server's response envelope.
type workerVerbResponse struct {
	Status string     `json:"status"`
	Bead   beads.Bead `json:"bead"`
}

func (c *Client) doWorkerVerb(ctx context.Context, method, tail string, payload any) (WorkerVerbResult, error) {
	var out WorkerVerbResult
	if err := c.requireCityScope(); err != nil {
		return out, err
	}
	if !c.isRemote || c.restClient == nil {
		return out, fmt.Errorf("api: worker lifecycle verbs are remote-only; build the client with NewRemoteCityScopedClient")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return out, fmt.Errorf("api: encoding worker request: %w", err)
	}
	endpoint, err := url.JoinPath(strings.TrimRight(c.baseURL, "/"), "/v0/city/", c.cityName, strings.TrimPrefix(tail, "/"))
	if err != nil {
		return out, fmt.Errorf("api: building worker URL for %q: %w", tail, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("api: building worker request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GC-Request", "true")
	if tok, err := c.bearerToken(); err != nil {
		return out, err
	} else if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if err := c.attachCityWriteGrant(req); err != nil {
		return out, err
	}

	resp, err := c.restClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("api: %s %s: %w", method, tail, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read below
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return out, fmt.Errorf("api: reading %s %s response: %w", method, tail, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return out, &WorkerVerbHTTPError{Method: method, Path: tail, StatusCode: resp.StatusCode, Body: snippet(raw)}
	}
	var parsed workerVerbResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return out, fmt.Errorf("api: decoding %s %s output: %w (%s)", method, tail, err, snippet(raw))
	}
	out.Status = parsed.Status
	out.Bead = parsed.Bead
	out.Skipped = parsed.Status == "skipped"
	out.Revision = resp.Header.Get("X-GC-Revision")
	return out, nil
}

// WorkerVerbHTTPError is a non-2xx answer from a worker route. The status code
// stays a NUMBER because the caller branches on it: a 409 lost claim is a
// normal unwind (exit non-zero, say who holds it), a 501 means the city's store
// cannot serve the verb at all, and a 401/403 is a grant problem — flattening
// them into one string would throw away the only signal that separates them.
type WorkerVerbHTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *WorkerVerbHTTPError) Error() string {
	return fmt.Sprintf("api: %s %s: %d %s: %s", e.Method, e.Path, e.StatusCode, http.StatusText(e.StatusCode), e.Body)
}

// WorkerVerbStatusCode reports the HTTP status a worker verb came back with,
// and whether the failure was an HTTP answer at all.
func WorkerVerbStatusCode(err error) (int, bool) {
	var httpErr *WorkerVerbHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode, true
	}
	return 0, false
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 400 {
		return s[:400] + "\u2026"
	}
	return s
}
