package card

import (
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"strings"

	"ataniq/internal/approval/record"
)

// BindingTag returns four words for comparing a binding SHA-256 across
// displays. The input must be the record's 64 lower-case hex digits. An
// absent or malformed digest has no tag and returns the empty string.
//
// The four bytes of the IEEE CRC-32 of the decoded digest, most significant
// first, each select one word. This includes every digest bit and detects
// every single-bit change. Decisions continue to use the full binding digest.
func BindingTag(bindingSHA256 string) string {
	if !record.IsLowerHex(bindingSHA256, 64) {
		return ""
	}
	digest, _ := hex.DecodeString(bindingSHA256)
	var indices [4]byte
	binary.BigEndian.PutUint32(indices[:], crc32.ChecksumIEEE(digest))
	var words [4]string
	for i, index := range indices {
		words[i] = bindingWords[index]
	}
	return strings.Join(words[:], " ")
}

// The byte-to-word mapping is fixed for binding tags. It uses the project's
// pairing vocabulary, kept here so the card model needs no pairing service.
var bindingWords = [256]string{
	"acorn", "almond", "anchor", "anvil", "apple", "arrow", "aspen", "atlas",
	"aurora", "badge", "baker", "bamboo", "banjo", "banner", "barley", "barn",
	"basil", "basket", "beach", "beaver", "beetle", "bell", "berry", "birch",
	"bison", "blade", "boat", "bottle", "breeze", "brick", "bridge", "bronze",
	"brook", "bubble", "bucket", "cabin", "cactus", "camel", "candle",
	"canoe", "canyon", "carpet", "cashew", "castle", "cavern", "cedar",
	"celery", "cello", "chair", "chalk", "cherry", "chisel", "citrus",
	"cliff", "clock", "cloud", "clover", "coast", "cobalt", "cocoa", "comet",
	"condor", "copper", "coral", "cotton", "cougar", "crane", "crayon",
	"creek", "daisy", "delta", "desert", "dragon", "drift", "dune", "dusk",
	"eagle", "easel", "elbow", "ember", "fabric", "falcon", "fern", "fiddle",
	"field", "finch", "flame", "flask", "fleet", "flint", "flute", "forest",
	"fossil", "frost", "fudge", "galaxy", "garden", "garlic", "gecko",
	"geyser", "globe", "goblet", "gopher", "gravel", "guitar", "harbor",
	"harp", "hawk", "hazel", "hedge", "heron", "hippo", "honey", "horse",
	"iguana", "island", "ivory", "jacket", "jaguar", "jester", "jigsaw",
	"jungle", "kayak", "kettle", "kite", "kiwi", "koala", "ladder", "lagoon",
	"lake", "lark", "latte", "leaf", "lemon", "lentil", "lily", "lizard",
	"llama", "lotus", "magnet", "magpie", "mallet", "mango", "maple",
	"marble", "marsh", "mason", "meadow", "mint", "mirror", "mocha", "monkey",
	"moose", "mosaic", "nectar", "needle", "nickel", "nugget", "nutmeg",
	"oasis", "ocean", "olive", "onion", "onyx", "opal", "orange", "orbit",
	"orchid", "otter", "oyster", "paddle", "pagoda", "palm", "panda",
	"pebble", "pencil", "pepper", "petal", "piano", "pickle", "pigeon",
	"pine", "pixel", "planet", "plaza", "plum", "pond", "poppy", "prism",
	"pulley", "puma", "puzzle", "quail", "quartz", "quiver", "rabbit",
	"radish", "ranch", "raven", "reef", "rhino", "ribbon", "ripple", "river",
	"robin", "rocket", "ruby", "rudder", "sailor", "salmon", "sand", "scarf",
	"sesame", "shadow", "shark", "sheep", "shovel", "silver", "skate",
	"snail", "sponge", "sprout", "spruce", "squid", "stamp", "summit",
	"sunset", "swan", "tackle", "tiger", "timber", "tomato", "torch", "tower",
	"trail", "trout", "tulip", "tundra", "tunnel", "turtle", "valley",
	"vault", "velvet", "violet", "wagon", "walnut", "walrus", "whale",
	"wheat", "whisk", "window", "winter", "wolf", "yogurt", "zebra", "zipper",
}
