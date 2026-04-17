"""
GoClaw HTTP Handler Slimming Script v2
Removes ~50 non-essential routes/handlers, keeping only 19 core endpoints.
Also cleans up http_test.go.
"""

import re

# ---- Handlers to KEEP in http.go ----
KEEP_HANDLERS = {
    "handleChat",
    "handleStreamChat",
    "handleGetHistory",
    "handleDeleteSession",
    "handleListTools",
    "handleDisableTool",
    "handleEnableTool",
    "handleListSessions",
    "handleForkSession",
    "handleRenameSession",
    "handleHealth",
    "handleMetrics",
    "handleGetConfig",
    "handleConfigReload",
    "handleChatCompletions",
    "handleOpenAIStream",
    "handleModels",
    "handleWebSocket",
    "handleWSChat",
    "handleGetMemory",
    "handleGetSystemPrompt",
    "handleSetSystemPrompt",
    # helpers (not registered as routes, but called internally)
    "emitEvent",
}

# ---- Routes to KEEP in mux registrations ----
KEEP_ROUTE_HANDLERS = {
    "s.handleChat",
    "s.handleGetHistory",
    "s.handleDeleteSession",
    "s.handleListTools",
    "s.handleDisableTool",
    "s.handleEnableTool",
    "s.handleListSessions",
    "s.handleForkSession",
    "s.handleRenameSession",
    "s.handleHealth",
    "s.handleMetrics",
    "s.handleGetConfig",
    "s.handleConfigReload",
    "s.handleChatCompletions",
    "s.handleModels",
    "s.handleWebSocket",
    "s.handleGetMemory",
    "s.handleGetSystemPrompt",
    "s.handleSetSystemPrompt",
}

# ---- Handlers referenced in tests that don't exist or are being deleted ----
# Tests referencing any of these will be removed from http_test.go
DELETED_OR_NONEXISTENT_HANDLERS = {
    "handleExportSession",
    "handleOpenAPISpec",
    "handleSessionSearch",
    "handleSetTags",
    "handleGetTags",
    "handleDeleteTag",
    "handleBatchChat",
    "handleAdminGC",
    "handleAnalytics",
    "handleDeepHealth",
    "handleListDisabledTools",
    "handleListPlugins",
    "handleReloadPlugins",
    "handleUnloadPlugin",
    "handleEnvInfo",
    "handleToolDryRun",
    "handleLockSession",
    "handleUnlockSession",
    "handleSessionStats",
    "handleBatchTools",
    "handleListTemplates",
    "handleAddTemplate",
    "handleDeleteTemplate",
    "handleTrimMessages",
    "handleImportSession",
    "handleInjectMessage",
    "handleEventStream",
    "handleCreateCheckpoint",
    "handleListCheckpoints",
    "handleRestoreCheckpoint",
    "handleEditMessage",
    "handleDeleteMessage",
    "handleUndoMessage",
    "handleBulkDeleteSessions",
    "handleToolPipeline",
    "handleSaveSession",
    "handleGetMessages",
    "handleTokenCount",
    "handleGetSessionMeta",
    "handleSetSessionMeta",
    "handleBatchExport",
    "handleGlobalSearch",
    "handleToolUsage",
    "handleCapabilities",
    "handleContextWindow",
    "handleSessionFromTemplate",
    "handleListADKCheckpoints",
    "handleSaveADKCheckpoint",
    "handleGetADKCheckpoint",
    "handleDeleteADKCheckpoint",
    "handleRateLimitStatus",
    "handleAnnotateSession",
    "handleGetAnnotations",
    "handleAnnotateMessage",
}


def find_func_boundaries(lines):
    """Find all top-level function boundaries: (start_idx, end_idx, func_name)."""
    funcs = []
    i = 0
    while i < len(lines):
        line = lines[i]
        m = re.match(r'^func\s+', line)
        if m:
            name_match = re.search(r'func\s+(?:\([^)]*\)\s+)?(\w+)', line)
            func_name = name_match.group(1) if name_match else "unknown"
            start = i
            brace_depth = 0
            found_open = False
            j = i
            while j < len(lines):
                for ch in lines[j]:
                    if ch == '{':
                        brace_depth += 1
                        found_open = True
                    elif ch == '}':
                        brace_depth -= 1
                if found_open and brace_depth == 0:
                    funcs.append((start, j, func_name))
                    i = j + 1
                    break
                j += 1
            else:
                i += 1
        else:
            i += 1
    return funcs


