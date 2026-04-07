## Quick start

```bash
curl -sSL https://get.vipas.dev | sudo sh
```

Opens at `http://your-server-ip:3000`. That's it.

**Upgrade:**

```bash
curl -sSL https://get.vipas.dev/upgrade | sudo sh
```

> **Requirements:** Linux (x86_64 / arm64), 2 CPU, 2 GB RAM minimum. Runs on any VPS, bare metal, or Raspberry Pi.

## Features

#### Applications
- Git push to deploy (GitHub App) or Docker image
- In-cluster builds via **Kaniko** — no Docker socket needed
- Rolling deploys, one-click rollback, cancel in-flight builds
- Custom domains with automatic TLS
- Environment variables, secrets, persistent volumes
- Health checks (liveness & readiness probes)
- Horizontal autoscaling (Kubernetes HPA)
- Web terminal into running containers

#### Databases
- PostgreSQL · MySQL · MariaDB · Redis · MongoDB
- Connection strings, external access via NodePort
- Automated S3 backups with schedule and retention
- Version management and health probes

#### Cron Jobs
- Native Kubernetes CronJobs
- Manual trigger, run history, real-time logs

#### Cluster
- Node overview with topology visualization
- Helm releases and DaemonSets
- Traefik ingress configuration editor
- Alert rules — CPU, memory, disk, node, pod events
- Auto-cleanup of evicted and failed pods

#### Team & Security
- Roles: Owner · Admin · Member
- Project-level permissions (admin / viewer)
- Two-factor authentication (TOTP)
- Team invitations via email

#### Notifications
- Email (SMTP) · Slack · Discord · Telegram
- Auto-fire on alert with per-channel toggle

#### Developer Experience
- Real-time log streaming
- `Cmd+K` global search
- Dark / light theme
- REST API

## Comparison

|  | Vipas | Coolify | Dokploy |
|---|:---:|:---:|:---:|
| **Orchestrator** | Kubernetes (K3s) | Docker Compose | Docker Swarm |
| **In-cluster builds** (no Docker socket) | Kaniko | — | — |
| **Rolling updates** | Native K8s | Custom | Custom |
| **Autoscaling** (HPA) | Yes | — | — |
| **Health probes** (liveness / readiness) | Yes | — | — |
| **Helm releases** management | Yes | — | — |
| **Node topology** view | Yes | — | — |
| **CronJobs** | K8s native | Custom | Custom |
| **kubectl / Helm** compatible | Yes | — | — |
| **Two-factor auth** (TOTP) | Yes | — | — |
| **RBAC** with project-level perms | Yes | Limited | Limited |
| **Alert rules** (CPU/Mem/Node/Pod) | Yes | — | Basic |
| **Database S3 backup** | Yes | Yes | Yes |
| **Docker Compose** support | — | Yes | Yes |
| **One-click templates** | — | Yes | Yes |

> Vipas doesn't wrap Kubernetes — it **is** Kubernetes.<br/>
> Your workloads run the same way they would on any K8s cluster, and everything you learn here applies everywhere else.

## Contributing

Contributions are welcome. Please see [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## License

Vipas is open-source under [AGPL-3.0](LICENSE) with [attribution terms](NOTICE). The "Powered by Vipas" notice must remain visible in derivative works. For a commercial license, [contact us](mailto:hello@vipas.dev).

---

<p align="center">
  <a href="mailto:hello@vipas.dev">Contact</a> ·
  <a href="https://github.com/sponsors/victorgomez09">Sponsor</a> ·
  <a href="https://github.com/victorgomez09/vipas">Documentation</a> ·
  <a href="https://github.com/victorgomez09/vipas/discussions">Community</a>
</p>
