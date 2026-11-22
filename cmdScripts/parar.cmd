@echo off
rem Desliga a cópia local do vvy-backup.
chcp 65001 >nul
tasklist /fi "imagename eq vvy-backup.exe" | find /i "vvy-backup.exe" >nul
if errorlevel 1 (
  echo O vvy-backup nao esta rodando.
) else (
  taskkill /im vvy-backup.exe /f >nul && echo vvy-backup desligado.
)
ping -n 3 127.0.0.1 >nul
