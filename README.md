# vvy-backup

Painel web leve para agendar, rodar e acompanhar backups feitos com o [rclone](https://rclone.org).
O formato do backup não muda: as pastas ficam espelhadas no destino (ex.: Google Drive), abríveis normalmente,
com uma lixeira datada para o que foi apagado ou alterado.

O app não copia nada sozinho. Ele manda o `rclone rcd` rodar `sync`/`copy`, acompanha o progresso pela API rc
e guarda o histórico num SQLite.

- Jobs de espelho, cópia e limpeza de lixeira, com agenda, simulação (dry-run) e pausa
- Árvore de pastas para escolher o que fica fora do backup
- Progresso ao vivo, histórico, volume e duração por execução
- Uso do destino por pasta
- Jobs que rodam em outra máquina (cron) informam o resultado com `deploy/bm-run`

Um binário Go (~14 MB), sem CGO. Dependências: `modernc.org/sqlite` e `robfig/cron`.

## Requisitos

- Linux com `rclone rcd` rodando (ex.: `rclone rcd --rc-addr 127.0.0.1:5572 --rc-user admin --rc-pass ...`)
- As pastas de origem acessíveis na mesma máquina

## Compilar

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/vvy-backup .
go test ./...
```

## Instalar

```sh
install -m 755 bin/vvy-backup /usr/local/bin/
install -m 644 deploy/vvy-backup.service /etc/systemd/system/
install -m 600 /dev/null /etc/vvy-backup.env   # e preencha:
#   BM_USER=admin            login do painel
#   BM_PASS=...
#   RC_USER=admin            login do rclone rcd
#   RC_PASS=...
#   DRIVE_BASE=gdrive:backup pasta medida no painel
systemctl enable --now vvy-backup
```

Painel em `http://<host>:8080`.

## Jobs de outra máquina

Na máquina que roda o script, troque a linha do cron por:

```
0 2 * * * /caminho/bm-run "Nome do job" /caminho/script.sh >> /var/log/script.log 2>&1
```

Crie no painel um job do tipo **Roda no host** com o mesmo nome, e as credenciais em
`/root/.config/vvy-backup.env` (`BM_URL`, `BM_USER`, `BM_PASS`). O script roda igual mesmo se o painel estiver fora do ar.
