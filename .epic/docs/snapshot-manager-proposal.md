# Proposta de Implementação: Gerenciador de Snapshots btrfs

> Status: **proposta** (rascunho para discussão)
> Autor: bentoolkit team
> Data: 2026-06-08
> Escopo: novo subcomando `bentoo snapshot` para gerenciar snapshots btrfs,
> agendamento e envio para destinos externos (SSH, nuvem/S3, Google Drive).

---

## 1. Objetivo

Adicionar ao bentoolkit um recurso para **gerenciar snapshots btrfs** com:

- criação de snapshots locais (manuais e agendados);
- retenção/poda automática (política GFS — hourly/daily/weekly/monthly);
- envio para destinos externos (disco/host remoto via SSH, nuvem/S3, ou
  qualquer storage suportado por rclone, como Google Drive);
- notificações de sucesso/falha;
- agendamento via systemd timers;
- restauração/rollback.

O bentoolkit atua como **orquestrador declarativo**: uma configuração TOML
única descreve a intenção, e o bentoolkit aciona ferramentas maduras
(btrbk, snapper, restic, rclone) — sem reimplementar a lógica crítica de
manipulação do filesystem.

---

## 2. Princípios de design

1. **Não reimplementar o crítico.** Snapshot, send/receive, rollback e poda
   ficam a cargo de ferramentas testadas (btrbk, snapper, restic). O bentoolkit
   gera config e orquestra; não toca no btrfs diretamente em caminhos
   destrutivos.
2. **Camadas ortogonais e plugáveis.** Snapshot (ENGINE), envio (SHIP),
   notificação (NOTIFY) e agendamento (SCHEDULE) são independentes e
   selecionáveis por config. Trocar de destino não afeta o motor de snapshot.
3. **Config declarativa única.** Um arquivo TOML versionável (mesmo estilo do
   `autoupdate`) é a fonte de verdade. `apply` materializa configs nativas das
   ferramentas + systemd timers.
4. **Reuso do que já existe no bentoolkit.** `internal/common/logger`,
   `output`, `httputil`, `config`, `git`.
5. **Degradação clara.** Backends ausentes no PATH produzem erro acionável
   (mesmo padrão usado com playwright/chromedp no autoupdate).
6. **Sem daemon próprio.** O agendamento é delegado ao systemd.

---

## 3. Arquitetura em camadas

```
┌─ bentoo snapshot ─────────────────────────────────────────────────┐
│                                                                    │
│  ENGINE     snapshot local + poda          btrbk | snapper        │
│  SHIP       envio p/ destino externo       ssh | restic | archive │
│  NOTIFY     resultado do run               ntfy | healthchecks |   │
│                                            webhook | email        │
│  SCHEDULE   quando rodar                   systemd .timer/.service │
│                                                                    │
└────────────────────────────────────────────────────────────────────┘
```

Fluxo de um run agendado:

```
systemd .timer → `bentoo snapshot run` →
    ENGINE.Create() → ENGINE.Prune() → [SHIP.Send() ...] → NOTIFY.Notify(result)
```

---

## 4. Ferramentas externas e papéis

| Papel              | Ferramenta | Por quê                                                        | Disponibilidade |
|--------------------|------------|----------------------------------------------------------------|-----------------|
| ENGINE (backup)    | **btrbk**  | snapshot + send/receive SSH + stream_compress + timer próprio   | `app-backup/btrbk` (Portage) |
| ENGINE (rollback)  | **snapper**| rollback local + hooks de `emerge` + timeline                  | Portage         |
| SHIP (nuvem/S3)    | **restic** | dedup + cripto + compressão zstd + S3/B2/GCS/rclone nativos     | `app-backup/restic` (Portage) |
| SHIP (transporte)  | **rclone** | 70+ backends, incl. Google Drive; usado por `archive` e restic | `net-misc/rclone` (Portage) |

**Descartado:** timeshift (só subvolume root, sem remoto), buttersink
(S3 nativo, mas **abandonado**).

### 4.1 Os dois modelos de envio para a nuvem

O bentoolkit oferece **ambos** como drivers da camada SHIP, pois resolvem
necessidades diferentes:

- **`restic`** — backup em nível de arquivo a partir do snapshot read-only.
  Dedup, criptografia, compressão, restauração granular, destino agnóstico.
  *Recomendado para nuvem/S3 e backups frequentes.*
- **`archive`** — `btrfs send | zstd > arquivo` enviado via rclone para
  qualquer storage. Restauração bit-exact do subvolume, conceito trivial,
  vai para Google Drive/Dropbox/etc. *Recomendado para datasets pequenos,
  backups esporádicos e quando se quer fidelidade total do subvolume.*

Trade-off resumido:

