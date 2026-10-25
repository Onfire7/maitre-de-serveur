package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Decision 104: every Node holds one letter and its Drives take names that
// start with it, so a glance at a name says which Node a Drive sits in.

// driveNames is the name list, six per letter, friendly and easy to say out
// loud. When a letter runs out the names continue as <Letter>-drive-N.
var driveNames = map[byte][]string{
	'A': {"Apollo", "Atlas", "Aurora", "Aspen", "Amber", "Argon"},
	'B': {"Beacon", "Birch", "Basil", "Bolt", "Bramble", "Breeze"},
	'C': {"Cedar", "Comet", "Coral", "Cobalt", "Canyon", "Clover"},
	'D': {"Delta", "Dune", "Dusk", "Dahlia", "Drift", "Domino"},
	'E': {"Ember", "Echo", "Elm", "Eagle", "Everest", "Emerald"},
	'F': {"Falcon", "Fern", "Fjord", "Flint", "Fable", "Frost"},
	'G': {"Granite", "Gale", "Glacier", "Garnet", "Grove", "Gust"},
	'H': {"Harbor", "Hazel", "Helix", "Heron", "Horizon", "Hollow"},
	'I': {"Iris", "Indigo", "Ivory", "Island", "Icicle", "Ion"},
	'J': {"Jasper", "Juniper", "Jade", "Jet", "Jetty", "Jubilee"},
	'K': {"Kestrel", "Kite", "Kelp", "Keel", "Krypton", "Koa"},
	'L': {"Lark", "Lotus", "Lumen", "Lagoon", "Larch", "Lantern"},
	'M': {"Maple", "Meteor", "Mesa", "Marble", "Mist", "Mango"},
	'N': {"Nova", "Nectar", "Nimbus", "Nutmeg", "Nickel", "North"},
	'O': {"Orbit", "Oak", "Onyx", "Opal", "Otter", "Oasis"},
	'P': {"Pebble", "Pine", "Prism", "Pluto", "Pepper", "Pixel"},
	'Q': {"Quartz", "Quill", "Quest", "Quasar", "Quince", "Quiver"},
	'R': {"Raven", "Reef", "Rune", "Ridge", "Ruby", "Rocket"},
	'S': {"Sage", "Summit", "Saturn", "Spruce", "Sparrow", "Slate"},
	'T': {"Tundra", "Topaz", "Tide", "Thistle", "Timber", "Talon"},
	'U': {"Umber", "Unity", "Upland", "Ursa", "Umbra", "Utah"},
	'V': {"Vega", "Velvet", "Violet", "Vortex", "Valley", "Vapor"},
	'W': {"Willow", "Walnut", "Wren", "Wave", "Winter", "Wisp"},
	'X': {"Xenon", "Xylem", "Xenia", "Xavier", "Xanadu", "Xerus"},
	'Y': {"Yarrow", "Yonder", "Yew", "Yukon", "Yacht", "Yeti"},
	'Z': {"Zephyr", "Zenith", "Zinc", "Zodiac", "Zircon", "Zinnia"},
}

// Letters is the alphabet Nodes draw from, in order.
const Letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// PickLetter returns the first letter of Letters not in used, or "" when
// every letter is taken.
func PickLetter(used []string) string {
	taken := map[string]bool{}
	for _, u := range used {
		taken[strings.ToUpper(u)] = true
	}
	for i := 0; i < len(Letters); i++ {
		l := Letters[i : i+1]
		if !taken[l] {
			return l
		}
	}
	return ""
}

// NextName returns the first name for letter that is not in used; after
// the list runs out, <Letter>-drive-N. An empty letter yields "".
func NextName(letter string, used []string) string {
	letter = strings.ToUpper(letter)
	if len(letter) != 1 {
		return ""
	}
	taken := map[string]bool{}
	for _, u := range used {
		taken[strings.ToLower(u)] = true
	}
	for _, n := range driveNames[letter[0]] {
		if !taken[strings.ToLower(n)] {
			return n
		}
	}
	for i := 1; ; i++ {
		n := fmt.Sprintf("%s-drive-%d", letter, i)
		if !taken[strings.ToLower(n)] {
			return n
		}
	}
}

// AssignLetter gives this Node its letter if it has none, or a new one when
// an older Node holds the same letter (two Nodes provisioned at once). It
// returns the letter and whether the record was written.
func AssignLetter(ctx context.Context, st *store.Store) (string, bool, error) {
	nodes, err := store.ListLive(ctx, st, store.KindNode, func() *maitrev1.Node { return &maitrev1.Node{} })
	if err != nil {
		return "", false, err
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].GetProvisionedUnixMs() < nodes[j].GetProvisionedUnixMs() })
	var self *maitrev1.Node
	var used []string
	for _, n := range nodes {
		if n.GetNodeId() == st.NodeID() {
			self = n
			continue
		}
		if n.GetLetter() == "" {
			continue
		}
		// A conflicting letter counts as used only when the other Node is
		// older; the younger one yields.
		if self == nil || !strings.EqualFold(n.GetLetter(), self.GetLetter()) || n.GetProvisionedUnixMs() < self.GetProvisionedUnixMs() {
			used = append(used, n.GetLetter())
		}
	}
	if self == nil {
		return "", false, fmt.Errorf("own node record missing")
	}
	for _, u := range used {
		if strings.EqualFold(u, self.GetLetter()) {
			self.Letter = ""
		}
	}
	if self.GetLetter() != "" {
		return self.GetLetter(), false, nil
	}
	self.Letter = PickLetter(used)
	if self.Letter == "" {
		return "", false, nil
	}
	if _, err := st.Put(ctx, store.KindNode, st.NodeID(), self, false); err != nil {
		return "", false, err
	}
	return self.Letter, true, nil
}

// SuggestName is the name the next Drive of this Node would get.
func (m *Manager) SuggestName(ctx context.Context) string {
	var self maitrev1.Node
	if _, err := m.st.Get(ctx, store.KindNode, m.st.NodeID(), &self); err != nil || self.GetLetter() == "" {
		return ""
	}
	records, _ := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	used := make([]string, 0, len(records))
	for _, r := range records {
		used = append(used, r.GetName())
	}
	return NextName(self.GetLetter(), used)
}
