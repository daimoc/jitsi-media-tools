package session

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

func TestBudget_Ladder(t *testing.T) {
	// One pin: unpinned height, then unpinned count, then pinned height.
	got := ladder(Selection{Limit: 4, Height: 360, PinHeight: 720}, 1)
	want := []level{{3, 360, 720}, {3, 180, 720}, {2, 180, 720}, {1, 180, 720}, {0, 180, 720}, {0, 180, 360}, {0, 180, 180}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("one pin:\n got %v\nwant %v", got, want)
	}
	// No pin: down to one video.
	got = ladder(Selection{Limit: 3, Height: 180, PinHeight: 720}, 0)
	want = []level{{3, 180, 720}, {2, 180, 720}, {1, 180, 720}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("no pin:\n got %v\nwant %v", got, want)
	}
}

// The controller's moves, worked through with the default rates: a pin at
// 720p (2.5 Mbps) and 8 others at 180p (200 kbps each) in a 3 Mbps budget.
func TestBudget_Steps(t *testing.T) {
	sel := Selection{Limit: 9, Height: 180, PinHeight: 720, Budget: 3_000_000}
	lad := ladder(sel, 1) // {8 180 720} … {0 180 720} {0 180 360} {0 180 180}
	room := counts{pins: 1, others: 8}
	c := newBudgetCtl(sel.Budget)
	at := time.Unix(1000, 0)
	c.fit(at.Add(-time.Minute), lad, room)
	if l := lad[c.level]; l.others != 2 || l.pinHeight != 720 {
		t.Fatalf("start at %v, want 2 others with the pin at 720 (2.9 Mbps estimated)", l)
	}
	// 3.5 Mbps arrives for an estimated 2.9: one window over is not enough.
	arriving := counts{pins: 1, others: 2}
	if c.step(at, lad, room, arriving, 3_500_000) {
		t.Fatal("moved after one window over")
	}
	at = at.Add(2 * time.Second)
	if !c.step(at, lad, room, arriving, 3_500_000) {
		t.Fatal("did not move after two windows over")
	}
	// Scaled by 3.5/2.9, the pin alone at 720 (3.02) does not fit; at 360 it does.
	if l := lad[c.level]; l.others != 0 || l.pinHeight != 360 {
		t.Fatalf("after over budget: %v, want the pin alone at 360", l)
	}
	// Under budget but within 10 s of moving down: stays.
	arriving = counts{pins: 1}
	at = at.Add(4 * time.Second)
	if c.step(at, lad, room, arriving, 500_000) {
		t.Fatal("moved up within 10 s of moving down")
	}
	// 0.8 Mbps for an estimated 0.7: 720 would be 2.86, above 85% of 3 Mbps.
	at = at.Add(10 * time.Second)
	if c.step(at, lad, room, arriving, 800_000) {
		t.Fatalf("moved up to %v without room", lad[c.level])
	}
	// 0.5 for 0.7: the pin at 720 and 5 others are 2.47 Mbps, within 2.55.
	at = at.Add(2 * time.Second)
	if !c.step(at, lad, room, arriving, 500_000) {
		t.Fatal("did not move up with room")
	}
	if l := lad[c.level]; l.others != 5 || l.pinHeight != 720 {
		t.Fatalf("moved up to %v, want 5 others with the pin at 720", l)
	}
}

// With a budget, the session asks the bridge for the reduced selection and
// offers it the budget (FR-033).
func TestBudget_PolicySent(t *testing.T) {
	var occ []signaling.Occupant
	for _, n := range []string{"Alice", "Bob", "Carol", "Dan", "Erin"} {
		occ = append(occ, signaling.Occupant{ID: n + "1", DisplayName: n})
	}
	f := &fakeSignal{Occupants: occ}
	useFake(t, f)
	var col collected
	s, err := Start(context.Background(), col.config(Config{MeetingURL: "https://meet.test/Room", DisplayName: "Viewer", Receive: true,
		Selection: Selection{Budget: 500_000}}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// 5 others at 200 kbps: 2 fit in 500 kbps.
	p := f.LastPolicy()
	if p == nil || p.LastN != 2 || p.AssumedBandwidth != 500_000 {
		t.Fatalf("policy %+v, want lastN 2 and the budget offered", p)
	}
	if err := s.SetSelection(Selection{Budget: 50_000}); err == nil {
		t.Error("a 50 kbps budget was accepted")
	}
}
