package api

import (
	"context"
	"fmt"
	"net/http"
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

// ErrClaimConflict reports that a claim was refused because someone else holds
// the bead, the bead is closed, or the session already holds a different
// claim. It is a normal outcome of racing for routed work, not a fault, so the
// caller can drain rather than fail.
var ErrClaimConflict = fmt.Errorf("claim conflict")

// WorkerClaim claims beadID for assignee on behalf of sessionID via
// POST /v0/city/{cityName}/worker/claim. A 409 is returned as ErrClaimConflict
// so a caller can branch on a lost race without string-matching a message.
//
// observedClaim is the session's current-claim pointer as this caller read it
// before deciding to claim; it becomes the compare-and-swap's expected value.
// Passing "" is honest about having read nothing and leaves the server to derive
// the expected value — it does not assert that the pointer is empty.
func (c *Client) WorkerClaim(sessionID, assignee, beadID, observedClaim string) (beads.Bead, error) {
	if err := c.requireCityScope(); err != nil {
		return beads.Bead{}, err
	}
	body := genclient.PostV0CityByCityNameWorkerClaimJSONRequestBody{
		SessionId: sessionID,
		Assignee:  assignee,
		BeadId:    beadID,
	}
	if observedClaim != "" {
		body.CurrentClaim = &observedClaim
	}
	resp, err := c.cw.PostV0CityByCityNameWorkerClaimWithResponse(
		context.Background(), c.cityName, nil, body)
	if err != nil {
		return beads.Bead{}, &connError{err: fmt.Errorf("request failed: %w", err)}
	}
	if resp == nil {
		return beads.Bead{}, &connError{err: fmt.Errorf("nil response")}
	}
	if resp.StatusCode() == 409 {
		return beads.Bead{}, ErrClaimConflict
	}
	if err := checkWorkerRead("POST /worker/claim", resp); err != nil {
		return beads.Bead{}, err
	}
	if resp.JSON200 == nil {
		return beads.Bead{}, fmt.Errorf("API returned %d with no body", resp.StatusCode())
	}
	return beadFromGen(resp.JSON200.Bead), nil
}

// WorkerRelease gives beadID back when it still names assignee, via
// DELETE /v0/city/{cityName}/worker/claim. It reports whether the release
// actually applied; false means the snapshot moved, which is an outcome and
// not an error (the ReleaseIfCurrent contract).
func (c *Client) WorkerRelease(sessionID, assignee, beadID string) (bool, error) {
	if err := c.requireCityScope(); err != nil {
		return false, err
	}
	resp, err := c.cw.DeleteV0CityByCityNameWorkerClaimWithResponse(
		context.Background(), c.cityName, nil,
		genclient.DeleteV0CityByCityNameWorkerClaimJSONRequestBody{
			SessionId: &sessionID,
			Assignee:  assignee,
			BeadId:    beadID,
		})
	if err := checkWorkerMutation("DELETE /worker/claim", resp, err); err != nil {
		return false, err
	}
	if resp.JSON200 == nil {
		return false, fmt.Errorf("API returned %d with no body", resp.StatusCode())
	}
	return resp.JSON200.Status == "released", nil
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

// WorkerCloseBead closes a work bead via POST /v0/city/{cityName}/worker/close.
func (c *Client) WorkerCloseBead(beadID string) error {
	if err := c.requireCityScope(); err != nil {
		return err
	}
	resp, err := c.cw.PostV0CityByCityNameWorkerCloseWithResponse(
		context.Background(), c.cityName, nil,
		genclient.PostV0CityByCityNameWorkerCloseJSONRequestBody{BeadId: beadID})
	return checkWorkerMutation("POST /worker/close", resp, err)
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
