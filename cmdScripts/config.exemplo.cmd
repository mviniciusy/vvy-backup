@echo off
rem Copie para config.cmd e preencha. O config.cmd fica fora do git.

rem Login da página local
set "BM_USER=dev"
set "BM_PASS=troque-esta-senha"

rem rclone rcd (o servidor onde o backup roda)
set "RC_USER=admin"
set "RC_PASS=senha-do-rclone"
set "VB_RC=http://IP-DO-SERVIDOR:5572"

rem Pasta do backup no Drive, endereço local e banco local
set "VB_BASE=gdrive:backup"
set "VB_ADDR=127.0.0.1:8099"
set "VB_DB=%~dp0..\bin\local.db"