| Critério                         | `archive` (send→arquivo) | `restic` |
|----------------------------------|--------------------------|----------|
| Compressão / dedup / cripto      | manual / ❌ / manual     | nativo   |
| Restauração de 1 arquivo         | ❌ (baixa tudo)          | ✅       |
| Restauração bit-exact subvolume  | ✅                       | ⚠️ não  |
| Destino precisa ser btrfs        | só p/ restore (receive)  | ❌       |
| Robustez a corrupção             | stream tudo-ou-nada      | repo verificável |
| Vai p/ Google Drive              | ✅ (rclone)              | ✅ (rclone) |

---

## 5. Modelo de configuração (TOML)

Arquivo padrão: `~/.config/bentoo/snapshot.toml` (ou `/etc/bentoo/snapshot.toml`
para uso de sistema). Resolução de caminho segue o padrão já usado por
`internal/common/config`.

```toml
# ── ENGINE: como tirar e podar snapshots locais ───────────────────
[engine]
driver = "btrbk"                 # "btrbk" | "snapper"
subvolumes = ["/", "/home"]
snapshot_dir = "/.snapshots"     # onde os snapshots locais vivem

[engine.retention]               # política GFS (poda)
preserve_min = "latest"
hourly  = 24
daily   = 7
weekly  = 4
monthly = 6

# ── SHIP: destinos externos (lista; zero ou mais) ─────────────────
[[ship]]
name = "offsite-ssh"
type = "ssh"                     # delega send/receive ao btrbk
target = "user@host:/backup/btrbk"
stream_compress = "zstd"

[[ship]]
name = "cloud-restic"
type = "restic"                  # recomendado p/ S3/nuvem
repo = "s3:s3.amazonaws.com/meu-bucket/restic"
password_file = "/etc/bentoo/restic.pass"
compression = "auto"            # auto | max | off
mount_strategy = "private-ns"   # monta snapshot RO em mount namespace privado

[[ship]]
name = "gdrive-archive"
type = "archive"                 # arquivo único portável
remote = "gdrive:/backups"       # remote rclone (Google Drive)
mode = "incremental"            # "full" | "incremental"
compress = "zstd"

# ── NOTIFY: avisos de resultado ───────────────────────────────────
[notify]
on = ["failure", "success"]      # eventos que disparam aviso
[notify.ntfy]
url = "https://ntfy.sh/meu-topico"
[notify.healthchecks]
ping_url = "https://hc-ping.com/UUID"
[notify.webhook]
url = "https://exemplo.com/hook"

# ── SCHEDULE: agendamento systemd ─────────────────────────────────
[schedule]
backend = "systemd"
on_calendar = "hourly"           # sintaxe OnCalendar= do systemd
persistent = true                # roda no boot se perdeu janela
randomized_delay = "5m"
```

Mapeamento para structs Go segue o estilo de `internal/autoupdate/config.go`
(tags `toml:"...,omitempty"`, ponteiros para campos opcionais).

---

## 6. Estrutura de código

Espelha a organização de `internal/autoupdate/` e `cmd/bentoo/overlay_*.go`.

```
internal/snapshot/
  config.go            # TOML → structs + validação (Validate())
  config_test.go
  manager.go           # orquestra ENGINE→SHIP→NOTIFY; ponto de entrada do run
  manager_test.go
  result.go            # RunResult (status por etapa, durações, erros)

  engine.go            # interface Engine
  engine_btrbk.go      # render btrbk.conf + invoca `btrbk run`
  engine_snapper.go    # render config snapper + `snapper create`
  engine_*_test.go

  ship.go              # interface Shipper
  ship_ssh.go          # delega ao btrbk (targets remotos)
  ship_restic.go       # snapshot RO → mount (ns privado) → `restic backup`
  ship_archive.go      # `btrfs send [-p parent] | zstd | rclone rcat`
  parent.go            # tracking de snapshot-pai p/ incremental (archive)
  ship_*_test.go

  notify.go            # interface Notifier + ntfy/healthchecks/webhook/email
  notify_test.go

  systemd.go           # render/instala/habilita .timer e .service
  systemd_test.go

  detect.go            # checa btrbk/snapper/restic/rclone no PATH
  restore.go           # lógica de restore/rollback por driver

cmd/bentoo/
  snapshot.go          # grupo cobra `snapshot` (registra subcomandos)
  snapshot_apply.go    # materializa configs + instala timers
  snapshot_run.go      # executa o pipeline (alvo do timer)
  snapshot_list.go     # lista snapshots locais e remotos
  snapshot_status.go   # estado: último run, falhas, espaço, timers
  snapshot_prune.go    # poda manual conforme retenção
  snapshot_restore.go  # restore/rollback
  snapshot_*_test.go
```

---

## 7. Interfaces Go (núcleo)

