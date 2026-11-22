package seatpair

// wordList is the 256 words of the fingerprint: a byte of the digest is the
// index of its word. It is fixed: the desk console and the phone must show the
// same words for one key, so a change of any word, or of the order, changes
// every fingerprint, and a test pins the list's digest.
//
// The list is plain, everyday nouns in lower-case ASCII, 3 to 6 letters each,
// sorted. No word is a prefix of another, so the words can be read run
// together, and no two begin with the same four letters (or are the same
// when shorter than four), so the first four letters of a word name it. No two
// words are one letter apart, or one swap of two neighbouring letters, so a
// word misread or mistyped by one letter is no other word. A test holds
// these properties and that no word holds a fragment of its list of offensive
// words. Words with a common homophone were left out when the list was made;
// no test can hold that.
var wordList = [256]string{
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
