package engine

import "github.com/vansh7877/vansh-repo/internal/model"

// dayGraph is the directed account graph of one CSP on one day.
type dayGraph struct {
	adj   map[string]map[string][]*Record
	dirty bool
	res   loopStats
}

// loopStats is the order-independent round-trip summary of a graph.
type loopStats struct {
	txns   int
	amount model.Money
	loops  int
	recs   []*Record
}

func newDayGraph() *dayGraph { return &dayGraph{adj: map[string]map[string][]*Record{}} }

func (g *dayGraph) add(r *Record) {
	m := g.adj[r.From]
	if m == nil {
		m = map[string][]*Record{}
		g.adj[r.From] = m
	}
	m[r.To] = append(m[r.To], r)
	g.dirty = true
}

// path returns the node path src … dst (inclusive) via BFS, or nil.
func (g *dayGraph) path(src, dst string, maxDepth int) []string {
	if _, ok := g.adj[src]; !ok {
		return nil
	}
	type item struct {
		node  string
		depth int
	}
	parent := map[string]string{src: ""}
	queue := []item{{src, 0}}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if it.depth >= maxDepth {
			continue
		}
		for next := range g.adj[it.node] {
			if _, seen := parent[next]; seen {
				continue
			}
			parent[next] = it.node
			if next == dst {
				var p []string
				for n := dst; n != ""; n = parent[n] {
					p = append(p, n)
				}
				for i, j := 0, len(p)-1; i < j; i, j = i+1, j-1 {
					p[i], p[j] = p[j], p[i]
				}
				return p
			}
			queue = append(queue, item{next, it.depth + 1})
		}
	}
	return nil
}

// stats returns the transactions lying on any loop (edges inside a strongly
// connected component) and the number of independent loops
// (Σ edges − nodes + 1 per component). Cached until the graph changes.
func (g *dayGraph) stats() loopStats {
	if !g.dirty {
		return g.res
	}
	g.dirty = false
	comp := scc(g.adj)
	size := map[int]int{}
	for _, c := range comp {
		size[c]++
	}
	edges := map[int]int{}
	var res loopStats
	for u, m := range g.adj {
		for v, recs := range m {
			cu, ok1 := comp[u]
			cv, ok2 := comp[v]
			if !ok1 || !ok2 || cu != cv || size[cu] < 2 {
				continue
			}
			edges[cu]++
			for _, r := range recs {
				res.txns++
				res.amount += r.Amount
				res.recs = append(res.recs, r)
			}
		}
	}
	for c, e := range edges {
		res.loops += e - size[c] + 1
	}
	g.res = res
	return res
}

// scc labels each node with its strongly connected component (iterative Tarjan).
func scc[T any](adj map[string]map[string]T) map[string]int {
	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	comp := map[string]int{}
	var stack []string
	next, ncomp := 0, 0

	type frame struct {
		node string
		succ []string
		i    int
	}
	succOf := func(n string) []string {
		out := make([]string, 0, len(adj[n]))
		for v := range adj[n] {
			out = append(out, v)
		}
		return out
	}

	nodes := map[string]struct{}{}
	for u, m := range adj {
		nodes[u] = struct{}{}
		for v := range m {
			nodes[v] = struct{}{}
		}
	}
	for root := range nodes {
		if _, ok := index[root]; ok {
			continue
		}
		index[root], low[root] = next, next
		next++
		stack = append(stack, root)
		onStack[root] = true
		call := []frame{{root, succOf(root), 0}}
		for len(call) > 0 {
			f := &call[len(call)-1]
			if f.i < len(f.succ) {
				w := f.succ[f.i]
				f.i++
				if _, ok := index[w]; !ok {
					index[w], low[w] = next, next
					next++
					stack = append(stack, w)
					onStack[w] = true
					call = append(call, frame{w, succOf(w), 0})
				} else if onStack[w] && index[w] < low[f.node] {
					low[f.node] = index[w]
				}
				continue
			}
			v := f.node
			if low[v] == index[v] {
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[w] = false
					comp[w] = ncomp
					if w == v {
						break
					}
				}
				ncomp++
			}
			call = call[:len(call)-1]
			if len(call) > 0 {
				p := call[len(call)-1].node
				if low[v] < low[p] {
					low[p] = low[v]
				}
			}
		}
	}
	return comp
}
