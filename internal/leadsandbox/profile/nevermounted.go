package profile

// neverMounted is the caller's list plus the other runtime's config folder.
func (in Inputs) neverMounted() []string {
	never := append([]string(nil), in.NeverMounted...)
	if other := in.OtherConfigDir(); other != "" {
		never = append(never, other)
	}
	return never
}

// CheckNeverMounted refuses a bind source that is equal to, below or above any
// path of never. The comparison is lexical: both sides must be clean absolute
// paths with their symbolic links resolved by the caller.
func CheckNeverMounted(source string, never []string) error {
	if err := CheckPath("bind source", source); err != nil {
		return err
	}
	for _, n := range never {
		switch {
		case source == n:
			return refuse("bind source", "%s is a never-mounted path", source)
		case below(source, n):
			return refuse("bind source", "%s lies below the never-mounted path %s", source, n)
		case below(n, source):
			return refuse("bind source", "%s lies above the never-mounted path %s", source, n)
		}
	}
	return nil
}

// CheckSource is CheckNeverMounted with the list of the inputs: the caller's
// never-mounted paths and the other runtime's config folder.
func (in Inputs) CheckSource(source string) error {
	return CheckNeverMounted(source, in.neverMounted())
}
