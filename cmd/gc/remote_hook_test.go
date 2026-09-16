package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beads"
)

func TestRemoteHookWorkQueryListsOnlyOwnRoutedWork(t *testing.T) {
	listed := []beads.Bead{
		{ID: "own-route", Status: "open", Type: "task", Metadata: map[string]string{"gc.routed_to": "rig/worker"}},
		{ID: "other-route", Status: "open", Type: "task", Metadata: map[string]string{"gc.routed_to": "rig/reviewer"}},
		{ID: "own-message", Status: "open", Type: "message", Assignee: "rig/worker"},
		{ID: "foreign-held", Status: "open", Type: "task", Assignee: "rig/reviewer", Metadata: map[string]string{"gc.routed_to": "rig/worker"}},
		{ID: "own-workflow", Status: "open", Type: "workflow", Metadata: map[string]string{"gc.kind": "workflow", "gc.run_target": "rig/worker"}},
		{ID: "closed-route", Status: "closed", Type: "task", Metadata: map[string]string{"gc.routed_to": "rig/worker"}},
	}

	client := fakeRemoteHookBeadLister{listed: listed}
	var stdout, stderr bytes.Buffer
	if code := remoteHookWorkQuery(&client, []string{"rig/worker", "rig/worker"}, &stdout, &stderr); code != 0 {
		t.Fatalf("remote query exit = %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if client.status != "open" {
		t.Fatalf("remote list status = %q, want open", client.status)
	}
	var got []beads.Bead
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("remote query output is not a bead array: %v (%q)", err, stdout.String())
	}
	if len(got) != 2 || got[0].ID != "own-route" || got[1].ID != "own-workflow" {
		t.Fatalf("remote query rows = %+v, want own-route and own-workflow", got)
	}
}

type fakeRemoteHookBeadLister struct {
	listed []beads.Bead
	status string
}

func (f *fakeRemoteHookBeadLister) ListBeads(opts api.ListBeadsOpts) (api.CachedRead[[]beads.Bead], error) {
	f.status = opts.Status
	return api.CachedRead[[]beads.Bead]{Body: f.listed}, nil
}

func TestCmdHookRemoteClaimKeepsCapabilityRefusal(t *testing.T) {
	remoteCityUnderTestForHook(t, "http://127.0.0.1:1")
	t.Setenv("GC_AGENT", "rig/worker")
	t.Setenv("GC_ALIAS", "")

	var stdout, stderr bytes.Buffer
	code := cmdHookWithOptions(nil, hookCommandOptions{Claim: true, JSON: true}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("remote claim exit = 0, want capability refusal; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "does not support a remote city") {
		t.Fatalf("remote claim refusal changed: %q", stderr.String())
	}
}

func remoteCityUnderTestForHook(t *testing.T, url string) {
	t.Helper()
	origContext, origURL, origName := contextFlag, cityURLFlag, cityNameFlag
	t.Cleanup(func() { contextFlag, cityURLFlag, cityNameFlag = origContext, origURL, origName })
	contextFlag, cityURLFlag, cityNameFlag = "", url, "mc"
	t.Setenv("GC_HOME", t.TempDir())
}
