// The words of a new passkey, worked out on the phone from the credential
// it made. The formula is that of the seatpair package of the daemon:
//
//   SHA-256( BE32(len(credentialId)) || credentialId || BE32(len(spki)) || spki )
//
// BE32 is a 4-byte big-endian length. Each of the first COUNT (eight) bytes of the digest
// is an index into the 256 words of LIST, which is the list of that package,
// word for word; a test in tests/ holds the two equal and runs this function
// against the package's own on a vector and on random inputs.
"use strict";
window.SeatWords = (function () {
  var LIST = [
    "acorn", "almond", "anchor", "anvil", "apple", "arrow", "aspen", "atlas",
    "aurora", "badge", "baker", "bamboo", "banjo", "banner", "barley", "barn",
    "basil", "basket", "beach", "beaver", "beetle", "bell", "berry", "birch",
    "bison", "blade", "boat", "bottle", "breeze", "brick", "bridge", "bronze",
    "brook", "bubble", "bucket", "cabin", "cactus", "camel", "candle", "canoe",
    "canyon", "carpet", "cashew", "castle", "cavern", "cedar", "celery", "cello",
    "chair", "chalk", "cherry", "chisel", "citrus", "cliff", "clock", "cloud",
    "clover", "coast", "cobalt", "cocoa", "comet", "condor", "copper", "coral",
    "cotton", "cougar", "crane", "crayon", "creek", "daisy", "delta", "desert",
    "dragon", "drift", "dune", "dusk", "eagle", "easel", "elbow", "ember",
    "fabric", "falcon", "fern", "fiddle", "field", "finch", "flame", "flask",
    "fleet", "flint", "flute", "forest", "fossil", "frost", "fudge", "galaxy",
    "garden", "garlic", "gecko", "geyser", "globe", "goblet", "gopher", "gravel",
    "guitar", "harbor", "harp", "hawk", "hazel", "hedge", "heron", "hippo",
    "honey", "horse", "iguana", "island", "ivory", "jacket", "jaguar", "jester",
    "jigsaw", "jungle", "kayak", "kettle", "kite", "kiwi", "koala", "ladder",
    "lagoon", "lake", "lark", "latte", "leaf", "lemon", "lentil", "lily",
    "lizard", "llama", "lotus", "magnet", "magpie", "mallet", "mango", "maple",
    "marble", "marsh", "mason", "meadow", "mint", "mirror", "mocha", "monkey",
    "moose", "mosaic", "nectar", "needle", "nickel", "nugget", "nutmeg", "oasis",
    "ocean", "olive", "onion", "onyx", "opal", "orange", "orbit", "orchid",
    "otter", "oyster", "paddle", "pagoda", "palm", "panda", "pebble", "pencil",
    "pepper", "petal", "piano", "pickle", "pigeon", "pine", "pixel", "planet",
    "plaza", "plum", "pond", "poppy", "prism", "pulley", "puma", "puzzle",
    "quail", "quartz", "quiver", "rabbit", "radish", "ranch", "raven", "reef",
    "rhino", "ribbon", "ripple", "river", "robin", "rocket", "ruby", "rudder",
    "sailor", "salmon", "sand", "scarf", "sesame", "shadow", "shark", "sheep",
    "shovel", "silver", "skate", "snail", "sponge", "sprout", "spruce", "squid",
    "stamp", "summit", "sunset", "swan", "tackle", "tiger", "timber", "tomato",
    "torch", "tower", "trail", "trout", "tulip", "tundra", "tunnel", "turtle",
    "valley", "vault", "velvet", "violet", "wagon", "walnut", "walrus", "whale",
    "wheat", "whisk", "window", "winter", "wolf", "yogurt", "zebra", "zipper"
  ];

  // COUNT is the number of words of a fingerprint. It is the seatpair package's
  // FingerprintWords; a test in tests/ holds the two equal.
  var COUNT = 8;

  function bytesOf(x) { return new Uint8Array(x); }

  // fingerprint resolves to the array of COUNT words for a credential id and a
  // public key (DER SPKI), each an ArrayBuffer or a typed array.
  async function fingerprint(credentialId, spki) {
    var id = bytesOf(credentialId), key = bytesOf(spki);
    var buf = new Uint8Array(4 + id.length + 4 + key.length);
    var view = new DataView(buf.buffer);
    view.setUint32(0, id.length, false);
    buf.set(id, 4);
    view.setUint32(4 + id.length, key.length, false);
    buf.set(key, 8 + id.length);
    var sum = new Uint8Array(await crypto.subtle.digest("SHA-256", buf));
    return Array.prototype.slice.call(sum.slice(0, COUNT)).map(function (b) { return LIST[b]; });
  }

  return { list: LIST, count: COUNT, fingerprint: fingerprint };
})();