```go
package snapshot

import "context"

// Snapshot identifica um snapshot local criado pelo ENGINE.
type Snapshot struct {
    ID         string    // identificador estável (ex.: nome do subvolume RO)
    Subvolume  string
    Path       string
    CreatedAt  time.Time
    ReadOnly   bool
    ParentID   string    // p/ incremental (vazio = full)
}

// Engine cria e poda snapshots locais.
type Engine interface {
    Name() string
    Create(ctx context.Context, sv string) (Snapshot, error)
    Prune(ctx context.Context, sv string, policy Retention) ([]Snapshot, error)
    List(ctx context.Context, sv string) ([]Snapshot, error)
}

// Shipper envia um snapshot para um destino externo.
type Shipper interface {
    Name() string
    Send(ctx context.Context, snap Snapshot) (ShipReport, error)
}

// Notifier reporta o resultado de um run.
type Notifier interface {
    Notify(ctx context.Context, res RunResult) error
}

// Scheduler materializa o agendamento (systemd).
type Scheduler interface {
    Apply(ctx context.Context, cfg ScheduleConfig) error  // cria/atualiza timer
    Remove(ctx context.Context) error
}
```

`Manager` injeta as implementações concretas conforme o TOML e roda o pipeline,
acumulando `RunResult` (status por etapa, durações, erros) para o `Notifier`.

---

## 8. Comandos CLI

```
bentoo snapshot apply        # materializa configs nativas + instala/atualiza timers
bentoo snapshot run          # executa o pipeline agora (alvo do systemd timer)
bentoo snapshot list         # lista snapshots locais e nos destinos
bentoo snapshot status       # último run, falhas, uso de espaço, estado dos timers
bentoo snapshot prune        # poda manual conforme política de retenção
bentoo snapshot restore <id> # restore/rollback (comportamento depende do driver)
```

Flags relevantes: `--config <path>`, `--dry-run` (mostra o que faria sem
executar), `--subvolume <sv>`, `--ship <name>` (restringe a um destino),
`--yes` (não interativo). Convenções idênticas às de `overlay autoupdate`.

Registro no `rootCmd` em `cmd/bentoo/main.go`:

```go
rootCmd.AddCommand(snapshotCmd)   // snapshotCmd agrega os subcomandos
```

---

## 9. Detalhes por camada

### 9.1 ENGINE

- **btrbk:** o bentoolkit renderiza `btrbk.conf` a partir do TOML e invoca
  `btrbk run`/`btrbk clean`. Retenção GFS mapeada para diretivas
  `snapshot_preserve*` / `target_preserve*`.
- **snapper:** renderiza `/etc/snapper/configs/<nome>` e usa
  `snapper -c <nome> create` / `setup-quota` / cleanup `timeline`. Opcional:
  instalar hook de `emerge` (snapshot pre/post) — documentado, não obrigatório.

### 9.2 SHIP

- **ssh:** delegado inteiramente ao btrbk (target remoto no `btrbk.conf`);
  o bentoolkit não move bytes.
- **restic:** cria snapshot **read-only**, monta em **mount namespace privado**
  (via diretiva no `.service`, conforme best practice), roda `restic backup`,
  depois `restic forget --prune` conforme retenção. Senha via `password_file`
  (nunca inline).
- **archive:** `btrfs send [-p <parent>] | zstd | rclone rcat remote:path/arquivo`.
  Para `mode = "incremental"`, `parent.go` persiste o UUID/ID do último snapshot
  enviado por (subvolume, destino) e o usa como `-p`. Registra claramente quando
  faz full vs incremental.

### 9.3 NOTIFY

- Implementações reusam `internal/common/httputil` (UA/headers/timeout).
- **ntfy/webhook:** POST com corpo resumindo o `RunResult`.
- **healthchecks:** ping de sucesso (`/UUID`) ou falha (`/UUID/fail`).
- **email:** opcional via `sendmail`/SMTP (fase posterior).
- Disparo condicionado a `notify.on` (`failure` e/ou `success`).

### 9.4 SCHEDULE (systemd)

`bentoo snapshot apply` gera, sob `/etc/systemd/system/` (sistema) ou
`~/.config/systemd/user/` (usuário):

```ini
# bentoo-snapshot.service
[Unit]
Description=bentoo snapshot run

[Service]
Type=oneshot
ExecStart=/usr/bin/bentoo snapshot run --config %CONFIG%
# mount namespace privado p/ montar snapshots RO com segurança (driver restic)
PrivateMounts=yes
```

```ini
# bentoo-snapshot.timer
[Unit]
Description=bentoo snapshot schedule

[Timer]
OnCalendar=hourly
Persistent=true
RandomizedDelaySec=5m

[Install]
WantedBy=timers.target
```

Depois roda `systemctl daemon-reload` + `enable --now`. `--dry-run` imprime
as units sem escrever.

---

## 10. Restauração / rollback

`bentoo snapshot restore <id>` despacha conforme o driver de origem:

