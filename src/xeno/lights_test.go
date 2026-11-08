package main

import (
	"reflect"
	"testing"
)

// A thread's heat map row becomes its pane's lights: the positive control on
// the verifier, the negative control on the falsifier, its best audited fit
// on jev; a control in the uncertain band stays hollow.
func TestControlLights(t *testing.T) {
	scores := []reasonScore{{"parse", .91, .92}, {"other", .4, .1}, {"pipeline", .80, .07}, {"kubernetes", .01, .01}}
	got := controlLights(scores, []string{"parse", "other"}, "pipeline", "kubernetes")
	want := [][]string{{"verifier", "confirmed"}, {"falsifier", "failed"}, {"jev", "0.91"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lights %v", got)
	}
	scores = []reasonScore{{"parse", .5, .5}, {"pipeline", .5, 0}, {"kubernetes", .9, 0}}
	got = controlLights(scores, []string{"parse"}, "pipeline", "kubernetes")
	want = [][]string{{"falsifier", "refuted"}, {"jev", "0.50"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lights %v", got)
	}
	if got := controlLights(scores, []string{"parse"}, "", ""); len(got) != 0 {
		t.Fatalf("lights without controls %v", got)
	}
}
