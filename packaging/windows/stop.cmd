@echo off
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0launcher.ps1" -Action stop
exit /b %errorlevel%
