// The A2A reference adapter is its own module: it is a standalone server an outsider can run, and it
// must not borrow anything from the Ratatoskr repository's internal packages.
module github.com/mngeow/ratatoskr/integrations/a2a-reference

go 1.26.0

// The SDK is the only direct dependency: everything else is required by it, which is why the rest are
// marked indirect. No Ratatoskr package is imported.
require github.com/a2aproject/a2a-go/v2 v2.8.0

require (
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
)
