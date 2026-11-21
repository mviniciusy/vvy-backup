# vvy-backup

Painel web para agendar, executar e monitorar backups feitos com `rclone sync` para o Google Drive.

O projeto substitui um conjunto de scripts agendados no cron, que não tinham histórico nem alertas: falhas de
autenticação no Drive e alterações no crontab só eram percebidas dias depois. O formato do backup não muda.
As pastas continuam espelhadas no destino, acessíveis pelo próprio Drive, e os arquivos removidos da origem
são movidos para uma lixeira organizada por data.

O vvy-backup não transfere dados diretamente. Ele envia ao `rclone rcd` as operações `sync/sync`, `copy/copy`
e `operations/delete`, acompanha o progresso por `core/stats` a cada 2 segundos e registra o resultado em SQLite.

## Funcionalidades

- Jobs de espelhamento, de cópia e de limpeza da lixeira, com agenda diária, semanal, mensal ou em formato cron
- Execução manual, simulação (dry-run) e pausa por job
- Seleção de pastas em árvore; as pastas desmarcadas viram regras `--exclude` do rclone
- Acompanhamento da execução em tempo real e histórico com volume, duração e erros
- Uso da cota do Google Drive por pasta e uso dos discos locais
- Mapa de execuções dos últimos 30 dias, agenda dos próximos 7 dias e tendência de crescimento do backup
- Verificação de saúde: autenticação no Drive, discos montados, jobs atrasados e versão do rclone
- Registro de jobs executados em outro host por meio do `deploy/bm-run`

O executável é um único binário Go, de aproximadamente 14 MB, compilado sem CGO. Além da biblioteca padrão,
as dependências são `modernc.org/sqlite` e `robfig/cron`. A interface não usa framework JavaScript.

## Requisitos

- Linux com `rclone rcd` em execução e acesso às pastas de origem
- Para o uso de disco local, as origens devem estar em pontos de montagem (ex.: `/mnt/hd1`)

Exemplo de inicialização do rclone:

```sh
rclone rcd --rc-addr 127.0.0.1:5572 --rc-user admin --rc-pass <senha> --drive-chunk-size 64M
```

## Compilação

```sh
go test ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/vvy-backup .
```

## Instalação

```sh
install -m 755 bin/vvy-backup /usr/local/bin/
install -m 644 deploy/vvy-backup.service /etc/systemd/system/
install -m 600 /dev/null /etc/vvy-backup.env
```

Conteúdo de `/etc/vvy-backup.env`:

```sh
BM_USER=admin                 # usuário do painel
BM_PASS=...
RC_USER=admin                 # usuário do rclone rcd
RC_PASS=...
DRIVE_BASE="gdrive:backup"    # pasta medida no painel; cada subpasta vira um segmento
```

Em seguida, execute `systemctl enable --now vvy-backup`. O painel fica disponível em `http://<host>:8080`.

## Jobs executados em outro host

Tarefas que dependem do host, como snapshots do Proxmox (`vzdump`), permanecem no cron daquela máquina.
A linha do cron passa a chamar o script por meio do `bm-run`:

```
0 1 * * 0 /root/scripts/bm-run "Snapshot semanal" /root/scripts/snapshot.sh >> /var/log/snapshot.log 2>&1
```

No painel, cadastre um job do tipo **Roda no host** com o mesmo nome e defina `BM_URL`, `BM_USER` e `BM_PASS`
em `/root/.config/vvy-backup.env`. Se o painel estiver indisponível, o script é executado normalmente e o
`bm-run` registra apenas um aviso no log.
