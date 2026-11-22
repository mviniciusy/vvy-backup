@echo off
rem Compila e inicia a cópia local do vvy-backup e abre o navegador.
rem Configuração (senhas e endereços) em cmdScripts\config.cmd, que fica fora do git.
setlocal
chcp 65001 >nul
set "RAIZ=%~dp0.."
set "TOOLS=%~dp0..\..\.tools"

if not exist "%~dp0config.cmd" (
  echo Falta cmdScripts\config.cmd. Copie config.exemplo.cmd para config.cmd e preencha.
  pause
  exit /b 1
)
call "%~dp0config.cmd"

tasklist /fi "imagename eq vvy-backup.exe" | find /i "vvy-backup.exe" >nul
if not errorlevel 1 (
  echo O vvy-backup ja esta rodando. Abrindo o navegador.
  start "" "http://%VB_ADDR%/"
  exit /b 0
)

rem Go fica só na pasta do projeto (.tools), sem instalar nada no Windows.
set "GOROOT=%TOOLS%\go"
set "GOPATH=%TOOLS%\gopath"
set "GOMODCACHE=%TOOLS%\gopath\pkg\mod"
set "GOCACHE=%TOOLS%\gocache"
set "GOENV=%TOOLS%\go.env"
set "GOTOOLCHAIN=local"
set "PATH=%TOOLS%\go\bin;%PATH%"

cd /d "%RAIZ%"
echo Compilando...
go build -buildvcs=false -o bin\vvy-backup.exe . || (echo Falha ao compilar. & pause & exit /b 1)

echo Iniciando em http://%VB_ADDR%/
start "vvy-backup" /min bin\vvy-backup.exe -addr %VB_ADDR% -db "%VB_DB%" -rc %VB_RC% -drive-base "%VB_BASE%"
ping -n 3 127.0.0.1 >nul
start "" "http://%VB_ADDR%/"
endlocal
