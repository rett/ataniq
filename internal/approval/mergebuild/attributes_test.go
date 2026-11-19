package mergebuild

import (
	"errors"
	"testing"

	"ataniq/internal/approval/record"
)

// attributeLines are the attribute lines a lead could commit to bend a
// merge: the first two make every file binary, which git then refuses to
// merge by lines; "merge=union" joins both sides where a conflict should be
// reported; "merge=binary" reports a conflict for every file both changed.
var attributeLines = []string{"* -diff\n", "* binary\n", "* merge=union\n", "* merge=binary\n"}

// A ".gitattributes" in the trees changes nothing: with each line, once
// already in the target (in every tree) and once newly added by the
// feature, the clean merge gives the tree of the same change without the
// file plus the file itself, and the conflicting merge is still a conflict.
//
// What makes it so is gitenv's attribute pin, which a caller cannot take
// off; gitenv's own tests show, with a git that is not pinned, that these
// lines do change a merge once an attribute source is set.
func TestAttributesInTreesDoNotChangeTheMerge(t *testing.T) {
	for _, format := range formats {
		// The control: the same changes with no attribute file anywhere.
		control := newScene(t, newLabFormat(t, format), nil, nil)
		controlMerge, err := Build(ctx, control.repo, inputs(control.target, control.feature), "k")
		if err != nil {
			t.Fatal(err)
		}
		if want := control.tree("f.txt", mergedText); controlMerge.Tree != want {
			t.Fatalf("the control's tree is %s, want %s", controlMerge.Tree, want)
		}
		if _, err := Build(ctx, control.repo, inputs(control.clashTarget, control.clashFeature), "c"); !isConflict(err) {
			t.Fatalf("the control's conflict returned %v", err)
		}

		for _, line := range attributeLines {
			file := side{".gitattributes", line}
			for where, s := range map[string]*scene{
				"already in the target":      newScene(t, newLabFormat(t, format), file, nil),
				"newly added by the feature": newScene(t, newLabFormat(t, format), nil, file),
			} {
				t.Run(format+" "+line[:len(line)-1]+" "+where, func(t *testing.T) {
					m, err := Build(ctx, s.repo, inputs(s.target, s.feature), "k")
					if err != nil {
						t.Fatalf("the clean merge: %v", err)
					}
					// The control's tree and the attribute file, nothing
					// else: "f.txt" is the very blob of the control.
					if want := s.tree("f.txt", mergedText, ".gitattributes", line); m.Tree != want {
						t.Errorf("the tree is %s, want the control's with the file, %s", m.Tree, want)
					}
					if got, want := s.git("", "ls-tree", "--end-of-options", m.Tree, "f.txt"),
						control.git("", "ls-tree", "--end-of-options", controlMerge.Tree, "f.txt"); got != want {
						t.Errorf("f.txt is %q, the control's %q", got, want)
					}
					if err := Check(ctx, s.repo, m.Commit, inputs(s.target, s.feature)); err != nil {
						t.Errorf("Check: %v", err)
					}

					commits, refs := s.count("commit"), s.refs()
					if _, err := Build(ctx, s.repo, inputs(s.clashTarget, s.clashFeature), "c"); !isConflict(err) {
						t.Errorf("the conflicting merge returned %v, want merge-conflict as without the file", err)
					}
					if s.count("commit") != commits || s.refs() != refs {
						t.Error("the refused merge wrote a commit or a ref")
					}
				})
			}
		}
	}
}

// isConflict reports whether err is the refusal merge-conflict.
func isConflict(err error) bool {
	var refusal *record.Error
	return errors.As(err, &refusal) && refusal.Code == record.CodeMergeConflict
}
