package cli

import (
	"sort"
	"strings"
)

// tnode is one segment of the tree of a block's entries. It may carry
// entries of its own, more than one when normalising the architecture
// brought two paths to one, and children both.
type tnode struct {
	name    string
	entries []rline
	kids    map[string]*tnode
}

// treeLines draws a block's entries as a tree of their paths, as --tree
// shows them. An entry's sign is glued to its name, as the flat report glues
// it to the path, and a directory drawn only to hold others has none. Every
// entry is a line of its own: a segment drawn for no entry folds with the one
// under it, and an entry stops the fold. Top-level names keep their leading
// slash and no box drawing.
func treeLines(es []rline, st style) []string {
	root := &tnode{kids: map[string]*tnode{}}
	for _, e := range es {
		n := root
		for _, seg := range strings.Split(strings.Trim(e.path, "/"), "/") {
			kid := n.kids[seg]
			if kid == nil {
				kid = &tnode{name: seg, kids: map[string]*tnode{}}
				n.kids[seg] = kid
			}
			n = kid
		}
		n.entries = append(n.entries, e)
	}
	for name, kid := range root.kids {
		root.kids[name] = foldChain(kid)
	}
	var out []string
	drawKids(root, "", true, st, &out)
	return out
}

// foldChain draws a run of segments with no entry and one child each as one.
func foldChain(n *tnode) *tnode {
	for name, kid := range n.kids {
		n.kids[name] = foldChain(kid)
	}
	for len(n.entries) == 0 && len(n.kids) == 1 {
		for _, only := range n.kids {
			only.name = n.name + "/" + only.name
			n = only
		}
	}
	return n
}

func drawKids(n *tnode, prefix string, top bool, st style, out *[]string) {
	// By the name drawn, which folding may have made longer than the key.
	kids := make([]*tnode, 0, len(n.kids))
	for _, kid := range n.kids {
		kids = append(kids, kid)
	}
	sort.Slice(kids, func(i, j int) bool { return kids[i].name < kids[j].name })
	for i, kid := range kids {
		drawNode(kid, prefix, i == len(kids)-1, top, st, out)
	}
}

func drawNode(n *tnode, prefix string, last, top bool, st style, out *[]string) {
	glyph, pad := "", ""
	if !top {
		glyph, pad = "\u251c\u2500 ", "\u2502  "
		if last {
			glyph, pad = "\u2514\u2500 ", "   "
		}
	}
	dim := func(s string) string {
		if s == "" || st.dim == "" {
			return s
		}
		return st.dim + s + st.reset
	}
	name := n.name
	if top {
		name = "/" + name
	}
	lead := dim(prefix) + dim(glyph)
	if len(n.entries) == 0 {
		*out = append(*out, lead+name+"/")
	}
	sort.SliceStable(n.entries, func(i, j int) bool { return n.entries[i].path < n.entries[j].path })
	for _, e := range n.entries {
		text := string(e.sign) + name
		if strings.HasSuffix(e.path, "/") {
			text += "/"
		}
		text += e.tail()
		*out = append(*out, lead+st.paint(e.sign, text)+e.origin())
	}
	drawKids(n, prefix+pad, false, st, out)
}
