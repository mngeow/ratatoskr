package tui

// Pane geometry, in one place.
//
// One pane takes the full width; two panes keep the split and the single-column
// separator that plain local rtk has always had. Every consumer asks these helpers
// - pane headers, screen composition, resize requests, cursor offsets, mouse hit
// testing and drag clamping - so the drawn layout and the interactive layout
// cannot drift apart.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

// paneBox is one pane's rectangle on screen.
type paneBox struct {
	index  int
	x      int // first column, inclusive
	width  int // columns, at least 1 when rows >= 1
	header int // the row its header sits on
	top    int // first body row
	rows   int // body rows
}

// activeCount is how many panes this model has.
//
// The constructor decides it; nothing derives it from the layout or from
// Snapshot.Runtime, so a connected run with one agent and a local run with two
// cannot be confused for one another.
func (m *Model) activeCount() int {
	switch {
	case m.active < 1:
		return 1
	case m.active > len(m.slots):
		return len(m.slots)
	default:
		return m.active
	}
}

// single reports whether only one pane exists, which changes the keys and hints.
func (m *Model) single() bool { return m.activeCount() == 1 }

// boxes returns the active panes' rectangles for the current terminal size.
func (m *Model) boxes() []paneBox {
	count := m.activeCount()
	boxes := make([]paneBox, 0, count)

	rows := m.screenRows()
	header := screenTop() - 1
	if rows < 1 {
		// No body to speak of; the header rows still exist, so a hit test can
		// still tell which pane a header belongs to.
		for i := 0; i < count; i++ {
			boxes = append(boxes, paneBox{index: i, header: header, top: screenTop()})
		}
		return boxes
	}

	if count == 1 {
		return append(boxes, paneBox{
			index:  0,
			x:      0,
			width:  m.width,
			header: header,
			top:    screenTop(),
			rows:   rows,
		})
	}

	left, right := paneWidths(m.width)
	return append(boxes,
		paneBox{index: 0, x: 0, width: left, header: header, top: screenTop(), rows: rows},
		// The separator column belongs to no pane.
		paneBox{index: 1, x: left + 1, width: right, header: header, top: screenTop(), rows: rows},
	)
}

// boxFor returns one active pane's rectangle.
func (m *Model) boxFor(index int) (paneBox, bool) {
	for _, box := range m.boxes() {
		if box.index == index {
			return box, true
		}
	}
	return paneBox{}, false
}

// boxAt returns the active pane whose header or body contains a cell.
func (m *Model) boxAt(x, y int) (paneBox, bool) {
	for _, box := range m.boxes() {
		if box.width < 1 {
			continue
		}
		if y == box.header && x >= box.x && x < box.x+box.width {
			return box, true
		}
		if box.rows > 0 && y >= box.top && y < box.top+box.rows && x >= box.x && x < box.x+box.width {
			return box, true
		}
	}
	return paneBox{}, false
}

// inHeader reports whether a cell is on this pane's header row.
func (b paneBox) inHeader(x, y int) bool {
	return b.width > 0 && y == b.header && x >= b.x && x < b.x+b.width
}

// inBody reports whether a cell is inside this pane's body.
func (b paneBox) inBody(x, y int) bool {
	if b.width < 1 || b.rows < 1 {
		return false
	}
	return y >= b.top && y < b.top+b.rows && x >= b.x && x < b.x+b.width
}

// relative converts a screen cell into pane-relative coordinates.
func (b paneBox) relative(x, y int) (int, int) {
	return x - b.x, y - b.top
}
