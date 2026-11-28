package seatlink

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// docRooms reads the rooms table of the design (docs/design/seat-gate.md,
// under its login section: "| Kind | Calls | Room in 80 s |"), each row's
// first cell and the leading number of its last.
func docRooms(doc string) (map[string]int, map[string]string, error) {
	const head = "| Kind | Calls | Room in 80 s |"
	if strings.Count(doc, head) != 1 {
		return nil, nil, fmt.Errorf("the design holds %d rooms tables", strings.Count(doc, head))
	}
	rest := doc[strings.Index(doc, head)+len(head):]
	rooms, notes := map[string]int{}, map[string]string{}
	cell := regexp.MustCompile(`^(\d+)(?: \((.*)\))?$`)
	for i, line := range strings.Split(rest, "\n")[1:] {
		if i == 0 {
			if !strings.HasPrefix(line, "| --- |") {
				return nil, nil, fmt.Errorf("no separator under the head: %q", line)
			}
			continue
		}
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(strings.Trim(line, "|"), " | ")
		if len(cells) != 3 {
			return nil, nil, fmt.Errorf("a row of %d cells: %q", len(cells), line)
		}
		name := strings.TrimSpace(cells[0])
		m := cell.FindStringSubmatch(strings.TrimSpace(cells[2]))
		if m == nil {
			return nil, nil, fmt.Errorf("row %s: room %q", name, cells[2])
		}
		if _, dup := rooms[name]; dup {
			return nil, nil, fmt.Errorf("row %s twice", name)
		}
		rooms[name], _ = strconv.Atoi(m[1])
		notes[name] = m[2]
	}
	return rooms, notes, nil
}

// TestSeatLinkDesignRoomsTable: the design's rooms table says the rooms the
// link keeps, row by row (kind.room() and pingRoom), with no row the link
// does not have, so the documents cannot drift from budget.go again. The
// pairs of the decisions and of anyone's room are half the room, and the
// sum the prose under the table gives is the rooms' sum.
func TestSeatLinkDesignRoomsTable(t *testing.T) {
	b, err := os.ReadFile("../../docs/design/seat-gate.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	rooms, notes, err := docRooms(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"Pings":     pingRoom,
		"Anyone's":  kindPublic.room(),
		"Reads":     kindRead.room(),
		"Chat":      kindChat.room(),
		"Decisions": kindDecide.room(),
		"Waits":     kindWait.room(),
		"Genesis":   kindGenesis.room(),
	}
	if len(want) != int(numKinds)+1 {
		t.Fatalf("the test names %d rows, the link has %d kinds and the pings", len(want), numKinds)
	}
	for name, room := range want {
		got, ok := rooms[name]
		if !ok {
			t.Errorf("the design's table has no row %s", name)
			continue
		}
		if got != room {
			t.Errorf("the design's table gives %s %d, the link keeps %d", name, got, room)
		}
	}
	for name := range rooms {
		if _, ok := want[name]; !ok {
			t.Errorf("the design's table has a row %s, which is no room of the link", name)
		}
	}
	if got, want := notes["Anyone's"], fmt.Sprintf("%d logins", publicRoom/2); got != want {
		t.Errorf("anyone's room says %q, want %q", got, want)
	}
	if got, want := notes["Decisions"], fmt.Sprintf("%d decisions", decideRoom/2); got != want {
		t.Errorf("the decisions' room says %q, want %q", got, want)
	}
	sum := fmt.Sprintf("The rooms add up to the gate's %d (%d + %d + %d + %d + %d + %d + %d,", GateRate,
		pingRoom, decideRoom, publicRoom, readRoom, chatRoom, waitRoom, genesisRoom)
	if !strings.Contains(doc, sum) {
		t.Errorf("the design lacks %q", sum)
	}
}

// TestSeatLinkGuideRooms: the seat's guide (docs/seat-gate.md) says the
// same rooms in its sentence of what live refresh costs.
func TestSeatLinkGuideRooms(t *testing.T) {
	b, err := os.ReadFile("../../docs/seat-gate.md")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("pings\n%d, decisions %d, pairing and login (anyone's) %d, reads %d, the Leads pages %d,\nwaits %d and the new-project pages %d, %d in all.",
		pingRoom, decideRoom, publicRoom, readRoom, chatRoom, waitRoom, genesisRoom, GateRate)
	if !strings.Contains(string(b), want) {
		t.Errorf("the guide lacks %q", want)
	}
}

// TestSeatLinkDocRoomsParser: the reader of the table refuses a table it
// cannot read whole, so the test above cannot pass on a table it misread.
func TestSeatLinkDocRoomsParser(t *testing.T) {
	const head = "| Kind | Calls | Room in 80 s |\n| --- | --- | --- |\n"
	for _, bad := range []string{
		"no table",
		head + head,
		"| Kind | Calls | Room in 80 s |\n| Pings | x | 6 |\n",
		head + "| Pings | x | six |\n",
		head + "| Pings | x | 6 | 7 |\n",
		head + "| Pings | x | 6 |\n| Pings | x | 6 |\n",
	} {
		if _, _, err := docRooms(bad); err == nil {
			t.Errorf("read %q", bad)
		}
	}
	rooms, notes, err := docRooms(head + "| Pings | `seat.ping` | 6 |\n| Decisions | a, b | 10 (5 decisions) |\n\nprose | 9 |\n")
	if err != nil || len(rooms) != 2 || rooms["Pings"] != 6 || rooms["Decisions"] != 10 || notes["Decisions"] != "5 decisions" {
		t.Fatalf("%v %v %v", rooms, notes, err)
	}
}
