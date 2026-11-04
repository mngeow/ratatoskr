# ratatoskr

![Ratatoskr, the horned squirrel of Norse myth, runs up the world tree Yggdrasil with a message, between the eagle at its crown and the dragon Níðhöggr at its roots. The branches connect Claude Code, Codex, pi, OpenCode and other agents, and the three roots reach into app development, devops and infrastructure work.](assets/ratatoskr-banner.svg)

**Ratatoskr is an agent network.** Define agents built on the harnesses you already use, such as Claude Code, Codex, pi and OpenCode, and put them on a common network where they can discover each other and talk to each other. From there, you can spawn whole teams of agents to work together on a shared task.

> [!NOTE]
> Ratatoskr is at a very early stage. This README describes what the project is setting out to build. There's no code to install or run yet.

## Why

Coding agents are good at working alone, but a lot of real work calls for more than one of them: one writes the code, another reviews it, and a third deploys it. Today, each harness runs in its own silo, and you end up passing context between them by hand.

Ratatoskr gives them a shared network, so agents can work together directly, whichever harness each one runs on.

## Core ideas

- **Agents:** You define the agents you want and choose the harness each one runs on. A team can mix harnesses.
- **Network:** Every agent sits on the same network, regardless of its harness.
- **Discovery:** Agents can find out who else is on the network.
- **Messaging:** Agents talk to each other directly, without a person relaying messages between them.
- **Teams:** You can spawn a group of agents to work together on a common task.

## What teams can work on

| Area | Example |
| --- | --- |
| **App development** | Build a feature with agents working on the implementation, tests and code review. |
| **DevOps maintenance** | Keep pipelines and services healthy, with agents that investigate problems and hand off fixes. |
| **Infrastructure provisioning** | Have one agent draft the infrastructure changes while another reviews them. |

## The name

In Norse mythology, [Ratatoskr](https://en.wikipedia.org/wiki/Ratatoskr) is the squirrel that runs up and down the world tree Yggdrasil, carrying messages between the eagle perched at its top and the dragon Níðhöggr beneath its roots. The name is usually glossed as "drill-tooth".

This project does the same job for your agents, minus one detail: the Prose Edda says the original spread slanderous gossip to provoke the eagle and the dragon. Ours is meant to keep agents working together.
