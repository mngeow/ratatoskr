package tui

// UNTESTED. Graph rectangles are independent of pane rectangles and never resize a PTY.
type graphRect struct{ x, y, width, height int }

func (r graphRect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.width && y >= r.y && y < r.y+r.height
}

type graphGeometry struct {
	wide   bool
	left   int
	detail graphRect
	nodes  map[string]graphRect
}

func (m *Model) graphGeometry() graphGeometry {
	g := graphGeometry{nodes: map[string]graphRect{}}
	count := 0
	for _, n := range m.graph.order {
		if _, ok := m.graphHost(n); ok {
			count++
		}
	}
	g.wide = m.width >= 120 && m.height >= 26 && count <= 12 && len(m.graph.order) <= 12 && !m.graph.fullDetail && !m.graph.edgeView
	if !g.wide {
		return g
	}
	g.left = 60
	g.detail = graphRect{61, screenTop(), max(1, m.width-61), m.screenRows()}
	visible := map[string]bool{}
	for _, n := range m.graphNames() {
		visible[n] = true
	}
	for i, n := range m.graph.order {
		if n == "" || !visible[n] {
			continue
		}
		g.nodes[n] = graphRect{1 + (i%3)*20, screenTop() + 1 + (i/3)*3, 18, 3}
	}
	return g
}
