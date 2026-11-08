# Testing the daemon by hand

**The owner reports that the daemon works, with one open bug:** text appears in the prompt box after an answer, and the cause is unknown. That report doesn't say which scenarios it covered. Every expected result here comes from reading the code, so tell the team when something differs.

Documentation home: [index.html](../index.html).

**Start with the [full guide](index.html),** which has diagrams, scenario navigation, fill-in fields and copy buttons. Open it from a local checkout, because GitHub shows `.html` as source. The READMEs below hold the same commands, for reading on a VM.

| Scenario | Setup | Status today |
| --- | --- | --- |
| [1. One Mac](scenario-1-local/README.md) | Gateway and two daemons on loopback, with two of each harness | Supported |
| [2. LAN, plain HTTP](scenario-2-lan-http/README.md) | Gateway on an Ubuntu VM, daemons on a second VM and your Mac | Supported |
| [3. Tailnet](scenario-3-tailnet/README.md) | The same machines over Tailscale | Supported **without HTTPS**. `rtk` refuses HTTPS on a Tailscale address |
| [4. Network tab and SSH entry](scenario-4-network-tab/README.md) | The Network tab and SSH entry, on the machines of scenario 2 or 3 |
| [5. Host cards and the Graph tab](scenario-5-host-cards/README.md) | Public host cards, scheduled polling, the gateway migration and the Graph tab, on the machines of scenario 2 or 3 | Written, **never run** |
| [6. A2A participants](scenario-6-a2a-participants/README.md) | Standard A2A tasks between managed sessions and an external reference service, with a clarification and a result, on the machines of scenario 2 or 3 | Written, **never run**; includes a database migration |

Everything lives under `~/rtk-test`, so your normal `rtk` binary, pairing and database aren't touched.
