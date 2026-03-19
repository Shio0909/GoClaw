@echo off
REM GoClaw 启动脚本
REM 环境变量从 .env 文件自动加载，无需在此设置

cd /d "%~dp0"
go run ./cmd/
