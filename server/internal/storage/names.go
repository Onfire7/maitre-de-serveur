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
	'A': {"Apollo", "Atlas", "Aurora", "Aspen", "Amber", "Argon", "Alpine", "Anchor", "Aria", "Arrow", "Ash", "Avon", "Azure", "Acorn", "Adobe", "Aegis", "Albion", "Almond", "Amethyst", "Andes", "Antler", "Apex", "Arbor", "Astra", "Alder", "Acacia", "Azalea", "Anvil", "Avalon"},
	'B': {"Beacon", "Birch", "Basil", "Bolt", "Bramble", "Breeze", "Badger", "Banyan", "Baron", "Bay", "Bellow", "Berry", "Bison", "Blaze", "Bluebell", "Bonsai", "Borealis", "Brook", "Bronze", "Buckeye", "Burrow", "Butte", "Byway", "Bexley", "Bayou", "Boulder", "Briar", "Bastion", "Bluff"},
	'C': {"Cedar", "Comet", "Coral", "Cobalt", "Canyon", "Clover", "Cairn", "Calypso", "Camber", "Caper", "Caravel", "Cardinal", "Carmine", "Cashmere", "Cello", "Chalk", "Chestnut", "Citrine", "Cliff", "Compass", "Condor", "Copper", "Crag", "Crocus", "Cypress", "Cascade", "Cinder", "Cove", "Crescent"},
	'D': {"Delta", "Dune", "Dusk", "Dahlia", "Drift", "Domino", "Dagger", "Damson", "Dandelion", "Dart", "Dell", "Denim", "Derby", "Dew", "Dingle", "Dipper", "Dolphin", "Dorado", "Dragonfly", "Drake", "Druid", "Dulcet", "Durham", "Dynamo", "Dover", "Dapple", "Dory", "Dakota", "Dawn"},
	'E': {"Ember", "Echo", "Elm", "Eagle", "Everest", "Emerald", "Eclipse", "Edgewood", "Eider", "Elk", "Ellery", "Elysium", "Embark", "Emery", "Empire", "Encore", "Enigma", "Epoch", "Equinox", "Ermine", "Escher", "Esker", "Evergreen", "Exeter", "Ellis", "Ebony", "Eden", "Estuary", "Elder"},
	'F': {"Falcon", "Fern", "Fjord", "Flint", "Fable", "Frost", "Fairway", "Fawn", "Feather", "Fennec", "Fig", "Finch", "Fireside", "Flare", "Flax", "Fleet", "Flora", "Flume", "Folio", "Fossil", "Fox", "Freesia", "Fresco", "Fulcrum", "Fennel", "Foxglove", "Forge", "Fathom", "Fiddler"},
	'G': {"Granite", "Gale", "Glacier", "Garnet", "Grove", "Gust", "Galaxy", "Gannet", "Gardenia", "Gazelle", "Geyser", "Gilder", "Ginger", "Glade", "Glen", "Glint", "Gobi", "Gold", "Goshawk", "Gradient", "Grouse", "Gulch", "Gypsum", "Gyre", "Ginkgo", "Gull", "Gossamer", "Gorge", "Gable"},
	'H': {"Harbor", "Hazel", "Helix", "Heron", "Horizon", "Hollow", "Halcyon", "Halo", "Hamlet", "Harrier", "Hawthorn", "Heath", "Heather", "Hemlock", "Hermes", "Highland", "Hinterland", "Hoopoe", "Hornet", "Huckleberry", "Hudson", "Humber", "Hyacinth", "Hydra", "Hickory", "Haven", "Hearth", "Hummock", "Holt"},
	'I': {"Iris", "Indigo", "Ivory", "Island", "Icicle", "Ion", "Ibex", "Icarus", "Idyll", "Igloo", "Iguana", "Ilex", "Imber", "Impala", "Inca", "Incense", "Inkwell", "Inverness", "Iona", "Ipswich", "Iridium", "Iron", "Isthmus", "Ithaca", "Inlet", "Ibis", "Isle", "Ironwood", "Ivy"},
	'J': {"Jasper", "Juniper", "Jade", "Jet", "Jetty", "Jubilee", "Jackdaw", "Jaguar", "Jamboree", "Jasmine", "Javelin", "Jay", "Jenga", "Jersey", "Jewel", "Jiffy", "Jingle", "Jocund", "Jodhpur", "Jostle", "Journey", "Joust", "Jovial", "Jumbo", "Jonquil", "Juneau", "Jericho", "Jigsaw", "Jute"},
	'K': {"Kestrel", "Kite", "Kelp", "Keel", "Krypton", "Koa", "Kahuna", "Kale", "Kaleido", "Kangaroo", "Kapok", "Karst", "Kayak", "Kazoo", "Kelvin", "Kepler", "Kerosene", "Kettle", "Kilim", "Kingfisher", "Kiwi", "Klaxon", "Koala", "Kudu", "Kodiak", "Kinship", "Kerry", "Knoll", "Kona"},
	'L': {"Lark", "Lotus", "Lumen", "Lagoon", "Larch", "Lantern", "Labrador", "Lacewing", "Lakeshore", "Lapis", "Larkspur", "Latitude", "Lava", "Lavender", "Ledger", "Lemur", "Levee", "Lilac", "Lime", "Lioness", "Llama", "Loch", "Lodestar", "Lynx", "Laurel", "Loft", "Linden", "Lichen", "Lido"},
	'M': {"Maple", "Meteor", "Mesa", "Marble", "Mist", "Mango", "Macaw", "Magpie", "Mahogany", "Mandolin", "Mantis", "Maraca", "Marigold", "Mariner", "Marten", "Meadowlark", "Melody", "Mercury", "Merlin", "Mica", "Mimosa", "Monarch", "Moonstone", "Mosaic", "Marsh", "Mallow", "Moss", "Myrtle", "Mirth"},
	'N': {"Nova", "Nectar", "Nimbus", "Nutmeg", "Nickel", "North", "Nacre", "Narwhal", "Nebula", "Needle", "Neon", "Neptune", "Nest", "Newt", "Niche", "Nightjar", "Nile", "Nomad", "Nordic", "Notch", "Nougat", "Nuance", "Nymph", "Nyx", "Nook", "Nettle", "Navarre", "Nautilus", "Noble"},
	'O': {"Orbit", "Oak", "Onyx", "Opal", "Otter", "Oasis", "Oatmeal", "Obelisk", "Ocelot", "Octave", "Odyssey", "Ogham", "Okapi", "Olm", "Omega", "Onward", "Oracle", "Orca", "Oregano", "Oriole", "Orion", "Osier", "Ossian", "Oxbow", "Orchard", "Osprey", "Olive", "Ochre", "Outpost"},
	'P': {"Pebble", "Pine", "Prism", "Pluto", "Pepper", "Pixel", "Pagoda", "Palisade", "Panda", "Pansy", "Papyrus", "Parsley", "Pavilion", "Peony", "Peregrine", "Periwinkle", "Petrel", "Pewter", "Phoenix", "Pike", "Piper", "Plover", "Primrose", "Puffin", "Poplar", "Pampas", "Paddock", "Pinto", "Plume"},
	'Q': {"Quartz", "Quill", "Quest", "Quasar", "Quince", "Quiver", "Quadrant", "Quail", "Quaver", "Quebec", "Queensland", "Quenda", "Query", "Quartet", "Quiche", "Quicksilver", "Quiet", "Quilt", "Quinault", "Quinn", "Quintet", "Quip", "Quito", "Quorum", "Quay", "Quetzal", "Quokka", "Quarry", "Quinoa"},
	'R': {"Raven", "Reef", "Rune", "Ridge", "Ruby", "Rocket", "Raccoon", "Radiant", "Rainier", "Rampart", "Ranger", "Rapids", "Raspberry", "Redwood", "Regatta", "Relic", "Rhapsody", "Rhine", "Riddle", "Ripple", "Robin", "Rosemary", "Rudder", "Rye", "Rowan", "Rookery", "Rill", "Rosewood", "Rambler"},
	'S': {"Sage", "Summit", "Saturn", "Spruce", "Sparrow", "Slate", "Saddle", "Saga", "Sahara", "Salmon", "Sandpiper", "Sapphire", "Satin", "Savanna", "Scout", "Sedge", "Sequoia", "Sierra", "Silk", "Skylark", "Solstice", "Sonnet", "Starling", "Sterling", "Sorrel", "Sycamore", "Saffron", "Shoal", "Sable"},
	'T': {"Tundra", "Topaz", "Tide", "Thistle", "Timber", "Talon", "Tabor", "Taiga", "Tamarind", "Tangent", "Tapestry", "Tarn", "Tempest", "Tern", "Thimble", "Thrush", "Thunder", "Tiber", "Titan", "Toucan", "Trellis", "Trident", "Trillium", "Tulip", "Tamarack", "Teal", "Thicket", "Terrace", "Tansy"},
	'U': {"Umber", "Unity", "Upland", "Ursa", "Umbra", "Utah", "Ukulele", "Ulysses", "Umbrella", "Umpire", "Unicorn", "Union", "Universe", "Upbeat", "Upkeep", "Upriver", "Upstream", "Uptide", "Urchin", "Ural", "Uranus", "Urn", "Usher", "Utopia", "Upton", "Ulster", "Ulm", "Unwin", "Uxbridge"},
	'V': {"Vega", "Velvet", "Violet", "Vortex", "Valley", "Vapor", "Valiant", "Vanilla", "Vantage", "Vector", "Velodrome", "Venture", "Venus", "Verbena", "Verdigris", "Vermont", "Vernal", "Vertex", "Vesper", "Viburnum", "Viking", "Viola", "Vixen", "Voyager", "Verdant", "Vale", "Vireo", "Vermilion", "Vista"},
	'W': {"Willow", "Walnut", "Wren", "Wave", "Winter", "Wisp", "Wagtail", "Walrus", "Wander", "Warden", "Waterfall", "Wayfarer", "Weaver", "Wellspring", "Westwind", "Whale", "Wheat", "Whistler", "Wild", "Windward", "Wisteria", "Wolf", "Woodland", "Wyvern", "Wicker", "Wharf", "Wildwood", "Warbler", "Weir"},
	'X': {"Xenon", "Xylem", "Xenia", "Xavier", "Xanadu", "Xerus", "Xebec", "Xena", "Xeno", "Xenops", "Xerxes", "Xiphias", "Xingu", "Xiphos", "Xoanon", "Xochitl", "Xylia", "Xyris", "Xystus", "Xanthia", "Xanthus", "Xenith", "Xerophyte", "Xylophone", "Xylo", "Xanthe", "Xander", "Xiomara", "Xola"},
	'Y': {"Yarrow", "Yonder", "Yew", "Yukon", "Yacht", "Yeti", "Yak", "Yam", "Yangtze", "Yardarm", "Yawl", "Yearling", "Yellowhammer", "Yellowstone", "Yeoman", "Yerba", "Yield", "Yoho", "Yoke", "Yodel", "Yolk", "Yosemite", "Yucca", "Yurt", "Yule", "Yorke", "Yarn", "Yaffle", "Yarmouth"},
	'Z': {"Zephyr", "Zenith", "Zinc", "Zodiac", "Zircon", "Zinnia", "Zambezi", "Zander", "Zanzibar", "Zeal", "Zebra", "Zebu", "Zelkova", "Zen", "Zenobia", "Zeppelin", "Zabra", "Zeus", "Ziggurat", "Zigzag", "Zilch", "Zinger", "Zorilla", "Zither", "Zelda", "Zion", "Zorro", "Zuma", "Zest"},
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

