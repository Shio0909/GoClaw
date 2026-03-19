---
name: daily_brief
description: 每日简报 - 搜索并整理今日科技和AI领域新闻
version: "1.0"
requires:
  tools:
    - web_search
---

# 每日简报

当用户说"今日简报"、"每日总结"或类似的话时：

1. 使用 web_search 搜索今天的科技新闻
2. 使用 web_search 搜索 AI/LLM 领域最新动态
3. 整理为简洁的中文简报，格式如下：

```
📰 今日科技简报 (日期)

🤖 AI 动态
- ...

💻 技术新闻
- ...

📌 值得关注
- ...
```
