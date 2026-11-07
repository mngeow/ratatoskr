# Empty-window detach review fix

Source qitem-20261107011826-e345423537b33bc9; return delivery.lead.
internal/attach/attach.go passes attached slot presence into finish. Empty plain
detach and signal-ending of that window select the pure endingEmpty message:

    detached; no agents were running (rtk attach --agents claude,codex starts some)

Windows with sessions retain their existing detach/signal messages. Lost-connection
handling and entry.go signal-during-SSH message unchanged. Written exact-string
case in internal/attach/attach_test.go; UNRUN under GR-3.

gofmt completed. Darwin arm64 CGO_ENABLED=0 build and vet -mod=readonly
./internal/attach both exit0, no native prompts. No tests/app/daemon/SSH/probes
executed, no commits/push. Behavior remains untested.