// Names returns the shared name list for a letter: Nodes and Drives both
// draw from it (user, 2026-10-26), so a Fleet's Node A and its Drives all
// take names from the A list and no name is used twice.
func Names(letter string) []string {
	letter = strings.ToUpper(letter)
	if len(letter) != 1 {
		return nil
	}
	return append([]string(nil), driveNames[letter[0]]...)
}

// AllNames is every list by letter, for the Console (exported as JSON by
// `maitre names-index`).
func AllNames() map[string][]string {
	out := map[string][]string{}
	for i := 0; i < len(Letters); i++ {
		out[Letters[i:i+1]] = Names(Letters[i : i+1])
	}
	return out
}

// NextName returns the first name for letter that is not in used. The
// lists hold 29 names per letter; past that the list is walked again with
// a round number ("Apollo 2", "Atlas 2", ... "Apollo 3"), so a Node with
// any number of Drives keeps letter-led, pronounceable names. An empty
// letter yields "".
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
	for round := 2; ; round++ {
		for _, base := range driveNames[letter[0]] {
			n := fmt.Sprintf("%s %d", base, round)
			if !taken[strings.ToLower(n)] {
				return n
			}
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
	// Nodes draw from the same lists: their names are taken too.
	nodes, _ := store.ListLive(ctx, m.st, store.KindNode, func() *maitrev1.Node { return &maitrev1.Node{} })
	for _, n := range nodes {
		used = append(used, n.GetName())
	}
	return NextName(self.GetLetter(), used)
}
