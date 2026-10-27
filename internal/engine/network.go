package engine

import (
	"sort"

	"github.com/vansh7877/vansh-repo/internal/model"
)

// Cycle is a money loop across the whole dataset (any CSPs, any days).
type Cycle struct {
	ID       int         `json:"id"`
	Accounts []string    `json:"accounts"`
	CSPs     []string    `json:"csps"`
	Txns     int         `json:"txns"`
	Amount   model.Money `json:"amount"`
	Days     int         `json:"days"`
	CrossCSP bool        `json:"cross_csp"`
}

// Hub is an account used at many CSPs.
type Hub struct {
	Account  string      `json:"account"`
	CSPs     []string    `json:"csps"`
	CSPCount int         `json:"csp_count"`
	Txns     int         `json:"txns"`
	Amount   model.Money `json:"amount"`
}

// RingEdge links a CSP and an account inside a ring.
type RingEdge struct {
	CSP     string `json:"csp"`
	Account string `json:"account"`
	Weight  int    `json:"weight"`
}

// Ring is a connected group of CSPs and accounts joined by suspicious activity.
type Ring struct {
	ID       int            `json:"id"`
	CSPs     []string       `json:"csps"`
	Accounts []string       `json:"accounts"`
	Alerts   int            `json:"alerts"`
	Cycles   int            `json:"cycles"`
	Rules    map[string]int `json:"rules"`
	Edges    []RingEdge     `json:"edges"`
}

// Network is the rule-14 view.
type Network struct {
	Cycles []Cycle `json:"cycles"`
	Hubs   []Hub   `json:"hubs"`
	Rings  []Ring  `json:"rings"`
}

// Rules whose account hits link a CSP to an account in the ring graph.
var ringRules = []string{RuleRoundTrip, RuleTestDeposit, RuleRelative, RulePassThrough, RuleHubAccount, RuleNearThreshold}

// Network returns cycles, hubs and rings (cached per data version).
func (e *Engine) Network() Network {
	e.mu.Lock()
	defer e.mu.Unlock()
	return *e.networkLocked()
}

func (e *Engine) networkLocked() *Network {
	c := &e.cache
	if c.net != nil && c.netVersion == e.version {
		return c.net
	}
	n := e.buildNetwork()
	c.net, c.netVersion = &n, e.version
	return c.net
}

type edgeAgg struct {
	txns   int
	amount model.Money
	csps   map[string]int // CSP -> transactions on this edge
	days   map[string]struct{}
}