- **archive:** baixa o arquivo (rclone), `zstd -d | btrfs receive` para um
  subvolume temporário; em `incremental`, baixa e aplica a cadeia
  full→deltas na ordem correta (validando integridade da cadeia antes).
- **restic:** `restic restore <snap> --target <path>` (suporta restaurar
  arquivo/subdiretório individual).
- **snapper:** `snapper rollback` (rollback de sistema) — caminho dedicado,
  com confirmação obrigatória.

Operações destrutivas exigem `--yes` ou confirmação interativa.

---

## 11. Empacotamento (ebuild)

O recurso vira opcional via USE flags, puxando backends sob demanda:

```
IUSE="btrbk snapper restic rclone systemd"

RDEPEND="
    btrbk?   ( app-backup/btrbk )
    snapper? ( app-backup/snapper )
    restic?  ( app-backup/restic )
    rclone?  ( net-misc/rclone )
"
```

`detect.go` valida em runtime a presença das ferramentas conforme os drivers
ativados na config e falha com mensagem acionável (ex.:
`driver "restic" requer app-backup/restic no PATH`).

---

## 12. Segurança e segredos

- Senhas/credenciais **nunca** inline no TOML: usar `password_file`,
  variáveis de ambiente, ou integração com o mecanismo de segredos já usado
  pelo autoupdate (`meta fetch_*`).
- Configs de restic/rclone geradas com permissão `0600`.
- Restore destrutivo e rollback exigem confirmação explícita.

---

## 13. Testes

Seguindo o padrão do repo (cobertura alta em `internal/autoupdate`):

- **Unit:** render de `btrbk.conf`/`snapper config`/units systemd a partir do
  TOML (golden files); validação de config; seleção de parent incremental.
- **Mocks:** runner de comandos externos (espelhar `internal/common/git/mock.go`)
  para testar `Engine`/`Shipper` sem btrfs real.
- **Integração (gated):** testes `*_live_test.go` atrás de build tag/env, como já
  feito com `authfetch_live_test.go`, exigindo loopback btrfs + rclone configurado.

---

## 14. Roadmap por fases

**Fase 1 — MVP (snapshot local + SSH + agendamento)**
- `internal/snapshot` com `config`, `Engine(btrbk)`, `Shipper(ssh→btrbk)`,
  `systemd.go`.
- Comandos `apply`, `run`, `list`, `status`.
- USE flags `btrbk`, `systemd`.

**Fase 2 — Notificações**
- `Notifier` (ntfy, healthchecks, webhook) integrado ao `run`.

**Fase 3 — Nuvem**
- `Shipper(restic)` (mount RO + namespace privado) e `Shipper(archive)`
  (rclone, full + incremental), `parent.go`, `restore`.

**Fase 4 — Rollback local**
- `Engine(snapper)`, hooks de `emerge`, `snapper rollback`.

**Fase 5 — Polimento**
- email, `--dry-run` completo, documentação no README, ebuild final.

---

## 15. Riscos e mitigações

| Risco                                                | Mitigação |
|------------------------------------------------------|-----------|
| Cadeia incremental do `archive` quebra (elo perdido) | validar integridade da cadeia antes do restore; preferir restic p/ frequência |
| Stream `btrfs send` corrompido (tudo-ou-nada)        | recomendar restic p/ nuvem; checksum do objeto pós-upload |
| restic re-escaneia subvolume grande (lento)          | snapshot RO estável + dedup evita re-upload; documentar custo |
| Backend ausente no PATH                              | `detect.go` falha cedo com mensagem acionável |
| Operação destrutiva acidental (restore/rollback)     | confirmação obrigatória; `--dry-run` |

---

## 16. Decisões em aberto

1. Caminho padrão da config: `~/.config/bentoo/` vs `/etc/bentoo/` (provável:
   suportar ambos, preferindo o de sistema quando rodando como root).
2. Suportar **kopia** como driver SHIP alternativo (melhor p/ object storage,
   mas fora do Portage oficial) — fase futura via overlay/binário.
3. Integração de segredos: reusar `meta fetch_*` do autoupdate ou introduzir
   mecanismo dedicado.
4. Hook de `emerge` no snapper: opt-in via comando (`bentoo snapshot hook
   --install`) ou apenas documentado.

---

## Referências

- btrbk — <https://github.com/digint/btrbk> · `app-backup/btrbk`
- snapper — <https://wiki.archlinux.org/title/Snapper>
- restic — <https://restic.net> · `app-backup/restic` (compressão zstd desde 0.14)
- rclone — <https://rclone.org> · `net-misc/rclone`
- Atomic backups restic+btrfs — <https://blog.vsq.cz/blog/atomic-backups-with-restic-and-btrfs/>
- awesome-btrfs — <https://github.com/boredsquirrel/awesome-btrfs>