def find_type_boundaries(lines):
    """Find type definition boundaries: (start_idx, end_idx, type_name)."""
    types = []
    i = 0
    while i < len(lines):
        line = lines[i]
        m = re.match(r'^type\s+(\w+)\s+struct\s*\{', line)
        if m:
            type_name = m.group(1)
            start = i
            brace_depth = 0
            j = i
            while j < len(lines):
                for ch in lines[j]:
                    if ch == '{':
                        brace_depth += 1
                    elif ch == '}':
                        brace_depth -= 1
                if brace_depth == 0:
                    types.append((start, j, type_name))
                    i = j + 1
                    break
                j += 1
            else:
                i += 1
        else:
            i += 1
    return types


def process_http_go(filepath):
    with open(filepath, 'r', encoding='utf-8') as f:
        content = f.read()

    lines = content.split('\n')
    original_lines = len(lines)
    remove_lines = set()

    # --- Find and remove handler functions ---
    funcs = find_func_boundaries(lines)
    deleted_funcs = [(s, e, n) for s, e, n in funcs
                     if n.startswith('handle') and n not in KEEP_HANDLERS]

    print(f"Handler functions to remove: {len(deleted_funcs)}")
    for s, e, n in sorted(deleted_funcs, key=lambda x: x[0]):
        print(f"  {n} (lines {s+1}-{e+1})")
        for i in range(s, e + 1):
            remove_lines.add(i)
        # Remove preceding blank lines and comments
        j = s - 1
        while j >= 0 and (lines[j].strip() == '' or lines[j].strip().startswith('//')):
            remove_lines.add(j)
            j -= 1

    # --- Remove specific struct fields from HTTPServer ---
    fields_to_remove = {
        'promptTemplates sync.Map',
        'sessionTemplates sync.Map',
        'adkCheckpointStore *agent.FileCheckPointStore',
        'endpointStats sync.Map',
        'toolUsage sync.Map',
        'pluginMgr *tools.PluginManager',
    }
    for i, line in enumerate(lines):
        stripped = line.strip()
        for field in fields_to_remove:
            if field in stripped:
                remove_lines.add(i)
                break

    # --- Remove PluginDir from HTTPServerConfig ---
    for i, line in enumerate(lines):
        if 'PluginDir' in line and 'string' in line:
            remove_lines.add(i)

    # --- Remove endpointStats usage in withRequestLog ---
    # Remove the block: "// 端点延迟统计\n\tkey := ...\n\t...\n\t}\n"
    in_endpoint_stats_block = False
    for i, line in enumerate(lines):
        stripped = line.strip()
        if '端点延迟统计' in stripped or (stripped.startswith('key :=') and 'r.Method' in stripped and i not in remove_lines):
            # Check context: we're inside withRequestLog
            # Mark this line and the next few until the closing block
            if '端点延迟统计' in stripped:
                in_endpoint_stats_block = True
                remove_lines.add(i)
                continue
        if in_endpoint_stats_block:
            remove_lines.add(i)
            # Stop after closing the if block (after "stat.errors.Add(1)")
            if stripped == '}':
                in_endpoint_stats_block = False

    # Also remove the "key :=" and "stat :=" lines if we missed them
    for i, line in enumerate(lines):
        stripped = line.strip()
        if (stripped.startswith('key := r.Method') or
            stripped.startswith('val, _ := s.endpointStats') or
            stripped.startswith('stat := val.(*endpointStat)') or
            stripped.startswith('stat.calls.Add') or
            stripped.startswith('stat.totalMs.Add') or
            (stripped.startswith('stat.errors.Add') and 'rw.status' in lines[i-1] if i > 0 else False)):
            remove_lines.add(i)
    # Also clean the if rw.status >= 400 block inside withRequestLog
    for i, line in enumerate(lines):
        if 'if rw.status >= 400' in line:
            # Check if this is in withRequestLog (not elsewhere)
            # Look back to find function context
            for k in range(i-1, max(0, i-30), -1):
                if 'withRequestLog' in lines[k]:
                    remove_lines.add(i)
                    if i+1 < len(lines) and 'stat.errors' in lines[i+1]:
                        remove_lines.add(i+1)
                    if i+2 < len(lines) and lines[i+2].strip() == '}':
                        remove_lines.add(i+2)
                    break

    # --- Remove ADK checkpoint store initialization in NewHTTPServer ---
    # Find the block starting with "// ADK 检查点存储初始化"
    adk_block_start = None
    for i, line in enumerate(lines):
        if 'ADK 检查点存储初始化' in line or 'ADK 检查点存储' in line:
            if adk_block_start is None:
                adk_block_start = i
    
    # Find and remove the complete ADK init block
    i = 0
    while i < len(lines):
        if 'ADK 检查点存储初始化' in lines[i]:
            # Remove from this line until we close the if block
            start = i
            remove_lines.add(i)
            j = i + 1
            while j < len(lines):
                line = lines[j].strip()
                remove_lines.add(j)
                if line == '' and j > i + 5:
                    break
                # Stop after the closing log block
                if 'ADK 检查点存储:' in lines[j]:
                    # Also remove the closing brace and blank line
                    if j+1 < len(lines):
                        remove_lines.add(j+1)  # closing brace
                    if j+2 < len(lines) and lines[j+2].strip() == '':
                        remove_lines.add(j+2)
                    break
                j += 1
            break
        i += 1

    # --- Remove plugin manager initialization in NewHTTPServer ---
    i = 0
    in_plugin_block = False
    while i < len(lines):
        line = lines[i]
        if '// 加载插件' in line:
            in_plugin_block = True
            remove_lines.add(i)
        elif in_plugin_block:
            remove_lines.add(i)
            if line.strip() == '}' and i > 0:
                # Check if the next non-empty line is 'return srv'
                j = i + 1
                while j < len(lines) and lines[j].strip() == '':
                    j += 1
                if j < len(lines) and 'return srv' in lines[j]:
                    in_plugin_block = False
        i += 1

    # --- Remove struct types only used by deleted handlers ---
    types_to_remove = {'promptTemplate', 'checkResult', 'endpointStat', 'toolUsageStats', 'sessionTemplate'}
    type_defs = find_type_boundaries(lines)
    for s, e, n in type_defs:
        if n in types_to_remove:
            print(f"Removing type {n} (lines {s+1}-{e+1})")
            for i in range(s, e + 1):
                remove_lines.add(i)
            # Remove preceding blank lines and comments
            j = s - 1
            while j >= 0 and (lines[j].strip() == '' or lines[j].strip().startswith('//')):
                remove_lines.add(j)
                j -= 1

    # --- Remove route registrations ---
    new_lines = []
    removed_routes = 0
    for i, line in enumerate(lines):
        if i in remove_lines:
            continue
        stripped = line.strip()
        if stripped.startswith('mux.HandleFunc('):
            keep = any(h in stripped for h in KEEP_ROUTE_HANDLERS)
            if not keep:
                removed_routes += 1
                continue
        new_lines.append(line)

    # --- Remove stray comment blocks that were between deleted sections ---
    # Remove consecutive comment-only lines that are now orphaned
    # (this is best-effort cleanup)

    print(f"\nSummary for {filepath}:")
    print(f"  Original lines: {original_lines}")
    print(f"  Lines removed: {len(remove_lines)}")
    print(f"  Routes removed: {removed_routes}")
    print(f"  New line count: {len(new_lines)}")

    new_content = '\n'.join(new_lines)
    with open(filepath, 'w', encoding='utf-8') as f:
        f.write(new_content)


