#!/bin/bash
# GoClaw 启动脚本
# 环境变量从 .env 文件自动加载，无需在此设置

cd "$(dirname "$0")"
go run ./cmd/