func (e *Engine) buildNetwork() Network {
	var net Network

	// Global money graph and hub statistics in one pass.
	adj := map[string]map[string]*edgeAgg{}
	hubs := map[string]*Hub{}
	for a, set := range e.acctCSPs {
		if len(set) >= e.cfg.HubMinCSPs {
			h := &Hub{Account: a, CSPCount: len(set)}
			for c := range set {
				h.CSPs = append(h.CSPs, c)
			}
			sort.Strings(h.CSPs)
			hubs[a] = h
		}
	}
	for _, r := range e.recs {
		if r.Failed {
			continue
		}
		for _, a := range [2]string{r.From, r.To} {
			if h := hubs[a]; h != nil {
				h.Txns++
				h.Amount += r.Amount
			}
		}
		if !e.roundTripEdge(r) {
			continue
		}
		m := adj[r.From]
		if m == nil {
			m = map[string]*edgeAgg{}
			adj[r.From] = m
		}
		ea := m[r.To]
		if ea == nil {
			ea = &edgeAgg{csps: map[string]int{}, days: map[string]struct{}{}}
			m[r.To] = ea
		}
		ea.txns++
		ea.amount += r.Amount
		ea.csps[r.CSP]++
		ea.days[r.Day] = struct{}{}
	}
	for _, h := range hubs {
		net.Hubs = append(net.Hubs, *h)
	}
	sort.Slice(net.Hubs, func(i, j int) bool {
		if net.Hubs[i].CSPCount != net.Hubs[j].CSPCount {
			return net.Hubs[i].CSPCount > net.Hubs[j].CSPCount
		}
		return net.Hubs[i].Account < net.Hubs[j].Account
	})
	if len(net.Hubs) > 200 {
		net.Hubs = net.Hubs[:200]
	}

	comp := scc(adj)
	type cyc struct {
		accts map[string]struct{}
		csps  map[string]struct{}
		days  map[string]struct{}
		txns  int
		amt   model.Money
	}
	cycles := map[int]*cyc{}
	size := map[int]int{}
	for _, c := range comp {
		size[c]++
	}
	for u, m := range adj {
		for v, ea := range m {
			if comp[u] != comp[v] || size[comp[u]] < 2 {
				continue
			}
			cy := cycles[comp[u]]
			if cy == nil {
				cy = &cyc{accts: map[string]struct{}{}, csps: map[string]struct{}{}, days: map[string]struct{}{}}
				cycles[comp[u]] = cy
			}
			cy.accts[u], cy.accts[v] = struct{}{}, struct{}{}
			for c := range ea.csps {
				cy.csps[c] = struct{}{}
			}
			for d := range ea.days {
				cy.days[d] = struct{}{}
			}
			cy.txns += ea.txns
			cy.amt += ea.amount
		}
	}
	for _, cy := range cycles {
		net.Cycles = append(net.Cycles, Cycle{Accounts: capList(sortedKeys(cy.accts), 50),
			CSPs: sortedKeys(cy.csps), Txns: cy.txns, Amount: cy.amt, Days: len(cy.days), CrossCSP: len(cy.csps) > 1})
	}
	sort.Slice(net.Cycles, func(i, j int) bool {
		if net.Cycles[i].CrossCSP != net.Cycles[j].CrossCSP {
			return net.Cycles[i].CrossCSP
		}
		return net.Cycles[i].Amount > net.Cycles[j].Amount
	})
	if len(net.Cycles) > 200 {
		net.Cycles = net.Cycles[:200]
	}
	for i := range net.Cycles {
		net.Cycles[i].ID = i + 1
	}

	// Rings: union-find over CSP and account nodes.
	uf := newUnionFind()
	type ek struct{ csp, acct string }
	edges := map[ek]int{}
	edgeRules := map[ek]map[string]int{}
	for csp, live := range e.csps {
		for _, rule := range ringRules {
			for acct, hits := range live.accounts[rule] {
				uf.union("c:"+csp, "a:"+acct)
				k := ek{csp, acct}
				edges[k] += hits
				if edgeRules[k] == nil {
					edgeRules[k] = map[string]int{}
				}
				edgeRules[k][rule] += hits
			}
		}
	}
	// Cycle legs link the CSP where each leg happened to both accounts.
	for u, m := range adj {
		for v, ea := range m {
			if comp[u] != comp[v] || size[comp[u]] < 2 {
				continue
			}
			for c, n := range ea.csps {
				for i, a := range [2]string{u, v} {
					k := ek{c, a}
					edges[k] += n
					if edgeRules[k] == nil {
						edgeRules[k] = map[string]int{}
					}
					if i == 0 { // count each leg once
						edgeRules[k]["money_cycle"] += n
					}
				}
			}
		}
	}
	cycleRoot := map[string]int{}
	for _, cy := range cycles {
		var first string
		for a := range cy.accts {
			if first == "" {
				first = "a:" + a
			}
			uf.union(first, "a:"+a)
		}
		for c := range cy.csps {
			uf.union(first, "c:"+c)
		}
	}
	for _, cy := range cycles {
		for a := range cy.accts {
			cycleRoot[uf.find("a:"+a)]++
			break
		}
	}

	type ringAcc struct {
		csps, accts map[string]struct{}
		alerts      int
		rules       map[string]int
		edges       []RingEdge
	}
	// A ring needs evidence beyond shared hub accounts: a cycle or another rule.
	strong := map[string]bool{}
	for k, rules := range edgeRules {
		for rule := range rules {
			if rule != RuleHubAccount {
				strong[uf.find("c:"+k.csp)] = true
			}
		}
	}
	for root := range cycleRoot {
		strong[root] = true
	}
	rings := map[string]*ringAcc{}
	ringOf := func(node string) *ringAcc {
		root := uf.find(node)
		r := rings[root]
		if r == nil {
			r = &ringAcc{csps: map[string]struct{}{}, accts: map[string]struct{}{}, rules: map[string]int{}}
			rings[root] = r
		}
		return r
	}
	for node := range uf.parent {
		r := ringOf(node)
		if node[0] == 'c' {
			r.csps[node[2:]] = struct{}{}
		} else {
			r.accts[node[2:]] = struct{}{}
		}
	}
	for k, w := range edges {
		r := ringOf("c:" + k.csp)
		r.edges = append(r.edges, RingEdge{CSP: k.csp, Account: k.acct, Weight: w})
		for rule, n := range edgeRules[k] {
			r.rules[rule] += n
			if rule != "money_cycle" {
				r.alerts += n
			}
		}
	}
	for root, r := range rings {
		if !strong[root] || len(r.csps) < 2 && !(len(r.csps) >= 1 && len(r.accts) >= 3) {
			continue
		}
		sort.Slice(r.edges, func(i, j int) bool { return r.edges[i].Weight > r.edges[j].Weight })
		net.Rings = append(net.Rings, Ring{CSPs: capList(sortedKeys(r.csps), 100),
			Accounts: capList(sortedKeys(r.accts), 100), Alerts: r.alerts, Cycles: cycleRoot[root],
			Rules: r.rules, Edges: capEdges(r.edges, 300)})
	}
	sort.Slice(net.Rings, func(i, j int) bool {
		si := net.Rings[i].Alerts + 10*len(net.Rings[i].CSPs)
		sj := net.Rings[j].Alerts + 10*len(net.Rings[j].CSPs)
		if si != sj {
			return si > sj
		}
		return net.Rings[i].CSPs[0] < net.Rings[j].CSPs[0]
	})
	if len(net.Rings) > 100 {
		net.Rings = net.Rings[:100]
	}
	for i := range net.Rings {
		net.Rings[i].ID = i + 1
	}
	if net.Cycles == nil {
		net.Cycles = []Cycle{}
	}
	if net.Hubs == nil {
		net.Hubs = []Hub{}
	}
	if net.Rings == nil {
		net.Rings = []Ring{}
	}
	return net
}

func capList(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func capEdges(s []RingEdge, n int) []RingEdge {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type unionFind struct{ parent map[string]string }

func newUnionFind() *unionFind { return &unionFind{parent: map[string]string{}} }

func (u *unionFind) find(x string) string {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
		return x
	}
	root := x
	for u.parent[root] != root {
		root = u.parent[root]
	}
	for u.parent[x] != root {
		u.parent[x], x = root, u.parent[x]
	}
	return root
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}
