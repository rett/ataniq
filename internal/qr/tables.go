package qr

// Level is an error correction level. Only L and M exist; the zero Level is
// M.
type Level int

const (
	// M recovers about 15 percent of the codewords. It is the default.
	M Level = iota
	// L recovers about 7 percent of the codewords and holds more.
	L
)

// DefaultLevel is the level to use when the caller has no reason to choose.
const DefaultLevel = M

func (l Level) String() string {
	switch l {
	case L:
		return "L"
	case M:
		return "M"
	}
	return "Level(?)"
}

// formatBits is the two-bit level indicator of the format information.
func (l Level) formatBits() int {
	if l == L {
		return 1
	}
	return 0
}

// MinVersion and MaxVersion bound the versions Encode uses.
const (
	MinVersion = 1
	MaxVersion = 10
)

// blockSpec is the block structure of one version at one level: blocks1
// blocks of data1 data codewords, then blocks2 blocks of data1+1, every block
// followed by ecPerBlock error correction codewords.
type blockSpec struct {
	ecPerBlock int
	blocks1    int
	data1      int
	blocks2    int
}

func (s blockSpec) dataCodewords() int {
	return s.blocks1*s.data1 + s.blocks2*(s.data1+1)
}

func (s blockSpec) blocks() int { return s.blocks1 + s.blocks2 }

// specs is indexed by level and then version (index 0 unused).
var specs = [2][MaxVersion + 1]blockSpec{
	M: {
		{},
		{10, 1, 16, 0},
		{16, 1, 28, 0},
		{26, 1, 44, 0},
		{18, 2, 32, 0},
		{24, 2, 43, 0},
		{16, 4, 27, 0},
		{18, 4, 31, 0},
		{22, 2, 38, 2},
		{22, 3, 36, 2},
		{26, 4, 43, 1},
	},
	L: {
		{},
		{7, 1, 19, 0},
		{10, 1, 34, 0},
		{15, 1, 55, 0},
		{20, 1, 80, 0},
		{26, 1, 108, 0},
		{18, 2, 68, 0},
		{20, 2, 78, 0},
		{24, 2, 97, 0},
		{30, 2, 116, 0},
		{18, 2, 68, 2},
	},
}

// alignmentCenters holds the alignment pattern center coordinates of each
// version (index 0 unused).
var alignmentCenters = [MaxVersion + 1][]int{
	nil,
	nil,
	{6, 18},
	{6, 22},
	{6, 26},
	{6, 30},
	{6, 34},
	{6, 22, 38},
	{6, 24, 42},
	{6, 26, 46},
	{6, 28, 50},
}

// sizeOf is the number of modules on a side of a version.
func sizeOf(version int) int { return 17 + 4*version }

// countBits is the width of the character count indicator in byte mode.
func countBits(version int) int {
	if version <= 9 {
		return 8
	}
	return 16
}

// byteCapacity is the number of bytes a version holds at a level in byte
// mode.
func byteCapacity(version int, l Level) int {
	bits := specs[l][version].dataCodewords()*8 - 4 - countBits(version)
	return bits / 8
}
