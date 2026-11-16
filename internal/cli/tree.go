package cli

import (
	"strings"

	"github.com/lczyk/chisel-coverage/pkg/pathtree"
)

// treeLines draws a block's entries as a tree of their paths, as --tree
// shows them. An entry's sign is glued to its name, as the flat report glues
// it to the path, and a directory drawn only to hold others has none.
func treeLines(es []rline, st style) []string {
	paths := make([]string, len(es))
	for i, e := range es {
		paths[i] = e.path
	}
	line := func(i int, name string) string {
		e := es[i]
		text := string(e.sign) + name
		if strings.HasSuffix(e.path, "/") {
			text += "/"
		}
		return st.paint(e.sign, text+e.tail()) + e.origin()
	}
	dim := func(s string) string {
		if st.dim == "" {
			return s
		}
		return st.dim + s + st.reset
	}
	return pathtree.Draw(pathtree.Of(paths, line), dim)
}
