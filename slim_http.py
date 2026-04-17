"""
GoClaw HTTP Handler Slimming Script
Removes bloat handler functions and route registrations from http.go
Keeps only the 70 core endpoints needed for an AI Agent Runtime.
"""

import re
import sys

KEEP_HANDLERS = {
    # Core Chat
    "handleChat",
    "handleStreamChat",
    "handleGetHistory",
    "handleDeleteSession",
    "handleExportSession",
    "handleChatCompletions",
    "handleOpenAIStream",
    "handleModels",
    "handleWebSocket",
    "handleWSChat",
    "handleEventStream",
    "handleBatchChat",
    # Sessions
    "handleListSessions",
    "handleSessionSearch",
    "handleForkSession",
    "handleRenameSession",
    "handleSessionStats",
    "handleBulkDeleteSessions",
    "handleGetSessionMeta",
    "handleSetSessionMeta",
    "handleSaveSession",
    # Messages
    "handleEditMessage",
    "handleDeleteMessage",
    "handleUndoMessage",
    "handleInjectMessage",
    "handleTrimMessages",
    "handleGetMessages",
    "handleGlobalSearch",
    # Memory
    "handleGetMemory",
    "handleTokenCount",
    "handleContextWindow",
    "handleToolUsage",
    # Checkpoints
    "handleCreateCheckpoint",
    "handleListCheckpoints",
    "handleRestoreCheckpoint",
    "handleListADKCheckpoints",
    "handleSaveADKCheckpoint",
    "handleGetADKCheckpoint",
    "handleDeleteADKCheckpoint",
    # Export/Import
    "handleBatchExport",
    "handleImportSession",
    "handleSessionFromTemplate",
    # Tools
    "handleListTools",
    "handleDisableTool",
    "handleEnableTool",
    "handleListDisabledTools",
    "handleToolDryRun",
    "handleBatchTools",
    "handleToolPipeline",
    # Config/Admin
    "handleGetConfig",
    "handleConfigReload",
    "handleAdminGC",
    "handleLockSession",
    "handleUnlockSession",
    "handleSetSystemPrompt",
    "handleGetSystemPrompt",
    # Observability
    "handleHealth",
    "handleDeepHealth",
    "handleMetrics",
    "handleAnalytics",
    "handleAuditQuery",
    "handleOpenAPISpec",
    "handleEnvInfo",
    # Webhooks
    "handleListWebhooks",
    "handleAddWebhook",
    "handleRemoveWebhook",
    # Plugins
    "handleListPlugins",
    "handleReloadPlugins",
    "handleUnloadPlugin",
    # Templates
    "handleListTemplates",
    "handleAddTemplate",
    "handleDeleteTemplate",
    # Tags
    "handleSetTags",
    "handleGetTags",
    # Other kept
    "handleCapabilities",
}

# Route patterns to KEEP (handler name in the route registration)
KEEP_ROUTES = {f"s.{h}" for h in KEEP_HANDLERS}

def find_func_boundaries(lines):
    """Find all top-level function boundaries (start_line, end_line, func_name)."""
    funcs = []
    i = 0
    while i < len(lines):
        line = lines[i]
        # Match func declarations
        m = re.match(r'^func\s+', line)
        if m:
            # Extract function name
            name_match = re.search(r'func\s+(?:\([^)]*\)\s+)?(\w+)', line)
            func_name = name_match.group(1) if name_match else "unknown"
            
            # Find the opening brace
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

def process_routes(lines):
    """Remove route registrations for non-kept handlers."""
    result = []
    removed_routes = 0
    for line in lines:
        stripped = line.strip()
        if stripped.startswith('mux.HandleFunc('):
            # Check if this route should be kept
            keep = False
            for handler_ref in KEEP_ROUTES:
                if handler_ref in stripped:
                    keep = True
                    break
            if not keep:
                removed_routes += 1
                continue
        result.append(line)
    return result, removed_routes

def main():
    filepath = r"E:\llm\GoClaw\gateway\http.go"
    
    with open(filepath, 'r', encoding='utf-8') as f:
        content = f.read()
    
    lines = content.split('\n')
    original_lines = len(lines)
    
    # Step 1: Find all function boundaries
    funcs = find_func_boundaries(lines)
    print(f"Found {len(funcs)} functions total")
    
    # Step 2: Identify handler functions to remove
    handler_funcs = [(s, e, n) for s, e, n in funcs 
                     if n.startswith('handle') and n not in KEEP_HANDLERS]
    
    print(f"\nHandlers to KEEP: {len(KEEP_HANDLERS)}")
    print(f"Handler functions to REMOVE: {len(handler_funcs)}")
    
    # Print what we're removing
    print("\n--- REMOVING these handlers ---")
    for s, e, n in sorted(handler_funcs, key=lambda x: x[0]):
        print(f"  {n} (lines {s+1}-{e+1}, {e-s+1} lines)")
    
    # Step 3: Mark lines to remove (handler functions)
    remove_lines = set()
    for start, end, name in handler_funcs:
        for i in range(start, end + 1):
            remove_lines.add(i)
        # Also remove blank lines immediately before the function (comments/spacing)
        j = start - 1
        while j >= 0 and (lines[j].strip() == '' or lines[j].strip().startswith('//')):
            remove_lines.add(j)
            j -= 1
    
    # Step 4: Remove marked lines
    new_lines = [lines[i] for i in range(len(lines)) if i not in remove_lines]
    
    # Step 5: Remove route registrations
    new_lines, removed_routes = process_routes(new_lines)
    
    # Step 6: Write result
    new_content = '\n'.join(new_lines)
    with open(filepath, 'w', encoding='utf-8') as f:
        f.write(new_content)
    
    print(f"\n--- SUMMARY ---")
    print(f"Original lines: {original_lines}")
    print(f"Lines removed (handlers): {len(remove_lines)}")
    print(f"Routes removed: {removed_routes}")
    print(f"New line count: {len(new_lines)}")
    print(f"Reduction: {original_lines - len(new_lines)} lines ({(original_lines - len(new_lines))/original_lines*100:.1f}%)")

if __name__ == '__main__':
    main()
