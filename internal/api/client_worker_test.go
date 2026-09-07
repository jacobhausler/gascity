package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/api/genclient"
)

// A 404 from the worker family has two very different causes, and only one of
// them is visible to the worker: a bead or session that is really missing, or a
// controller that predates the whole route family. The second is indistinguishable
// from the first without the hint, so the client adds it — and only when the
// response carries no detail of its own, which is what a router 404 looks like.
func TestWorkerRouteErrorHintsAtAnOlderCity(t *testing.T) {
	detail := func(s string) *genclient.ErrorModel {
		return &genclient.ErrorModel{Detail: &s}
	}
	base := errors.New("API returned 404")

	for _, tc := range []struct {
		name     string
		status   int
		pd       *genclient.ErrorModel
		wantHint bool
	}{
		{"bare router 404", http.StatusNotFound, nil, true},
		{"404 with an empty detail", http.StatusNotFound, detail("   "), true},
		{"handler said what was missing", http.StatusNotFound, detail("bead mc-7 not found"), false},
		{"not a 404 at all", http.StatusConflict, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := workerRouteError("GET /worker/current", tc.status, tc.pd, base)
			if !errors.Is(got, base) {
				t.Fatalf("the original error was lost: %v", got)
			}
			hinted := strings.Contains(got.Error(), "worker route family")
			if hinted != tc.wantHint {
				t.Fatalf("hint present = %v, want %v (%v)", hinted, tc.wantHint, got)
			}
		})
	}

	if got := workerRouteError("GET /worker/current", http.StatusNotFound, nil, nil); got != nil {
		t.Fatalf("a successful call was turned into an error: %v", got)
	}
}
