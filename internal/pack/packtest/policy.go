package packtest

import _ "embed"

//go:embed testdata/alpha.golden
var alphaPolicy string

//go:embed testdata/beta.golden
var betaPolicy string

//go:embed testdata/tiny.golden
var tinyPolicy string

// AlphaPolicy returns the canonical policy for alpha's terminal and protocol launches.
func AlphaPolicy() []byte { return []byte(alphaPolicy) }

// BetaPolicy returns the canonical policy for beta's protocol launches.
func BetaPolicy() []byte { return []byte(betaPolicy) }

// TinyPolicy returns the canonical digest test vector for a second version of alpha.
func TinyPolicy() []byte { return []byte(tinyPolicy) }

// TinyPolicySHA256 is the SHA-256 of TinyPolicy, including its final newline.
const TinyPolicySHA256 = "b81e8a914ebf362aaab2d49676714172840f27538e6200f43875d6e94b905ad7"

const modeHelp = "-m,--mode=<v>   select a mode\n"

// AlphaHelp returns the help bytes referenced by AlphaPolicy and TinyPolicy.
func AlphaHelp() []byte { return []byte(modeHelp) }

// BetaHelp returns the help bytes referenced by BetaPolicy.
func BetaHelp() []byte { return []byte(modeHelp) }
