package windowclient_test

// UNRUN under GR-3. These compare the actual consumer interfaces, rather than
// copied method sets; no socket or application starts.
import (
	"github.com/mngeow/ratatoskr/internal/tui"
	"github.com/mngeow/ratatoskr/internal/windowclient"
)

var _ tui.Pane = (*windowclient.Pane)(nil)
var _ tui.Router = (*windowclient.Client)(nil)
