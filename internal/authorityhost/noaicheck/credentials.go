package noaicheck

import (
	"errors"
	"io/fs"
	"path"
	"syscall"
)

// CheckCredentials is check 3: a credential or config path of ai-names.list
// (".claude", ".config/opencode") present in any home folder or in /root,
// as a folder, a file or a link (even a dangling one), is a hit of kind
// "credential". The root folder is looked into too, for the users whose home
// is "/".
func (c *Checker) CheckCredentials() {
	for _, home := range append([]string{"."}, c.homeFolders()...) {
		for _, p := range c.lists.AIHomePaths {
			rel := path.Join(home, p)
			_, _, err := c.t.lstat(rel)
			switch {
			case err == nil:
				c.hit(KindCredential, show(rel), "")
			case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			default:
				c.unreadable(rel, err)
			}
		}
	}
}