def find_test_func_boundaries(lines):
    """Find test function boundaries in _test.go files."""
    funcs = []
    i = 0
    while i < len(lines):
        line = lines[i]
        m = re.match(r'^func\s+(Test\w+|Benchmark\w+|Example\w+)', line)
        if m:
            func_name = m.group(1)
            start = i
            brace_depth = 0
            found_open = False
            j = i
            while j < len(lines):
                for ch in lines[j]:
                    if ch == '{':
                        brace_depth += 1
                        found_open = True
                    elif ch == '}':
                        brace_depth -= 1
                if found_open and brace_depth == 0:
                    funcs.append((start, j, func_name))
                    i = j + 1
                    break
                j += 1
            else:
                i += 1
        else:
            i += 1
    return funcs


def process_http_test_go(filepath):
    with open(filepath, 'r', encoding='utf-8') as f:
        content = f.read()

    lines = content.split('\n')
    original_lines = len(lines)

    test_funcs = find_test_func_boundaries(lines)
    remove_lines = set()
    removed_tests = []

    for start, end, name in test_funcs:
        # Check if this test references any deleted/nonexistent handler
        func_body = '\n'.join(lines[start:end+1])
        should_remove = any(h in func_body for h in DELETED_OR_NONEXISTENT_HANDLERS)
        if should_remove:
            removed_tests.append(name)
            for i in range(start, end + 1):
                remove_lines.add(i)
            # Remove preceding blank lines and comments
            j = start - 1
            while j >= 0 and (lines[j].strip() == '' or lines[j].strip().startswith('//')):
                remove_lines.add(j)
                j -= 1

    new_lines = [lines[i] for i in range(len(lines)) if i not in remove_lines]

    print(f"\nSummary for {filepath}:")
    print(f"  Original lines: {original_lines}")
    print(f"  Test functions removed: {len(removed_tests)}")
    for t in sorted(removed_tests):
        print(f"    - {t}")
    print(f"  New line count: {len(new_lines)}")

    new_content = '\n'.join(new_lines)
    with open(filepath, 'w', encoding='utf-8') as f:
        f.write(new_content)


if __name__ == '__main__':
    process_http_go(r"E:\llm\GoClaw\gateway\http.go")
    process_http_test_go(r"E:\llm\GoClaw\gateway\http_test.go")
