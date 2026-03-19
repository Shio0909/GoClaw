#!/bin/bash
# 交叉编译 GoClaw 为 Linux amd64
set -e

echo "🐾 Building GoClaw for Linux amd64..."
GOOS=linux GOARCH=amd64 go build -o goclaw-linux-amd64 ./cmd/
echo "✅ Built: goclaw-linux-amd64"

echo ""
echo "部署步骤："
echo "  1. scp goclaw-linux-amd64 .env mcp_servers.json user@server:~/goclaw/"
echo "  2. scp -r skills/ memory_data/ user@server:~/goclaw/"
echo "  3. ssh user@server 'cd ~/goclaw && chmod +x goclaw-linux-amd64 && ./goclaw-linux-amd64'"
