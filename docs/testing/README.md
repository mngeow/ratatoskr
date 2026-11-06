# Testing the daemon by hand

**Nobody has run the daemon.** It's written and compile-checked only, so every expected result comes from reading the code. Tell the team when something differs.

**Start with the [full guide](index.html),** which has diagrams, scenario navigation, fill-in fields and copy buttons. Open it from a local checkout, because GitHub shows `.html` as source. The READMEs below hold the same commands, for reading on a VM.

| Scenario | Setup | Status today |
| --- | --- | --- |
| [1. One Mac](scenario-1-local/README.md) | Gateway and two daemons on loopback, with two of each harness | Supported |
| [2. LAN, plain HTTP](scenario-2-lan-http/README.md) | Gateway on an Ubuntu VM, daemons on a second VM and your Mac | Supported |
| [3. Tailnet](scenario-3-tailnet/README.md) | The same machines over Tailscale | Supported **without HTTPS**. `rtk` refuses HTTPS on a Tailscale address |

Everything lives under `~/rtk-test`, so your normal `rtk` binary, pairing and database aren't touched.
